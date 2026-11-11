package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

const managedDNSMigration = `CREATE TABLE dns_accounts (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 provider TEXT NOT NULL CHECK(provider = 'dnspod')
);
CREATE TABLE managed_records (
 group_id TEXT PRIMARY KEY REFERENCES entry_groups(id) ON DELETE CASCADE,
 account_id TEXT NOT NULL REFERENCES dns_accounts(id),
 provider TEXT NOT NULL,
 domain TEXT NOT NULL,
 record_id TEXT NOT NULL,
 name TEXT NOT NULL,
 line TEXT NOT NULL,
 line_id TEXT NOT NULL,
 current_value TEXT NOT NULL,
 current_ttl INTEGER NOT NULL,
 warnings TEXT NOT NULL,
 UNIQUE(provider,domain,record_id)
);
CREATE TABLE entry_publications (
 group_id TEXT PRIMARY KEY REFERENCES entry_groups(id) ON DELETE CASCADE,
 desired_version INTEGER NOT NULL DEFAULT 0,
 active_node_id TEXT NOT NULL DEFAULT '',
 expected_value TEXT NOT NULL DEFAULT '',
 current_action_id TEXT NOT NULL DEFAULT '',
 written_at INTEGER
);
CREATE TABLE entry_actions (
 id TEXT PRIMARY KEY,
 group_id TEXT NOT NULL REFERENCES entry_groups(id) ON DELETE CASCADE,
 version INTEGER NOT NULL,
 kind TEXT NOT NULL,
 node_id TEXT NOT NULL REFERENCES nodes(id),
 old_value TEXT NOT NULL,
 new_value TEXT NOT NULL,
 old_ttl INTEGER NOT NULL,
 stage TEXT NOT NULL,
 last_error TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 written_at INTEGER,
 write_started INTEGER NOT NULL DEFAULT 0,
 UNIQUE(group_id,version)
);`

type dnsAccount struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Configured bool   `json:"configured"`
}

type dnsCredentials struct {
	TokenID string `json:"token_id"`
	Token   string `json:"token"`
}

func (p *Panel) managedDNSRoutes(mux *http.ServeMux) {
	p.entryNodeRecordRoutes(mux)
	p.allPublishRoutes(mux)
	mux.HandleFunc("GET /api/entry-groups/{id}/dns-watch", p.getEntryDomainWatch)
	p.entryManualRoutes(mux)
	mux.HandleFunc("POST /api/entry-groups/{id}/publish", p.publishEntryGroup)
	mux.HandleFunc("GET /api/entry-groups/{id}/publication", p.getEntryPublication)
	mux.HandleFunc("GET /api/entry-groups/{id}/actions", p.listEntryActions)
	mux.HandleFunc("GET /api/dns-accounts", p.listDNSAccounts)
	mux.HandleFunc("GET /api/entry-groups/{id}/managed-record", p.getManagedRecord)
	mux.HandleFunc("PUT /api/entry-groups/{id}/managed-record", p.bindManagedRecord)
	mux.HandleFunc("POST /api/dns-accounts", p.addDNSAccount)
	p.dnsAccountBrowseRoutes(mux)
}

func (p *Panel) listDNSAccounts(w http.ResponseWriter, r *http.Request) {
	rows, err := p.db.QueryContext(r.Context(), "SELECT id,name,provider FROM dns_accounts ORDER BY name,id")
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer rows.Close()
	accounts := []dnsAccount{}
	for rows.Next() {
		var a dnsAccount
		if err := rows.Scan(&a.ID, &a.Name, &a.Provider); err != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
		a.Configured = true
		accounts = append(accounts, a)
	}
	if rows.Err() != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"accounts": accounts})
}

func (p *Panel) addDNSAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		TokenID string `json:"token_id"`
		Token   string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 200 || body.TokenID == "" || len(body.TokenID) > 100 || body.Token == "" || len(body.Token) > 4096 || strings.ContainsAny(body.TokenID+body.Token, ",\r\n") {
		problem(w, 400, "请填写账号名称和有效的 DNSPod API Token ID、Token")
		return
	}
	id, _, err := p.randomCredential()
	if err != nil {
		problem(w, 503, "无法创建 DNS 账号")
		return
	}
	data, _ := json.Marshal(dnsCredentials{body.TokenID, body.Token})
	if err = p.secrets.Put(r.Context(), "dnspod."+id, data); err == nil {
		_, err = p.db.ExecContext(r.Context(), "INSERT INTO dns_accounts(id,name,provider) VALUES(?,?,'dnspod')", id, body.Name)
	}
	if err != nil {
		problem(w, 503, "无法保存 DNS 账号")
		return
	}
	respond(w, 201, dnsAccount{ID: id, Name: body.Name, Provider: "dnspod", Configured: true})
}

func (p *Panel) dnsCredential(ctx context.Context, id string) (dnsCredentials, error) {
	var credentials dnsCredentials
	data, err := p.secrets.Get(ctx, "dnspod."+id)
	if err == nil {
		err = json.Unmarshal(data, &credentials)
	}
	return credentials, err
}
