package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"nbpass/internal/publicip"
)

type allWithdrawal struct {
	reason, identity, check string
	deadline                int64
	entry                   offlineEntry
}

func (p *Panel) allPolicyGroups(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id FROM entry_groups g WHERE publication_mode='all' AND first_published_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM entry_actions a WHERE a.group_id=g.id AND a.stage IN `+pendingRecordStages+`) ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []string
	for rows.Next() {
		var g string
		if err = rows.Scan(&g); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// 与主备策略隔离；同一组的所有摘除决定在一个事务内完成。
func (p *Panel) checkAllWithdrawals(ctx context.Context) error {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	groups, err := p.allPolicyGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		allowed, err := recoveryGroupAllowed(ctx, p.db, g)
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		members, err := loadAllMembers(ctx, p.db, g)
		if err != nil {
			return err
		}
		reasons := map[string]allWithdrawal{}
		for _, m := range members {
			if m.Record == nil || !m.ExpectedEnabled || !m.Record.CurrentEnabled || m.RemovalActionID != "" || m.LastError != "" {
				continue
			}
			r, err := p.allWithdrawalReason(ctx, g, m)
			if err != nil {
				return err
			}
			if r.reason != "" {
				reasons[m.NodeID] = r
			}
		}
		if err = p.decideAllWithdrawals(ctx, g, reasons); err != nil {
			return err
		}
	}
	return nil
}

func (p *Panel) allWithdrawalReason(ctx context.Context, g string, m allMemberPublication) (allWithdrawal, error) {
	var r allWithdrawal
	now := p.deps.Clock.Now()
	inputs, err := p.loadEntryReadinessInputs(ctx, m.NodeID, now)
	if err != nil || inputs.maintenance {
		return r, err
	}
	if inputs.blocked {
		err = p.db.QueryRowContext(ctx, "SELECT coalesce((SELECT incident_id FROM entry_blocked_incidents WHERE node_id=?),'')", m.NodeID).Scan(&r.identity)
		r.reason = "被墙自动摘除"
		return r, err
	}
	e := offlineEntry{group: g, node: m.NodeID, ip: m.ExpectedValue, record: m.Record.ID, binding: m.Record.BindingGeneration}
	var last, disconnected, maintenance sql.NullInt64
	var ip string
	err = p.db.QueryRowContext(ctx, `SELECT n.last_seen_at,n.disconnected_at,n.maintenance_until,coalesce(r.public_ipv4,''),n.check_port,n.desired_revision,s.desired_version FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id JOIN entry_publications s ON s.group_id=? WHERE n.id=?`, g, m.NodeID).Scan(&last, &disconnected, &maintenance, &ip, &e.port, &e.revision, &e.version)
	if err != nil {
		return r, err
	}
	if p.presence(e.node, last, disconnected, maintenance, now).status != "offline" || ip != e.ip {
		return r, nil
	}
	if _, err = publicip.Parse(e.ip); err != nil {
		return r, nil
	}
	if err = p.observeOffline(ctx, e.node, now); err != nil {
		return r, err
	}
	if err = p.db.QueryRowContext(ctx, "SELECT id FROM offline_incidents WHERE node_id=? AND recovered_at IS NULL", e.node).Scan(&e.incident); err != nil {
		return r, err
	}
	var id, outcome string
	var issued, deadline int64
	err = p.db.QueryRowContext(ctx, `SELECT id,outcome,issued_at,deadline FROM entry_offline_checks WHERE group_id=? AND node_id=? AND incident_id=? AND managed_record_id=? AND binding_generation=? AND target_ip=? AND target_port=? AND config_revision=? ORDER BY issued_at DESC LIMIT 1`, g, e.node, e.incident, e.record, e.binding, e.ip, e.port, e.revision).Scan(&id, &outcome, &issued, &deadline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	if err == nil {
		if outcome == "pending" {
			if now.UnixNano() >= deadline {
				tx, err := p.db.BeginTx(ctx, nil)
				if err != nil {
					return r, err
				}
				defer tx.Rollback()
				if err = p.finishOfflineCheck(ctx, tx, id, true); err != nil {
					return r, err
				}
				return r, tx.Commit()
			}
			return r, nil
		}
		if outcome == "unreachable" && now.UnixNano() < deadline {
			return allWithdrawal{reason: "离线且国内检测点确认不可达，自动摘除", identity: fmt.Sprint(e.incident), check: id, deadline: deadline, entry: e}, nil
		}
		if now.UnixNano() < issued+int64(time.Minute) {
			return r, nil
		}
	}
	return r, p.scheduleOfflineCheck(ctx, e, now)
}

func (p *Panel) allWithdrawalCurrent(ctx context.Context, tx *sql.Tx, m allMemberPublication, r allWithdrawal) (bool, error) {
	var last, disconnected, maintenance sql.NullInt64
	var ip string
	var port int
	var revision uint64
	err := tx.QueryRowContext(ctx, `SELECT n.last_seen_at,n.disconnected_at,n.maintenance_until,coalesce(r.public_ipv4,''),n.check_port,n.desired_revision FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id WHERE n.id=?`, m.NodeID).Scan(&last, &disconnected, &maintenance, &ip, &port, &revision)
	if err != nil {
		return false, err
	}
	now := p.deps.Clock.Now()
	if maintenance.Valid && now.UnixNano() < maintenance.Int64 {
		return false, nil
	}
	if r.check == "" {
		return true, nil
	} // 被墙输入受 controlMu 保护，时间不会令异常自行消失。
	if m.Record == nil || m.Record.ID != r.entry.record || m.Record.BindingGeneration != r.entry.binding || m.ExpectedValue != r.entry.ip || now.UnixNano() >= r.deadline || ip != r.entry.ip || port != r.entry.port || revision != r.entry.revision || p.presence(m.NodeID, last, disconnected, maintenance, now).status != "offline" {
		return false, nil
	}
	var current bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entry_offline_checks c JOIN offline_incidents i ON i.id=c.incident_id WHERE c.id=? AND c.outcome='unreachable' AND i.recovered_at IS NULL AND c.managed_record_id=? AND c.binding_generation=?)`, r.check, m.Record.ID, m.Record.BindingGeneration).Scan(&current)
	return current, err
}

func (p *Panel) decideAllWithdrawals(ctx context.Context, g string, reasons map[string]allWithdrawal) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = requireNoGroupAction(ctx, tx, g); err != nil {
		return err
	}
	var minimum int
	var version int64
	var policyGeneration int64
	if err = tx.QueryRowContext(ctx, `SELECT g.minimum_kept,s.desired_version,g.minimum_kept_generation FROM entry_groups g JOIN entry_publications s ON s.group_id=g.id WHERE g.id=? AND g.first_published_at IS NOT NULL`, g).Scan(&minimum, &version, &policyGeneration); err != nil {
		return err
	}
	members, err := loadAllMembers(ctx, tx, g)
	if err != nil {
		return err
	}
	minimum = min(minimum, len(members))
	enabled := 0
	var candidates []allMemberPublication
	for _, m := range members {
		if m.Record == nil || !m.Record.CurrentEnabled || !m.ExpectedEnabled || m.LastError != "" {
			continue
		}
		enabled++
		if r, ok := reasons[m.NodeID]; ok {
			current, err := p.allWithdrawalCurrent(ctx, tx, m, r)
			if err != nil {
				return err
			}
			if current {
				candidates = append(candidates, m)
			}
		}
	}
	// 优先保留仍在记录中的成员，再按组内顺序；不启用任何暂停成员凑数。
	if enabled < minimum {
		for _, m := range members {
			if m.Record == nil {
				continue
			}
			message := fmt.Sprintf("当前启用记录 %d 条，少于最少保留数 %d；不为凑数加回暂停成员，请管理员核对", enabled, minimum)
			if err = p.memberDecisionTx(ctx, tx, g, m, "entry_member_minimum_unmet", fmt.Sprintf("%d:%d:%d:%d", version, policyGeneration, minimum, enabled), message, ""); err != nil {
				return err
			}
			break
		}
	}
	keep := max(0, minimum-(enabled-len(candidates)))
	changes := []ManagedRecordChange{}
	var withdrawn []allMemberPublication
	for i, m := range candidates {
		r := reasons[m.NodeID]
		if i < keep {
			message := "因最少保留数保留：" + r.reason
			if _, err = tx.ExecContext(ctx, "UPDATE entry_member_publications SET reason=? WHERE group_id=? AND node_id=?", message, g, m.NodeID); err != nil {
				return err
			}
			if err = p.memberDecisionTx(ctx, tx, g, m, "entry_member_kept", r.identity+fmt.Sprintf(":%d:%d", minimum, policyGeneration), message, ""); err != nil {
				return err
			}
			continue
		}
		observed, err := p.observeEntryTx(ctx, tx, g, m.NodeID, "auto_withdraw", fmt.Sprintf("%d:%s:%s:%d", version, r.identity, m.Record.ID, m.Record.BindingGeneration), observationDetail("auto_withdraw", m.NodeID, r.reason))
		if err != nil {
			return err
		}
		if observed {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_enabled=0,reason=?,withdrawn_at=? WHERE group_id=? AND node_id=?`, r.reason, p.deps.Clock.Now().UnixNano(), g, m.NodeID); err != nil {
			return err
		}
		b := m.Record
		changes = append(changes, ManagedRecordChange{ManagedRecordID: b.ID, BindingGeneration: b.BindingGeneration, Enabled: false})
		withdrawn = append(withdrawn, m)
	}
	var a EntryAction
	if len(changes) > 0 {
		a, err = p.submitAutomaticMemberActionTx(ctx, tx, g, withdrawn[0].NodeID, "auto_withdraw", version, changes, minimum)
		if err != nil {
			return err
		}
		for _, m := range withdrawn {
			r := reasons[m.NodeID]
			// 动作改变组版本后，仍按同一成员证据复核，不能延长检测有效期。
			current, err := p.allWithdrawalCurrent(ctx, tx, m, r)
			if err != nil || !current {
				return err
			}
			if err = p.memberActionDecisionTx(ctx, tx, g, m, a, r.reason); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err == nil && a.ID != "" {
		p.coordinator.wake(actionWorkerKey(a))
	}
	return err
}

// 自动摘除/加回的唯一提交点。票 07 在这里复核观测状态；外部调用前的复核
// 位于 entryCoordinator.executeRecord 的写值/启停分支，已发出的结果仍按原动作核对。
func (p *Panel) submitAutomaticMemberActionTx(ctx context.Context, tx *sql.Tx, g, node, kind string, version int64, changes []ManagedRecordChange, minimum int) (EntryAction, error) {
	if observed, err := p.observeRecordTx(ctx, tx, RecordActionRequest{GroupID: g, NodeID: node, Kind: kind, ExpectedVersion: version, Records: changes}); err != nil || observed {
		return EntryAction{GroupID: g, NodeID: node, Kind: kind, Stage: entryObserved}, err
	}
	a, err := p.submitMemberActionTx(ctx, tx, g, node, kind, version, changes)
	if err == nil && minimum > 1 {
		a.MinimumEnabled = minimum
		_, err = tx.ExecContext(ctx, "UPDATE entry_actions SET minimum_enabled=? WHERE id=?", minimum, a.ID)
	}
	return a, err
}
