package panel

import (
	"context"
	"nbpass/internal/dnsprovider"
	"time"
)

// DNS 解析采样只观察；逐记录的服务商偏差通过统一协调器恢复地址与启用状态。
func (p *Panel) auditAllDNS(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `SELECT g.id,s.desired_version FROM entry_groups g JOIN entry_publications s ON s.group_id=g.id JOIN entry_dns_audits d ON d.group_id=g.id WHERE g.publication_mode='all' AND g.first_published_at IS NOT NULL AND d.next_check_at<=? AND NOT EXISTS(SELECT 1 FROM entry_actions a WHERE a.group_id=g.id AND a.stage IN `+pendingRecordStages+`)`, p.deps.Clock.Now().UnixNano())
	if err != nil {
		return
	}
	type item struct {
		g string
		v int64
	}
	var items []item
	for rows.Next() {
		var i item
		if rows.Scan(&i.g, &i.v) == nil {
			items = append(items, i)
		}
	}
	rows.Close()
	for _, i := range items {
		p.auditAllGroup(ctx, i.g, i.v)
	}
}
func (p *Panel) auditAllGroup(ctx context.Context, g string, v int64) {
	if _, err := p.db.ExecContext(ctx, "UPDATE entry_dns_audits SET next_check_at=? WHERE group_id=?", p.deps.Clock.Now().Add(5*time.Minute).UnixNano(), g); err != nil {
		return
	}
	allowed, err := recoveryGroupAllowed(ctx, p.db, g)
	if err != nil || !allowed {
		return
	}
	members, err := loadAllMembers(ctx, p.db, g)
	if err != nil {
		return
	}
	changes := []ManagedRecordChange{}
	message := ""
	actuals := map[string]dnsprovider.Record{}
	for _, m := range members {
		if m.Record == nil {
			continue
		}
		b := m.Record
		credential, e := p.dnsCredential(ctx, b.AccountID)
		if e != nil || p.deps.DNSProvider == nil {
			message = "DNSPod 凭据不可用"
			break
		}
		query, cancel := p.dnsContext(ctx)
		actual, e := p.deps.DNSProvider.ReadRecord(query, credential.provider(), dnsprovider.Key{Domain: b.Domain, RecordID: b.RecordID})
		cancel()
		if e != nil {
			message = "DNSPod 成员记录核对失败，等待下轮核对"
			break
		}
		if _, e = recordWarnings(actual, b.RecordID, b.LineID); e != nil || actual.Record.Name != b.Name {
			message = "成员受管记录的类型、名称或线路变化，等待人工核对"
			break
		}
		actuals[b.ID] = actual.Record
		if m.LastError == "" && (actual.Record.Enabled != m.ExpectedEnabled || m.ExpectedValue != "" && actual.Record.Value != m.ExpectedValue) {
			changes = append(changes, ManagedRecordChange{ManagedRecordID: b.ID, BindingGeneration: b.BindingGeneration, Value: m.ExpectedValue, Enabled: m.ExpectedEnabled, ExplicitEnable: m.ExpectedEnabled})
		}
	}
	if message != "" {
		_, _ = p.db.ExecContext(ctx, "UPDATE entry_dns_audits SET last_error=? WHERE group_id=?", message, g)
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	var current bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND desired_version=?)", g, v).Scan(&current)
	if err != nil || !current {
		return
	}
	if err = requireNoGroupAction(ctx, tx, g); err != nil {
		return
	}
	snapshotChanged := false
	for _, m := range members {
		if m.Record == nil {
			continue
		}
		r, ok := actuals[m.Record.ID]
		if !ok {
			continue
		}
		snapshotChanged = snapshotChanged || r.Value != m.Record.CurrentValue || r.Enabled != m.Record.CurrentEnabled
		if m.LastError != "" && r.Enabled == m.ExpectedEnabled && (m.ExpectedValue == "" || r.Value == m.ExpectedValue) {
			if _, err = tx.ExecContext(ctx, "UPDATE entry_member_publications SET last_action_error='',reason=CASE WHEN address_pending=1 THEN '地址待确认' WHEN expected_enabled=1 THEN '已发布' ELSE '暂停待加回' END WHERE group_id=? AND node_id=?", g, m.NodeID); err != nil {
				return
			}
		}
		if err = missingResult(tx.ExecContext(ctx, `UPDATE managed_records SET current_value=?,current_ttl=?,current_enabled=? WHERE id=? AND binding_generation=?`, r.Value, r.TTL, r.Enabled, m.Record.ID, m.Record.BindingGeneration)); err != nil {
			return
		}
	}
	var a EntryAction
	if len(changes) > 0 {
		a, err = p.submitMemberActionTx(ctx, tx, g, members[0].NodeID, "writeback", v, changes)
	}
	if err == nil && len(changes) == 0 && snapshotChanged {
		var actionID string
		err = tx.QueryRowContext(ctx, "SELECT current_action_id FROM entry_publications WHERE group_id=?", g).Scan(&actionID)
		if err == nil {
			err = p.refreshAllWatchTx(ctx, tx, EntryAction{GroupID: g, Version: v, ID: actionID}, p.deps.Clock.Now())
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE entry_dns_audits SET last_error='' WHERE group_id=?", g)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		if a.ID != "" {
			p.coordinator.wake(actionWorkerKey(a))
		}
		if snapshotChanged {
			p.wakeDNSWatch()
		}
	}
}
