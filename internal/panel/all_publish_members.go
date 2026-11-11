package panel

import (
	"context"
	"database/sql"
	"net/http"
)

func protectAllMemberRemoval(ctx context.Context, tx *sql.Tx, g string, members []string) error {
	rows, err := tx.QueryContext(ctx, "SELECT node_id FROM managed_records WHERE group_id=? AND owner_kind='member'", g)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return err
		}
		if !containsEntryMember(members, n) {
			return &forwardingError{409, "请先使用成员移出操作暂停记录，确认后再解绑"}
		}
	}
	return rows.Err()
}
func (p *Panel) removeAllMember(w http.ResponseWriter, r *http.Request) {
	if !p.requireGroupMode(w, r, "all") {
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	ctx := r.Context()
	g, n := r.PathValue("id"), r.PathValue("node")
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	members, err := loadAllMembers(ctx, tx, g)
	var target *managedRecord
	for _, m := range members {
		if m.NodeID == n {
			target = m.Record
		}
	}
	if len(members) < 2 {
		problem(w, 409, "入口组须至少保留一个成员")
		return
	}
	if target == nil {
		problem(w, 409, "请先绑定成员记录，或在编辑中移出未绑定成员")
		return
	}
	if err == nil {
		err = requireNoGroupAction(ctx, tx, g)
	}
	var remaining int
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_records WHERE owner_kind='member' AND group_id=? AND node_id<>? AND current_enabled=1", g, n).Scan(&remaining)
	}
	if err == nil && target.CurrentEnabled && remaining < 1 {
		err = &forwardingError{409, "唯一启用记录不能移出，请先启用其他成员"}
	}
	var version int64
	var a EntryAction
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT coalesce((SELECT desired_version FROM entry_publications WHERE group_id=?),0)", g).Scan(&version)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_enabled=0,reason='成员移出中' WHERE group_id=? AND node_id=?`, g, n)
	}
	if err == nil {
		a, err = p.submitMemberActionTx(ctx, tx, g, n, "member_remove", version, []ManagedRecordChange{{ManagedRecordID: target.ID, BindingGeneration: target.BindingGeneration, Enabled: false}})
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE entry_member_publications SET removal_action_id=? WHERE group_id=? AND node_id=?", a.ID, g, n)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法提交成员移出")
		}
		return
	}
	p.coordinator.wake(actionWorkerKey(a))
	respond(w, 202, a)
}

// 状态确认后的本地收尾可重入；即使在服务商确认后重启也会续做。
func (p *Panel) finishAllMemberRemovals(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `SELECT s.group_id,s.node_id,s.removal_action_id FROM entry_member_publications s JOIN entry_actions a ON a.id=s.removal_action_id WHERE a.stage=?`, EntryWritten)
	if err != nil {
		return
	}
	type item struct{ g, n, a string }
	var items []item
	for rows.Next() {
		var i item
		if rows.Scan(&i.g, &i.n, &i.a) == nil {
			items = append(items, i)
		}
	}
	rows.Close()
	for _, i := range items {
		p.entryMu.Lock()
		_ = p.commitForwarding(ctx, func(tx *sql.Tx) error {
			var id string
			err := tx.QueryRowContext(ctx, `SELECT m.id FROM managed_records m JOIN entry_member_publications s ON s.group_id=m.group_id AND s.node_id=m.node_id JOIN entry_action_records r ON r.action_id=s.removal_action_id AND r.managed_record_id=m.id AND r.binding_generation=m.binding_generation WHERE s.group_id=? AND s.node_id=? AND s.removal_action_id=? AND r.stage=? AND m.current_enabled=0`, i.g, i.n, i.a, EntryWritten).Scan(&id)
			if err != nil {
				return err
			}
			if _, err = unbindRecordTx(ctx, tx, id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM entry_group_members WHERE group_id=? AND node_id=?", i.g, i.n); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "DELETE FROM entry_member_publications WHERE group_id=? AND node_id=?", i.g, i.n)
			return err
		})
		p.entryMu.Unlock()
	}
}
