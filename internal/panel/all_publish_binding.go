package panel

import (
	"database/sql"
	"errors"
	"nbpass/internal/dnsprovider"
	"net/http"
)

func (p *Panel) requireGroupMode(w http.ResponseWriter, r *http.Request, mode string) bool {
	var actual string
	err := p.db.QueryRowContext(r.Context(), "SELECT publication_mode FROM entry_groups WHERE id=?", r.PathValue("id")).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "入口组不存在")
		return false
	}
	if err != nil {
		problem(w, 503, "无法读取发布模式")
		return false
	}
	if actual != mode {
		problem(w, 409, "该操作不适用于此发布模式")
		return false
	}
	return true
}
func (p *Panel) allPublishRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/entry-groups/{id}/minimum-kept", p.setAllMinimumKept)
	mux.HandleFunc("POST /api/entry-groups/{id}/members/{node}/withdraw", p.manualAllMemberAction)
	mux.HandleFunc("POST /api/entry-groups/{id}/members/{node}/restore", p.manualAllMemberAction)
	mux.HandleFunc("DELETE /api/entry-groups/{id}/members/{node}", p.removeAllMember)
	mux.HandleFunc("PUT /api/entry-groups/{id}/members/{node}/managed-record", p.bindMemberRecord)
	mux.HandleFunc("GET /api/entry-groups/{id}/members/{node}/managed-record", p.getMemberRecord)
}
func (p *Panel) getMemberRecord(w http.ResponseWriter, r *http.Request) {
	b, err := scanManaged(p.db.QueryRowContext(r.Context(), "SELECT "+managedColumns+" FROM managed_records WHERE owner_kind='member' AND group_id=? AND node_id=?", r.PathValue("id"), r.PathValue("node")))
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "成员未绑定记录")
		return
	}
	if err != nil {
		problem(w, 503, "无法读取成员记录")
		return
	}
	respond(w, 200, b)
}
func (p *Panel) bindMemberRecord(w http.ResponseWriter, r *http.Request) {
	if !p.requireGroupMode(w, r, "all") {
		return
	}
	var body struct {
		AccountID string `json:"account_id"`
		Domain    string `json:"domain"`
		RecordID  string `json:"record_id"`
		LineID    string `json:"line_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	domain, err := normalizeDNSDomain(body.Domain)
	if err != nil || body.AccountID == "" || body.RecordID == "" || len(body.RecordID) > 100 || body.LineID == "" || len(body.LineID) > 100 {
		problem(w, 400, "请指定 DNSPod 账号、主域名、记录 ID 和线路 ID")
		return
	}
	credential, err := p.dnsCredential(r.Context(), body.AccountID)
	if err != nil {
		problem(w, 400, "DNSPod 账号不可用")
		return
	}
	if p.deps.DNSProvider == nil {
		problem(w, 503, "DNS 服务商不可用")
		return
	}
	ctx, cancel := p.dnsContext(r.Context())
	defer cancel()
	actual, err := p.deps.DNSProvider.ReadRecord(ctx, credential.provider(), dnsprovider.Key{Domain: domain, RecordID: body.RecordID})
	if err != nil {
		problem(w, 502, "DNSPod 读取失败")
		return
	}
	warnings, err := recordWarnings(actual, body.RecordID, body.LineID)
	if err != nil {
		problem(w, 400, err.Error())
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
	if err = requireNoGroupAction(r.Context(), tx, r.PathValue("id")); err != nil {
		writeConfigurationError(w, err)
		return
	}
	var published bool
	var version int64
	err = tx.QueryRowContext(r.Context(), `SELECT g.first_published_at IS NOT NULL,coalesce(s.desired_version,0) FROM entry_groups g LEFT JOIN entry_publications s ON s.group_id=g.id WHERE g.id=?`, r.PathValue("id")).Scan(&published, &version)
	if err != nil {
		problem(w, 503, "无法读取组状态")
		return
	}
	recoveryAllowed, err := recoveryGroupAllowed(r.Context(), tx, r.PathValue("id"))
	if err != nil {
		problem(w, 503, "无法核对恢复保护")
		return
	}
	if published && !recoveryAllowed {
		problem(w, 409, "请先完成入口组恢复核对，再更换已发布成员记录")
		return
	}
	if published {
		var enabled, remaining int
		if err = tx.QueryRowContext(r.Context(), `SELECT coalesce(sum(node_id=? AND current_enabled=1),0),coalesce(sum(node_id<>? AND current_enabled=1),0) FROM managed_records WHERE owner_kind='member' AND group_id=?`, r.PathValue("node"), r.PathValue("node"), r.PathValue("id")).Scan(&enabled, &remaining); err != nil {
			problem(w, 503, "无法读取成员状态")
			return
		}
		if enabled > 0 && remaining == 0 {
			problem(w, 409, "唯一启用记录不能更换，请先启用其他成员")
			return
		}
	}

	var otherDomain, otherName, otherLine string
	err = tx.QueryRowContext(r.Context(), "SELECT domain,name,line_id FROM managed_records WHERE group_id=? AND owner_kind='member' LIMIT 1", r.PathValue("id")).Scan(&otherDomain, &otherName, &otherLine)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "无法核对组域名")
		return
	}
	if err == nil && (otherDomain != domain || otherName != actual.Record.Name || otherLine != actual.Record.LineID) {
		problem(w, 400, "成员记录须与组域名同名、同线路")
		return
	}
	b, err := p.bindRecordTx(r.Context(), tx, managedRecord{OwnerKind: "member", GroupID: r.PathValue("id"), NodeID: r.PathValue("node"), AccountID: body.AccountID, Provider: "dnspod", Domain: domain, RecordID: body.RecordID, Name: actual.Record.Name, Line: actual.Record.Line, LineID: actual.Record.LineID, CurrentValue: actual.Record.Value, CurrentTTL: actual.Record.TTL, CurrentEnabled: actual.Record.Enabled, Warnings: warnings})
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO entry_member_publications(group_id,node_id) VALUES(?,?)", b.GroupID, b.NodeID)
	}
	if err == nil {
		err = p.bindEntryDomainWatch(r.Context(), tx, b.GroupID)
	}
	var a EntryAction
	if err == nil && published && recoveryAllowed {
		// 本次手动绑定已重新读回新身份、地址和启用状态，并立即以暂停为新期望。
		// 只继承事务开始时已核对组的放行，不能绕过尚未完成的旧备份恢复。
		_, err = tx.ExecContext(r.Context(), `UPDATE recovery_records SET binding_generation=?,verified=1,last_error='' WHERE managed_record_id=?`, b.BindingGeneration, b.ID)
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "UPDATE recovery_groups SET verified=1,last_error='' WHERE group_id=?", b.GroupID)
		}
	}
	if err == nil && published {
		_, err = tx.ExecContext(r.Context(), "UPDATE entry_member_publications SET reason='暂停待加回',expected_value='',expected_enabled=0,address_pending=1,last_action_error='' WHERE group_id=? AND node_id=?", b.GroupID, b.NodeID)
		if err == nil {
			a, err = p.submitMemberActionTx(r.Context(), tx, b.GroupID, b.NodeID, "member_bind", version, []ManagedRecordChange{{ManagedRecordID: b.ID, BindingGeneration: b.BindingGeneration, Enabled: false}})
		}
	}
	if err == nil {
		err = tx.Commit()
	}

	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法保存成员记录")
		}
		return
	}
	if a.ID != "" {
		p.coordinator.wake(actionWorkerKey(a))
	}
	p.wakeDNSWatch()
	respond(w, 200, b)
}
