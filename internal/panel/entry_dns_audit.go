package panel

import (
	"context"
	"time"

	"nbpass/internal/dnsprovider"
)

// 独立核对服务商，慢 DNS API 不占用采样调度或入口队列。
func (p *Panel) startEntryDNSAudit(ctx context.Context) {
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		for ctx.Err() == nil {
			p.finishAllMemberRemovals(ctx)
			_ = p.notifyRecordRejections(ctx)
			_ = p.notifyNodeAddressDrift(ctx)
			p.auditAllDNS(ctx)
			p.auditEntryDNS(ctx)
			timer := p.deps.Clock.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C():
			}
			timer.Stop()
		}
	}()
}

func (p *Panel) auditEntryDNS(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, `SELECT s.group_id,s.desired_version,s.active_node_id,s.expected_value,w.propagation_status
 FROM entry_publications s JOIN entry_dns_audits d ON d.group_id=s.group_id JOIN dns_watches w ON w.entry_group_id=s.group_id
 WHERE EXISTS(SELECT 1 FROM entry_groups g WHERE g.id=s.group_id AND g.publication_mode='primary_backup') AND s.written_at IS NOT NULL AND d.next_check_at<=? ORDER BY d.next_check_at LIMIT 32`, p.deps.Clock.Now().UnixNano())
	if err != nil {
		return
	}
	type audit struct {
		group, node, ip, status string
		version                 int64
	}
	items := []audit{}
	for rows.Next() {
		var a audit
		if err = rows.Scan(&a.group, &a.version, &a.node, &a.ip, &a.status); err != nil {
			break
		}
		items = append(items, a)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return
	}
	for _, a := range items {
		interval := 5 * time.Minute
		if a.status == "传播中" {
			interval = 30 * time.Second
		}
		if _, err = p.db.ExecContext(ctx, "UPDATE entry_dns_audits SET next_check_at=? WHERE group_id=?", p.deps.Clock.Now().Add(interval).UnixNano(), a.group); err != nil {
			continue
		}
		message := "DNSPod 记录核对失败，等待下轮核对"
		allowed, e := recoveryGroupAllowed(ctx, p.db, a.group)
		if e != nil || !allowed {
			continue
		}
		b, e := p.loadManaged(ctx, a.group)
		if e != nil {
			continue
		}
		credential, e := p.dnsCredential(ctx, b.AccountID)
		if e != nil || p.deps.DNSProvider == nil {
			continue
		}
		query, cancel := p.dnsContext(ctx)
		actual, e := p.deps.DNSProvider.ReadRecord(query, credential.provider(), dnsprovider.Key{Domain: b.Domain, RecordID: b.RecordID})
		cancel()
		if e == nil {
			want := a.ip
			if b.Type == "CNAME" {
				want, e = cnameTarget(ctx, p.db, a.node, a.ip, false)
			}
			if e != nil {
				message = "CNAME 组活动入口的节点记录不可用，等待人工核对"
			} else if _, e = recordWarnings(actual, b.RecordID, b.LineID); e != nil || actual.Record.Type != b.Type || actual.Record.Name != b.Name || !actual.Record.Enabled {
				message = "主备受管记录的类型、名称、启用状态或线路变化，等待人工核对"
			} else {
				message = ""
				// 先存最新读回值；动作提交仍按当前版本核对，迟到核对不能改新期望。
				_, e = p.db.ExecContext(ctx, `UPDATE managed_records SET current_value=?,current_ttl=?,current_enabled=? WHERE id=? AND binding_generation=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND desired_version=? AND written_at IS NOT NULL)`, actual.Record.Value, actual.Record.TTL, actual.Record.Enabled, b.ID, b.BindingGeneration, a.group, a.version)
				if e == nil && actual.Record.Value != want {
					_, e = p.SubmitEntryAction(ctx, EntryActionRequest{GroupID: a.group, NodeID: a.node, IPv4: a.ip, Kind: "writeback", ExpectedVersion: a.version})
				}
				if e != nil {
					message = "DNSPod 核对版本已变化或动作暂未提交，等待下轮核对"
				}
			}
		}
		_, _ = p.db.ExecContext(ctx, "UPDATE entry_dns_audits SET last_error=? WHERE group_id=?", message, a.group)
	}
}
