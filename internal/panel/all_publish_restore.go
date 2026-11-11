package panel

import (
	"context"
	"database/sql"
	"fmt"
)

// 只在十秒候选定时器中累计，页面读取、摘除检查与提交前复核均不制造样本。
// 调用者持 entryMu → controlMu；重启、读取失败与任何中断都从零开始。
func (p *Panel) checkAllRestores(ctx context.Context, states map[[2]string]entryMemberState, previous map[[2]string]entryFailbackWindow) (map[[2]string]entryFailbackWindow, error) {
	groups, err := p.allPolicyGroups(ctx)
	if err != nil {
		return nil, err
	}
	next := map[[2]string]entryFailbackWindow{}
	for _, g := range groups {
		allowed, err := recoveryGroupAllowed(ctx, p.db, g)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		if err = p.restoreAllGroup(ctx, g, states, previous, next); err != nil {
			return nil, err
		}
	}
	return next, nil
}

func (p *Panel) restoreAllGroup(ctx context.Context, g string, states map[[2]string]entryMemberState, previous, next map[[2]string]entryFailbackWindow) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireNoGroupAction(ctx, tx, g); err != nil {
		return err
	}
	var version int64
	if err = tx.QueryRowContext(ctx, "SELECT desired_version FROM entry_publications WHERE group_id=?", g).Scan(&version); err != nil {
		return err
	}
	members, err := loadAllMembers(ctx, tx, g)
	if err != nil {
		return err
	}
	var changes []ManagedRecordChange
	var restored []allMemberPublication
	for _, m := range members {
		if m.Record == nil || m.ExpectedEnabled || m.ManualLocked || m.RemovalActionID != "" || m.LastError != "" {
			continue
		}
		allowed, err := allMemberRotationRecovered(ctx, tx, g, m)
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		key := [2]string{g, m.NodeID}
		state := states[key]
		fresh, err := p.offlineCandidateCurrent(ctx, tx, state)
		if err != nil {
			return err
		}
		if !fresh {
			continue
		}
		match, err := allRestoreAddressMatches(ctx, tx, m, state)
		if err != nil {
			return err
		}
		if !match {
			message := "加回暂停：记录地址、面板确认地址与健康证据地址不一致，请先在节点页同步地址"
			if _, err = tx.ExecContext(ctx, "UPDATE entry_member_publications SET reason=? WHERE group_id=? AND node_id=?", message, g, m.NodeID); err != nil {
				return err
			}
			if err = p.memberDecisionTx(ctx, tx, g, m, "entry_member_address_mismatch", fmt.Sprint(version)+":"+state.DomesticEvidence.PublicIPv4+":"+m.Record.CurrentValue+":"+m.ExpectedValue, message, ""); err != nil {
				return err
			}
			continue
		}
		old := previous[key]
		if old.generation != m.RestoreGeneration {
			old = entryFailbackWindow{}
		}
		w := advanceEntrySuccessWindow(old, m.NodeID, state.DomesticEvidence.PublicIPv4, version, *state.CheckedAt)
		w.generation = m.RestoreGeneration
		next[key] = w
		if w.successes < entryFailbackSuccesses {
			continue
		}
		observed, err := p.observeEntryTx(ctx, tx, g, m.NodeID, "auto_restore", fmt.Sprintf("%d:%s:%d:%s", version, m.Record.ID, m.Record.BindingGeneration, state.DomesticEvidence.PublicIPv4), observationDetail("auto_restore", m.NodeID, state.DomesticEvidence.PublicIPv4))
		if err != nil {
			return err
		}
		if observed {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_value=?,expected_enabled=1,address_pending=0,reason='自动加回',withdrawn_at=NULL WHERE group_id=? AND node_id=?`, state.DomesticEvidence.PublicIPv4, g, m.NodeID); err != nil {
			return err
		}
		b := m.Record
		changes = append(changes, ManagedRecordChange{ManagedRecordID: b.ID, BindingGeneration: b.BindingGeneration, Value: state.DomesticEvidence.PublicIPv4, Enabled: true, ExplicitEnable: true})
		restored = append(restored, m)
	}
	var a EntryAction
	if len(changes) > 0 {
		a, err = p.submitAutomaticMemberActionTx(ctx, tx, g, restored[0].NodeID, "auto_restore", version, changes, 1)
		if err != nil {
			return err
		}
		for _, m := range restored {
			state := states[[2]string{g, m.NodeID}]
			fresh, err := p.offlineCandidateCurrent(ctx, tx, state)
			if err != nil || !fresh {
				for _, selected := range restored {
					delete(next, [2]string{g, selected.NodeID})
				}
				return err
			}
			match, err := allRestoreAddressMatches(ctx, tx, m, state)
			if err != nil || !match {
				for _, selected := range restored {
					delete(next, [2]string{g, selected.NodeID})
				}
				return err
			}
			if err = p.memberActionDecisionTx(ctx, tx, g, m, a, "连续六次候选检查成功，自动加回"); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if a.ID != "" {
		p.coordinator.wake(actionWorkerKey(a))
		for _, m := range restored {
			delete(next, [2]string{g, m.NodeID})
		}
	}
	return nil
}

// 因本成员参与的受控换 IP 尚未恢复时，周期自测不能代替本次新 IP 复检。
// 后续合法手动换 IP 可以接续恢复；换绑后不继承旧记录的恢复门槛。
func allMemberRotationRecovered(ctx context.Context, tx *sql.Tx, g string, m allMemberPublication) (bool, error) {
	var waiting bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM rotation_actions a
 WHERE a.id=(SELECT id FROM rotation_actions WHERE node_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1)
 AND EXISTS(SELECT 1 FROM rotation_member_barriers b JOIN rotation_actions original ON original.id=b.action_id
 WHERE original.node_id=a.node_id AND b.group_id=? AND b.managed_record_id=? AND b.binding_generation=?)
 AND NOT (a.state='cancelled' AND a.permit_sent=0)
 AND NOT (a.state='changed' AND a.recheck_status='recovered' AND a.new_ip=(SELECT confirmed_ipv4 FROM node_regions WHERE node_id=a.node_id)))`, m.NodeID, g, m.Record.ID, m.Record.BindingGeneration).Scan(&waiting)
	return !waiting, err
}

// 新绑定的成员尚无本地期望地址时，仍须以绑定读回的记录地址核对三方一致。
func allRestoreAddressMatches(ctx context.Context, tx *sql.Tx, m allMemberPublication, state entryMemberState) (bool, error) {
	var confirmed string
	err := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT confirmed_ipv4 FROM node_regions WHERE node_id=?),'')", m.NodeID).Scan(&confirmed)
	ip := state.DomesticEvidence.PublicIPv4
	return confirmed != "" && confirmed == ip && m.Record.CurrentValue == ip && (m.ExpectedValue == "" || m.ExpectedValue == ip), err
}
