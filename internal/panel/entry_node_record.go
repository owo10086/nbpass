package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

func (p *Panel) entryNodeRecordRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{id}/managed-record", p.getNodeManagedRecord)
	mux.HandleFunc("PUT /api/nodes/{id}/managed-record", p.bindNodeManagedRecord)
	mux.HandleFunc("DELETE /api/nodes/{id}/managed-record", p.unbindNodeManagedRecord)
	mux.HandleFunc("GET /api/nodes/{id}/publication", p.getNodePublication)
	mux.HandleFunc("POST /api/nodes/{id}/publish", p.publishNodeRecord)
	mux.HandleFunc("POST /api/nodes/{id}/sync-address", p.syncNodeAddress)
	mux.HandleFunc("GET /api/nodes/{id}/dns-watch", p.getNodeDomainWatch)
}

func (p *Panel) nodeRecordError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点或受管记录不存在")
		return
	}
	if !writeConfigurationError(w, err) {
		problem(w, 503, "节点受管记录暂不可用")
	}
}

func (p *Panel) getNodeManagedRecord(w http.ResponseWriter, r *http.Request) {
	b, err := scanManaged(p.db.QueryRowContext(r.Context(), "SELECT "+managedColumns+" FROM managed_records WHERE owner_kind='node' AND node_id=?", r.PathValue("id")))
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	respond(w, 200, b)
}

func (p *Panel) bindNodeManagedRecord(w http.ResponseWriter, r *http.Request) {
	var body recordTarget
	if !decode(w, r, &body) {
		return
	}
	var exists bool
	if err := p.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM nodes WHERE id=?)", r.PathValue("id")).Scan(&exists); err != nil {
		p.nodeRecordError(w, err)
		return
	}
	if !exists {
		problem(w, 404, "节点不存在")
		return
	}
	// 在读取服务商前优先返回在途保护；绑定事务仍会再次校验，避免检查与提交之间的竞态。
	var pending bool
	err := p.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM managed_records m JOIN entry_action_records a ON a.managed_record_id=m.id WHERE m.owner_kind='node' AND m.node_id=? AND a.stage IN `+pendingRecordStages+`) OR EXISTS(SELECT 1 FROM node_address_sync_jobs j JOIN managed_records m ON m.id=j.managed_record_id WHERE m.owner_kind='node' AND m.node_id=?)`, r.PathValue("id"), r.PathValue("id")).Scan(&pending)
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	if pending {
		problem(w, 409, "该受管记录有未完成动作，不能更换")
		return
	}
	resolved, ok := p.resolveRecordTarget(w, r, body, recordTargetRules{initial: p.nodeInitialIPv4(r.PathValue("id"))})
	if !ok {
		return
	}
	domain, result, warnings := resolved.domain, resolved.result, resolved.warnings
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	defer tx.Rollback()
	var nodeID string
	err = tx.QueryRowContext(r.Context(), "SELECT id FROM nodes WHERE id=?", r.PathValue("id")).Scan(&nodeID)
	var b managedRecord
	if err == nil {
		b, err = p.bindRecordTx(r.Context(), tx, managedRecord{OwnerKind: "node", NodeID: nodeID, AccountID: body.AccountID, Provider: "dnspod", Domain: domain, RecordID: result.Record.ID, Name: result.Record.Name, Line: result.Record.Line, LineID: result.Record.LineID, CurrentValue: result.Record.Value, CurrentTTL: result.Record.TTL, CurrentEnabled: result.Record.Enabled, Warnings: warnings})
	}
	// 换绑保留单调版本，但必须由管理员重新发布，不能沿用旧记录的完成状态。
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO node_publications(node_id) VALUES(?) ON CONFLICT(node_id) DO UPDATE SET desired_version=node_publications.desired_version+1,current_action_id='',written_at=NULL,published=0`, nodeID)
	}
	if err == nil {
		err = p.bindNodeDomainWatchTx(r.Context(), tx, b)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	p.wakeDNSWatch()
	b.Created = resolved.created
	respond(w, 200, b)
}

func (p *Panel) unbindNodeManagedRecord(w http.ResponseWriter, r *http.Request) {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	defer tx.Rollback()
	var id, warning string
	err = tx.QueryRowContext(r.Context(), "SELECT id FROM managed_records WHERE owner_kind='node' AND node_id=?", r.PathValue("id")).Scan(&id)
	if err == nil {
		warning, err = unbindRecordTx(r.Context(), tx, id)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "DELETE FROM dns_watches WHERE owner_node_id=?", r.PathValue("id"))
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE node_publications SET desired_version=desired_version+1,current_action_id='',written_at=NULL,published=0 WHERE node_id=?", r.PathValue("id"))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	p.wakeDNSWatch()
	respond(w, 200, map[string]any{"warnings": []string{warning}})
}

func (p *Panel) getNodePublication(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	var version int64
	var action, address, reported string
	var written sql.NullInt64
	var published bool
	// 同一只读事务保证发布标记、动作阶段与逐条记录来自同一快照。
	tx, err := p.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(r.Context(), `SELECT coalesce(p.desired_version,0),coalesce(p.current_action_id,''),p.written_at,coalesce(p.published,0),coalesce(r.confirmed_ipv4,''),coalesce(r.public_ipv4,'') FROM nodes n LEFT JOIN node_publications p ON p.node_id=n.id LEFT JOIN node_regions r ON r.node_id=n.id WHERE n.id=?`, r.PathValue("id")).Scan(&version, &action, &written, &published, &address, &reported)
	var a *EntryAction
	if err == nil && action != "" {
		var loaded EntryAction
		loaded, err = scanAction(tx.QueryRowContext(r.Context(), "SELECT "+actionColumns+" FROM entry_actions WHERE id=?", action))
		if err == nil {
			loaded.Records, err = loadActionRecords(r.Context(), tx, action)
			a = &loaded
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	respond(w, 200, map[string]any{"desired_version": version, "written_at": nullableTime(written), "published": published, "expected_value": address, "reported_ipv4": reported, "address_mismatch": reported != "" && reported != address, "action": a})
}

func (p *Panel) publishNodeRecord(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion *int64 `json:"expected_version"`
		ConfirmInbound  bool   `json:"confirm_inbound"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 0 || !body.ConfirmInbound {
		problem(w, 400, "请指定期望版本并确认地址可入站")
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	defer tx.Rollback()
	var address string
	var published bool
	err = tx.QueryRowContext(r.Context(), `SELECT coalesce(r.confirmed_ipv4,''),coalesce(p.published,0) FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id LEFT JOIN node_publications p ON p.node_id=n.id WHERE n.id=?`, r.PathValue("id")).Scan(&address, &published)
	if err == nil && (address == "" || published) {
		err = &forwardingError{409, "请先同步确认节点地址；已首次发布的记录无需再次发布"}
	}
	var b managedRecord
	if err == nil {
		b, err = scanManaged(tx.QueryRowContext(r.Context(), "SELECT "+managedColumns+" FROM managed_records WHERE owner_kind='node' AND node_id=?", r.PathValue("id")))
	}
	if err == nil {
		err = protectRecordBinding(r.Context(), tx, b.ID)
	}
	var a EntryAction
	if err == nil {
		a, err = p.submitRecordActionTx(r.Context(), tx, RecordActionRequest{OwnerNodeID: r.PathValue("id"), NodeID: r.PathValue("id"), Kind: "first_publish", ExpectedVersion: *body.ExpectedVersion, Records: []ManagedRecordChange{{ManagedRecordID: b.ID, BindingGeneration: b.BindingGeneration, Value: address, Enabled: true, ExplicitEnable: true}}})
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	p.coordinator.wake(actionWorkerKey(a))
	respond(w, 202, a)
}

// 列表一次读取全部节点绑定，避免页面逐节点请求；动作阶段与首次发布状态分开展示。
func (p *Panel) nodeRecordViews(ctx context.Context, nodes []nodeView) error {
	byID := map[string]*nodeView{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	rows, err := p.db.QueryContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE owner_kind='node'")
	if err != nil {
		return err
	}
	for rows.Next() {
		b, e := scanManaged(rows)
		if e != nil {
			err = e
			break
		}
		if n := byID[b.NodeID]; n != nil {
			n.ManagedRecord = &b
			n.RecordStatus = "未发布"
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = p.db.QueryContext(ctx, `SELECT p.node_id,p.published,coalesce(a.stage,'') FROM node_publications p LEFT JOIN entry_actions a ON a.id=p.current_action_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, stage string
		var published bool
		if err = rows.Scan(&id, &published, &stage); err != nil {
			return err
		}
		if n := byID[id]; n != nil && n.ManagedRecord != nil {
			n.RecordStatus = stage
			if !published {
				n.RecordStatus = "尚未首次发布"
				if stage != "" {
					n.RecordStatus += " · " + stage
				}
			}
		}
	}
	return rows.Err()
}
