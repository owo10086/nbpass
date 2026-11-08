package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"nbpass/internal/eventlog"
)

const entryManualSwitchMigration = `ALTER TABLE entry_groups ADD COLUMN auto_failback_paused INTEGER NOT NULL DEFAULT 0 CHECK(auto_failback_paused IN (0,1));`

func (p *Panel) entryManualRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/entry-groups/{id}/switch", p.switchEntryGroup)
	mux.HandleFunc("POST /api/entry-groups/{id}/resume-failback", p.resumeEntryFailback)
}

func (p *Panel) switchEntryGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActiveNodeID    string `json:"active_node_id"`
		IPv4            string `json:"ipv4"`
		ConfirmInbound  bool   `json:"confirm_inbound"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.ConfirmInbound || body.ExpectedVersion == nil || *body.ExpectedVersion < 0 {
		problem(w, 400, "请指定期望版本并确认所选入口的公网 IPv4 可直接入站")
		return
	}
	a, err := p.SubmitEntryAction(r.Context(), EntryActionRequest{
		GroupID: r.PathValue("id"), NodeID: body.ActiveNodeID, IPv4: body.IPv4,
		Kind: "manual_switch", ExpectedVersion: *body.ExpectedVersion,
	})
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法持久化入口动作，未启动 DNS 写入")
		}
		return
	}
	respond(w, 202, a)
}

// entryActionPolicy 与动作及期望共用事务。暂停只限制自动回切，故障切换仍可入队。
func entryActionPolicy(ctx context.Context, tx *sql.Tx, req EntryActionRequest) error {
	switch req.Kind {
	case "manual_switch":
		_, err := tx.ExecContext(ctx, `UPDATE entry_groups SET auto_failback_paused=1 WHERE id=? AND ?<>(SELECT node_id FROM entry_group_members WHERE group_id=? ORDER BY position LIMIT 1)`, req.GroupID, req.NodeID, req.GroupID)
		return err
	case "failback":
		var paused bool
		if err := tx.QueryRowContext(ctx, "SELECT auto_failback_paused FROM entry_groups WHERE id=?", req.GroupID).Scan(&paused); err != nil {
			return err
		}
		if paused {
			return &forwardingError{409, "自动回切已暂停，请先手动恢复"}
		}
	}
	return nil
}

func (p *Panel) resumeEntryFailback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		problem(w, 400, "请指定已发布入口组的期望版本")
		return
	}
	if err := p.clearEntryFailbackPause(r.Context(), r.PathValue("id"), *body.ExpectedVersion); err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法恢复自动回切")
		}
		return
	}
	respond(w, 204, nil)
}

func (p *Panel) clearEntryFailbackPause(ctx context.Context, gid string, expectedVersion int64) error {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int64
	var paused bool
	err = tx.QueryRowContext(ctx, `SELECT coalesce(s.desired_version,0),g.auto_failback_paused FROM entry_groups g LEFT JOIN entry_publications s ON s.group_id=g.id WHERE g.id=?`, gid).Scan(&version, &paused)
	if errors.Is(err, sql.ErrNoRows) {
		return &forwardingError{404, "入口组不存在"}
	}
	if err != nil {
		return err
	}
	if version != expectedVersion {
		return &forwardingError{409, "期望版本已变化，请刷新后重新操作"}
	}
	if !paused {
		return nil
	}
	if _, err = tx.ExecContext(ctx, "UPDATE entry_groups SET auto_failback_paused=0 WHERE id=?", gid); err != nil {
		return err
	}
	id, _, err := p.randomCredential()
	if err != nil {
		return err
	}
	now := p.deps.Clock.Now()
	err = insertEvent(ctx, tx, "", "panel", eventlog.Event{ID: id, Type: "entry_failback_resumed", OccurredAt: now, Message: "入口组 " + gid + " 已手动恢复自动回切"}, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}
