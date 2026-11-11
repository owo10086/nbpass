package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"

	"golang.org/x/net/idna"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/forwarding"
)

type managedRecord struct {
	ID                string   `json:"id"`
	OwnerKind         string   `json:"owner_kind"`
	NodeID            string   `json:"node_id,omitempty"`
	BindingGeneration int64    `json:"binding_generation"`
	CurrentEnabled    bool     `json:"current_enabled"`
	GroupID           string   `json:"group_id"`
	AccountID         string   `json:"account_id"`
	Provider          string   `json:"provider"`
	Domain            string   `json:"domain"`
	RecordID          string   `json:"record_id"`
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	Hostname          string   `json:"hostname"`
	Line              string   `json:"line"`
	LineID            string   `json:"line_id"`
	CurrentValue      string   `json:"current_value"`
	CurrentTTL        int      `json:"current_ttl"`
	Warnings          []string `json:"warnings"`
	// Created 仅在本次绑定由面板新建记录时出现在应答中，不落库。
	Created bool `json:"created,omitempty"`
}

const managedColumns = `coalesce(group_id,''),account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings,id,owner_kind,coalesce(node_id,''),binding_generation,current_enabled,record_type`

func scanManaged(row scanner) (managedRecord, error) {
	var b managedRecord
	var warnings string
	err := row.Scan(&b.GroupID, &b.AccountID, &b.Provider, &b.Domain, &b.RecordID, &b.Name, &b.Line, &b.LineID, &b.CurrentValue, &b.CurrentTTL, &warnings, &b.ID, &b.OwnerKind, &b.NodeID, &b.BindingGeneration, &b.CurrentEnabled, &b.Type)
	if err == nil {
		err = json.Unmarshal([]byte(warnings), &b.Warnings)
	}
	b.Hostname = recordHostname(b.Name, b.Domain)
	return b, err
}
func (p *Panel) loadManaged(ctx context.Context, gid string) (managedRecord, error) {
	return scanManaged(p.db.QueryRowContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE group_id=? AND owner_kind='group'", gid))
}
func (p *Panel) getManagedRecord(w http.ResponseWriter, r *http.Request) {
	b, err := p.loadManaged(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "入口组未绑定受管记录")
		return
	}
	if err != nil {
		problem(w, 503, "无法读取受管记录")
		return
	}
	respond(w, 200, b)
}
// recordWarnings 校验记录可受管：A 记录须是 IPv4，CNAME 须是合法域名且同名同线路没有其他记录。
// 记录类型是否与绑定一致由调用方另行比较。
func recordWarnings(result dnsprovider.ReadResult, expectedID, lineID string) ([]string, error) {
	r := result.Record
	if r.ID != expectedID || (r.Type != "A" && r.Type != "CNAME") || r.Proxied || r.LineID != lineID || r.LineID == "" || r.TTL < 1 {
		return nil, errors.New("仅支持 DNS-only A/CNAME 记录和已确认的单一线路")
	}
	if r.Type == "CNAME" {
		if err := forwarding.ValidateTarget(strings.TrimSuffix(r.Value, "."), false); err != nil {
			return nil, errors.New("CNAME 记录值不是有效域名")
		}
		for _, other := range result.Related {
			if other.Name == r.Name && other.ID != r.ID && other.LineID == r.LineID {
				return nil, errors.New("CNAME 记录与同名同线路的其他记录（" + other.Type + " " + other.ID + "）共存，请先在 DNSPod 处理")
			}
		}
		return []string{}, nil
	}
	if ip, err := netip.ParseAddr(r.Value); err != nil || !ip.Is4() {
		return nil, errors.New("受管记录当前值不是 IPv4")
	}
	warnings := []string{}
	for _, other := range result.Related {
		if other.Name != r.Name || other.ID == r.ID || (other.Type != "A" && other.Type != "AAAA") {
			continue
		}
		if other.LineID != r.LineID {
			return nil, errors.New("同名 A/AAAA 存在多线路，暂不支持自动管理")
		}
		warnings = append(warnings, "同名其他 "+other.Type+" 记录（"+other.ID+"）可能分散流量或使客户端使用 IPv6；面板不会修改或删除它")
	}
	return warnings, nil
}
func normalizeDNSDomain(value string) (string, error) {
	domain, err := idna.Lookup.ToASCII(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), ".")))
	if err != nil {
		return "", errors.New("域名无效")
	}
	if _, err := netip.ParseAddr(domain); err == nil {
		return "", errors.New("请填写 DNSPod 主域名")
	}
	if err := forwarding.ValidateTarget(domain, false); err != nil {
		return "", err
	}
	return domain, nil
}
func (p *Panel) bindManagedRecord(w http.ResponseWriter, r *http.Request) {
	var body recordTarget
	if !decode(w, r, &body) {
		return
	}
	if !p.requireGroupMode(w, r, "primary_backup") {
		return
	}
	// 新建或改类型前先拒绝已发布的组，避免在服务商留下用不上的记录；事务内仍会再次校验。
	var started bool
	if err := p.db.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND desired_version>0)", r.PathValue("id")).Scan(&started); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if started {
		problem(w, 409, "已开始发布的入口组不能重新绑定受管记录")
		return
	}
	resolved, ok := p.resolveRecordTarget(w, r, body, recordTargetRules{initial: p.preferredInitialIPv4(r.PathValue("id")), cname: p.preferredCNAMETarget(r.PathValue("id"))})
	if !ok {
		return
	}
	domain, result, warnings := resolved.domain, resolved.result, resolved.warnings
	// 同一记录的归属由数据库唯一约束决定；账号别名不能绕过归属。
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	var owner, ownerKind string
	err = tx.QueryRowContext(r.Context(), "SELECT coalesce(group_id,''),owner_kind FROM managed_records WHERE provider='dnspod' AND domain=? AND record_id=?", domain, result.Record.ID).Scan(&owner, &ownerKind)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "存储暂不可用")
		return
	}
	if ownerKind != "" && (ownerKind != "group" || owner != r.PathValue("id")) {
		problem(w, 409, "该 DNSPod 记录已有归属")
		return
	}
	var version int
	err = tx.QueryRowContext(r.Context(), "SELECT desired_version FROM entry_publications WHERE group_id=?", r.PathValue("id")).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "存储暂不可用")
		return
	}
	if version > 0 {
		problem(w, 409, "已开始发布的入口组不能重新绑定受管记录")
		return
	}
	_, err = p.bindRecordTx(r.Context(), tx, managedRecord{OwnerKind: "group", GroupID: r.PathValue("id"), AccountID: body.AccountID, Provider: "dnspod", Domain: domain, RecordID: result.Record.ID, Name: result.Record.Name, Type: result.Record.Type, Line: result.Record.Line, LineID: result.Record.LineID, CurrentValue: result.Record.Value, CurrentTTL: result.Record.TTL, CurrentEnabled: result.Record.Enabled, Warnings: warnings})
	if err == nil {
		err = p.bindEntryDomainWatch(r.Context(), tx, r.PathValue("id"))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if writeConfigurationError(w, err) {
			return
		}
		problem(w, 503, "无法保存受管记录")
		return
	}
	p.wakeDNSWatch()
	b, err := p.loadManaged(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 503, "无法读取受管记录")
		return
	}
	b.Created = resolved.created
	respond(w, 200, b)
}
