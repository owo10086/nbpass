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
	GroupID      string   `json:"group_id"`
	AccountID    string   `json:"account_id"`
	Provider     string   `json:"provider"`
	Domain       string   `json:"domain"`
	RecordID     string   `json:"record_id"`
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	Line         string   `json:"line"`
	LineID       string   `json:"line_id"`
	CurrentValue string   `json:"current_value"`
	CurrentTTL   int      `json:"current_ttl"`
	Warnings     []string `json:"warnings"`
}

const managedColumns = `group_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings`

func scanManaged(row scanner) (managedRecord, error) {
	var b managedRecord
	var warnings string
	err := row.Scan(&b.GroupID, &b.AccountID, &b.Provider, &b.Domain, &b.RecordID, &b.Name, &b.Line, &b.LineID, &b.CurrentValue, &b.CurrentTTL, &warnings)
	if err == nil {
		err = json.Unmarshal([]byte(warnings), &b.Warnings)
	}
	b.Hostname = b.Domain
	if b.Name != "@" && b.Name != "" {
		b.Hostname = b.Name + "." + b.Domain
	}
	return b, err
}
func (p *Panel) loadManaged(ctx context.Context, gid string) (managedRecord, error) {
	return scanManaged(p.db.QueryRowContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE group_id=?", gid))
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
func recordWarnings(result dnsprovider.ReadResult, expectedID, lineID string) ([]string, error) {
	r := result.Record
	if r.ID != expectedID || r.Type != "A" || r.Proxied || !r.Enabled || r.LineID != lineID || r.LineID == "" || r.TTL < 1 {
		return nil, errors.New("仅支持启用的 DNS-only A 记录和已确认的单一线路")
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
		problem(w, 400, "请指定 DNSPod 账号、主域名、记录 ID 和单一线路 ID")
		return
	}
	var exists int
	if err = p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM entry_groups WHERE id=?", r.PathValue("id")).Scan(&exists); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if exists == 0 {
		problem(w, 404, "入口组不存在")
		return
	}
	credentials, err := p.dnsCredential(r.Context(), body.AccountID)
	if err != nil {
		problem(w, 400, "DNSPod 账号未配置或凭据不可用")
		return
	}
	if p.deps.DNSProvider == nil {
		problem(w, 503, "DNS 服务商不可用")
		return
	}
	ctx, cancel := p.dnsContext(r.Context())
	defer cancel()
	result, err := p.deps.DNSProvider.ReadRecord(ctx, dnsprovider.Credentials{TokenID: credentials.TokenID, Token: credentials.Token}, dnsprovider.Key{Domain: domain, RecordID: body.RecordID})
	if err != nil {
		problem(w, 502, "DNSPod 读取失败，请核对账号和记录")
		return
	}
	warnings, err := recordWarnings(result, body.RecordID, body.LineID)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	// 同一记录的归属由数据库唯一约束决定；账号别名不能绕过归属。
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(r.Context(), "SELECT group_id FROM managed_records WHERE provider='dnspod' AND domain=? AND record_id=?", domain, body.RecordID).Scan(&owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "存储暂不可用")
		return
	}
	if owner != "" && owner != r.PathValue("id") {
		problem(w, 409, "该 DNSPod 记录已归属其他入口组")
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
	data, _ := json.Marshal(warnings)
	_, err = tx.ExecContext(r.Context(), `INSERT INTO managed_records(`+managedColumns+`) VALUES(?,?,'dnspod',?,?,?,?,?,?,?,?) ON CONFLICT(group_id) DO UPDATE SET account_id=excluded.account_id,domain=excluded.domain,record_id=excluded.record_id,name=excluded.name,line=excluded.line,line_id=excluded.line_id,current_value=excluded.current_value,current_ttl=excluded.current_ttl,warnings=excluded.warnings`, r.PathValue("id"), body.AccountID, domain, body.RecordID, result.Record.Name, result.Record.Line, result.Record.LineID, result.Record.Value, result.Record.TTL, string(data))
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
	respond(w, 200, b)
}
