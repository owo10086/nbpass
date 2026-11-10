package panel

import (
	"context"
	"database/sql"
)

// 开关、作废逐记录动作、恢复未执行期望、释放许可额度必须在同一事务。
// 已持 entryMu → controlMu，DNS 的最后写入检查持 entryMu，许可投递持 controlMu。
func (p *Panel) cancelObservedActionsTx(ctx context.Context, tx *sql.Tx, group string) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+actionColumns+" FROM entry_actions WHERE group_id=? AND stage NOT IN "+terminalActionStages, group)
	if err != nil {
		return err
	}
	var actions []EntryAction
	for rows.Next() {
		a, e := scanAction(rows)
		if e != nil {
			err = e
			break
		}
		if automaticEntryKind(a.Kind) {
			actions = append(actions, a)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range actions {
		if err = p.cancelObservedDNSTx(ctx, tx, a); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT id,node_id FROM rotation_actions WHERE origin='automatic' AND permit_sent=0 AND state='authorized' AND node_id IN (SELECT node_id FROM entry_group_members WHERE group_id=?)`, group)
	if err != nil {
		return err
	}
	type permit struct{ id, node string }
	var permits []permit
	for rows.Next() {
		var a permit
		if err = rows.Scan(&a.id, &a.node); err != nil {
			break
		}
		permits = append(permits, a)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range permits {
		if _, err = p.observeRotationTx(ctx, tx, a.node, "cancel:"+a.id); err != nil {
			return err
		}
		if err = p.cancelObservedPermitTx(ctx, tx, a.node, a.id); err != nil {
			return err
		}
	}
	return nil
}

func (p *Panel) cancelObservedPermitTx(ctx context.Context, tx *sql.Tx, node, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE rotation_actions SET state='cancelled',reason='观测模式：未投递的自动换 IP 许可作废',updated_at=? WHERE id=? AND permit_sent=0 AND state='authorized'`, p.deps.Clock.Now().UnixNano(), id)
	if err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM rotation_quota WHERE action_id=?", id)
	}
	if err == nil {
		err = p.rotationNotice(ctx, tx, node, id, "cancelled", "观测模式：未投递的自动换 IP 许可作废，已释放额度")
	}
	return err
}

func (p *Panel) cancelObservedDNSTx(ctx context.Context, tx *sql.Tx, a EntryAction) error {
	records, err := loadActionRecords(ctx, tx, a.ID)
	if err != nil {
		return err
	}
	cancelled := false
	started := false
	for _, r := range records {
		started = started || r.writeStarted || r.Stage == EntryWritten
		if r.writeStarted || r.Stage != EntryDesired && r.Stage != EntryUnconfirmed {
			continue
		}
		cancelled = true
		node := a.NodeID
		if err = tx.QueryRowContext(ctx, "SELECT coalesce(node_id,?) FROM managed_records WHERE id=?", node, r.ManagedRecordID).Scan(&node); err != nil {
			return err
		}
		if _, err = p.observeEntryTx(ctx, tx, a.GroupID, node, a.Kind, "cancel:"+a.ID+":"+r.ManagedRecordID, observationDetail(a.Kind, node, r.NewValue)+"；尚未调用外部接口，原动作已作废"); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE entry_action_records SET stage=?,last_error='观测模式：未调用外部接口，动作作废' WHERE action_id=? AND managed_record_id=? AND write_started=0 AND stage IN `+pendingRecordStages, EntrySuperseded, a.ID, r.ManagedRecordID)
		if err != nil {
			return err
		}
		// 自动摘除/加回在提交时写了成员期望，作废后恢复原期望，防止关闭后审计重放。
		if a.Kind == "auto_withdraw" || a.Kind == "auto_restore" {
			_, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_value=?,expected_enabled=?,reason='观测模式：未执行动作已作废',withdrawn_at=CASE WHEN ? THEN NULL ELSE withdrawn_at END WHERE group_id=? AND node_id=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND current_action_id=?)`, r.OldValue, r.OldEnabled, r.OldEnabled, a.GroupID, node, a.GroupID, a.ID)
			if err != nil {
				return err
			}
		}
	}
	if !cancelled {
		return nil
	}
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM entry_action_records WHERE action_id=? AND stage IN "+pendingRecordStages, a.ID).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 || started {
		return nil
	} // 已发出的逐记录动作仍由原协调器核对。
	_, err = tx.ExecContext(ctx, `UPDATE entry_actions SET stage=?,last_error='观测模式：未调用外部接口的记录动作已作废' WHERE id=?`, EntrySuperseded, a.ID)
	if err != nil {
		return err
	}
	if err = p.actionEvent(ctx, tx, a, EntrySuperseded); err != nil {
		return err
	}
	if a.Kind == "switch" || a.Kind == "failback" {
		// 不倒退版本号。把自动动作尚未执行的主备目标退回之前的活动入口。
		_, err = tx.ExecContext(ctx, `UPDATE entry_publications SET active_node_id=coalesce((SELECT node_id FROM entry_actions WHERE group_id=? AND version<? AND written_at IS NOT NULL ORDER BY version DESC LIMIT 1),active_node_id),expected_value=?,written_at=(SELECT written_at FROM entry_actions WHERE group_id=? AND version<? AND written_at IS NOT NULL ORDER BY version DESC LIMIT 1) WHERE group_id=? AND current_action_id=?`, a.GroupID, a.Version, a.OldValue, a.GroupID, a.Version, a.GroupID, a.ID)
		if err != nil {
			return err
		}
		// 被作废的策略③工作流不能授权或阻止新的当前状态判断。
		if _, err = tx.ExecContext(ctx, `DELETE FROM entry_blocked_rotations WHERE decision_id IN (SELECT id FROM entry_blocked_decisions WHERE action_id=?)`, a.ID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM entry_blocked_decisions WHERE action_id=?`, a.ID); err != nil {
			return err
		}
	}
	// 主备偏差写回的未执行动作也须恢复审计资格，不能留下 written_at=NULL。
	if _, err = tx.ExecContext(ctx, `UPDATE entry_publications SET written_at=(SELECT written_at FROM entry_actions WHERE group_id=? AND version<? AND written_at IS NOT NULL ORDER BY version DESC LIMIT 1) WHERE group_id=? AND current_action_id=?`, a.GroupID, a.Version, a.GroupID, a.ID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE entry_groups SET publication_status=? WHERE id=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND current_action_id=?)`, EntrySuperseded, a.GroupID, a.GroupID, a.ID)
	if err != nil {
		return err
	}
	var mode string
	if err = tx.QueryRowContext(ctx, "SELECT publication_mode FROM entry_groups WHERE id=?", a.GroupID).Scan(&mode); err != nil {
		return err
	}
	if mode == "all" {
		return p.refreshAllWatchTx(ctx, tx, a, p.deps.Clock.Now())
	}
	var current bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND current_action_id=?)", a.GroupID, a.ID).Scan(&current); err != nil || !current {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT expected_value FROM entry_publications WHERE group_id=?", a.GroupID).Scan(&a.NewValue); err != nil {
		return err
	}
	if err = p.resetEntryDomainWatch(ctx, tx, a); err != nil {
		return err
	}
	return p.beginEntryPropagation(ctx, tx, a, p.deps.Clock.Now())
}
