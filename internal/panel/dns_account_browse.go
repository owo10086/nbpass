package panel

import (
	"errors"
	"net/http"
	"strings"

	"nbpass/internal/dnsprovider"
)

// browsedRecord 是绑定前供管理员挑选的 A 记录；Reason 非空表示绑定会被拒绝，Owner 标明已有归属。
// 挑选结果不授予任何权限，绑定仍由各绑定接口读回并完整校验。
type browsedRecord struct {
	dnsprovider.Record
	Hostname string `json:"hostname"`
	Reason   string `json:"reason"`
	Owner    string `json:"owner"`
}

func (p *Panel) dnsAccountBrowseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dns-accounts/{id}/domains", p.listDNSAccountDomains)
	mux.HandleFunc("GET /api/dns-accounts/{id}/records", p.listDNSAccountRecords)
}

func (p *Panel) browseCredentials(w http.ResponseWriter, r *http.Request) (dnsprovider.Credentials, bool) {
	credentials, err := p.dnsCredential(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "DNSPod 账号不存在或凭据不可用")
		return dnsprovider.Credentials{}, false
	}
	if p.deps.DNSProvider == nil {
		problem(w, 503, "DNS 服务商不可用")
		return dnsprovider.Credentials{}, false
	}
	return credentials.provider(), true
}

// browseProblem 只透出服务商拒绝码，不回显第三方原始消息。
func browseProblem(w http.ResponseWriter, err error) {
	var rejection *dnsprovider.RejectionError
	switch {
	case !errors.As(err, &rejection):
		problem(w, 502, "DNSPod 读取失败，请稍后重试")
	case rejection.Code == "AuthFailure.SignatureExpire":
		problem(w, 502, "腾讯云签名已过期，请校准面板服务器时间")
	case strings.HasPrefix(rejection.Code, "AuthFailure"):
		problem(w, 502, "腾讯云 API 密钥验证失败，请核对 SecretId 与 SecretKey")
	case strings.HasPrefix(rejection.Code, "UnauthorizedOperation"):
		problem(w, 502, "腾讯云 API 密钥没有 DNSPod 权限，请为该账号授予 DNSPod 访问策略")
	default:
		problem(w, 502, rejection.Error())
	}
}

func (p *Panel) listDNSAccountDomains(w http.ResponseWriter, r *http.Request) {
	credentials, ok := p.browseCredentials(w, r)
	if !ok {
		return
	}
	ctx, cancel := p.dnsContext(r.Context())
	defer cancel()
	domains, err := p.deps.DNSProvider.ListDomains(ctx, credentials)
	if err != nil {
		browseProblem(w, err)
		return
	}
	respond(w, 200, map[string]any{"domains": domains})
}

func (p *Panel) listDNSAccountRecords(w http.ResponseWriter, r *http.Request) {
	domain, err := normalizeDNSDomain(r.URL.Query().Get("domain"))
	if err != nil {
		problem(w, 400, "请选择 DNSPod 主域名")
		return
	}
	credentials, ok := p.browseCredentials(w, r)
	if !ok {
		return
	}
	ctx, cancel := p.dnsContext(r.Context())
	defer cancel()
	all, err := p.deps.DNSProvider.ListRecords(ctx, credentials, domain)
	if err != nil {
		browseProblem(w, err)
		return
	}
	owners, err := p.recordOwners(r, domain)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	records := []browsedRecord{}
	for _, record := range all {
		if record.Type != "A" {
			continue
		}
		b := browsedRecord{Record: record, Hostname: domain, Owner: owners[record.ID]}
		if record.Name != "@" && record.Name != "" {
			b.Hostname = record.Name + "." + domain
		}
		// 与绑定使用同一校验，提前标出非 IPv4、代理及同名多线路记录。
		related := []dnsprovider.Record{}
		for _, other := range all {
			if other.Name == record.Name {
				related = append(related, other)
			}
		}
		if _, err := recordWarnings(dnsprovider.ReadResult{Record: record, Related: related}, record.ID, record.LineID); err != nil {
			b.Reason = err.Error()
		}
		records = append(records, b)
	}
	respond(w, 200, map[string]any{"records": records})
}

// recordOwners 按记录 ID 描述已有归属；同一远端记录全局只能有一个归属。
func (p *Panel) recordOwners(r *http.Request, domain string) (map[string]string, error) {
	rows, err := p.db.QueryContext(r.Context(), `SELECT m.record_id,m.owner_kind,coalesce(g.name,''),coalesce(n.name,'') FROM managed_records m
 LEFT JOIN entry_groups g ON g.id=m.group_id LEFT JOIN nodes n ON n.id=m.node_id
 WHERE m.provider='dnspod' AND m.domain=?`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := map[string]string{}
	for rows.Next() {
		var id, kind, group, node string
		if err := rows.Scan(&id, &kind, &group, &node); err != nil {
			return nil, err
		}
		switch kind {
		case "node":
			owners[id] = "节点 " + node
		case "member":
			owners[id] = "入口组 " + group + " 的成员 " + node
		default:
			owners[id] = "入口组 " + group
		}
	}
	return owners, rows.Err()
}
