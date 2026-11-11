package panel

import (
	"net/http"
	"strings"
)

func (p *Panel) setAllMinimumKept(w http.ResponseWriter, r *http.Request) {
	if !p.requireGroupMode(w, r, "all") {
		return
	}
	var body struct {
		MinimumKept int `json:"minimum_kept"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.MinimumKept < 1 {
		problem(w, 400, "最少保留数须为大于或等于 1 的整数")
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	if err = requireNoGroupAction(r.Context(), tx, r.PathValue("id")); err == nil {
		err = missingResult(tx.ExecContext(r.Context(), "UPDATE entry_groups SET minimum_kept=? WHERE id=?", body.MinimumKept, r.PathValue("id")))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法保存最少保留数")
		}
		return
	}
	p.wakeOffline()
	respond(w, 200, map[string]any{"minimum_kept": body.MinimumKept})
}

// 手动操作只受最后一条确认启用记录保护，不受自动最少保留数限制。
func (p *Panel) manualAllMemberAction(w http.ResponseWriter, r *http.Request) {
	if !p.requireGroupMode(w, r, "all") {
		return
	}
	var body struct {
		Version *int64 `json:"expected_version"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Version == nil {
		problem(w, 400, "请提供当前期望版本")
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	ctx, g, n := r.Context(), r.PathValue("id"), r.PathValue("node")
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	var published bool
	if err = tx.QueryRowContext(ctx, "SELECT first_published_at IS NOT NULL FROM entry_groups WHERE id=?", g).Scan(&published); err == nil && !published {
		err = &forwardingError{409, "请先首次发布入口组"}
	}
	if err == nil {
		err = requireNoGroupAction(ctx, tx, g)
	}
	var members []allMemberPublication
	if err == nil {
		members, err = loadAllMembers(ctx, tx, g)
	}
	var target *allMemberPublication
	for i := range members {
		if members[i].NodeID == n {
			target = &members[i]
		}
	}
	if err == nil && (target == nil || target.Record == nil) {
		err = &forwardingError{404, "成员未绑定受管记录"}
	}
	enabled := strings.HasSuffix(r.URL.Path, "/restore")
	if err == nil && !enabled && target.Record.CurrentEnabled {
		var remaining int
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM managed_records WHERE owner_kind='member' AND group_id=? AND node_id<>? AND current_enabled=1", g, n).Scan(&remaining)
		if err == nil && remaining < 1 {
			err = &forwardingError{409, "不能手动摘除最后一条启用记录"}
		}
	}
	kind, reason := "manual_withdraw", "手动摘除"
	if enabled {
		kind, reason = "manual_restore", "手动加回"
	}
	var a EntryAction
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_enabled=?,manual_locked=?,reason=?,withdrawn_at=CASE WHEN ? THEN NULL ELSE ? END WHERE group_id=? AND node_id=?`, enabled, !enabled, reason, enabled, p.deps.Clock.Now().UnixNano(), g, n)
	}
	if err == nil {
		b := target.Record
		a, err = p.submitMemberActionTx(ctx, tx, g, n, kind, *body.Version, []ManagedRecordChange{{ManagedRecordID: b.ID, BindingGeneration: b.BindingGeneration, Value: target.ExpectedValue, Enabled: enabled, ExplicitEnable: enabled}})
	}
	if err == nil {
		err = p.memberActionDecisionTx(ctx, tx, g, *target, a, reason)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法保存成员操作")
		}
		return
	}
	p.coordinator.wake(actionWorkerKey(a))
	respond(w, 202, a)
}
