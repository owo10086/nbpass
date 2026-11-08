package clusterharness_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/panel"
)

// 真实适配器同样通过面板 HTTP 验收；假服务仅模拟 DNSPod 的外部 HTTP 边界。
func TestManagedDNSRealAdapterUsesTokenAndOnlyModifiesBoundRecord(t *testing.T) {
	var mu sync.Mutex
	var proxied atomic.Bool
	value, ttl, writes := "9.9.9.9", "600", 0
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("login_token") != "12345,adapter-secret" || r.Form.Get("format") != "json" || r.Form.Get("domain") != "example.com" {
			t.Error("DNSPod 请求缺少认证或记录范围")
			http.Error(w, "bad", 400)
			return
		}
		status := map[string]string{"code": "1"}
		switch r.URL.Path {
		case "/Record.Info":
			if r.Form.Get("record_id") != "100" {
				t.Error("读取了错误的记录")
			}
			json.NewEncoder(w).Encode(map[string]any{"status": status, "record": map[string]any{"id": "100", "sub_domain": "entry", "record_type": "A", "record_line": "默认", "record_line_id": "0", "value": value, "ttl": ttl, "enabled": "1"}})
		case "/Record.List":
			if r.Form.Get("sub_domain") != "entry" {
				t.Error("同名记录查询范围错误")
			}
			proxy := "no"
			if proxied.Load() {
				proxy = "yes"
			}
			records := []any{map[string]any{"id": "100", "name": "entry", "type": "A", "line": "默认", "line_id": "0", "value": value, "ttl": ttl, "status": "enable", "use_aqb": proxy}, map[string]any{"id": "101", "name": "entry", "type": "AAAA", "line": "默认", "line_id": "0", "value": "2001:db8::1", "ttl": "600", "status": "enable"}}
			// 同名列表分页，确保不会因首批只有受管记录漏掉其他记录。
			offset := r.Form.Get("offset")
			if offset == "0" {
				records = records[:1]
			} else {
				records = records[1:]
			}
			json.NewEncoder(w).Encode(map[string]any{"status": status, "info": map[string]string{"record_total": "2"}, "records": records})
		case "/Record.Modify":
			if r.Form.Get("record_id") != "100" || r.Form.Get("sub_domain") != "entry" || r.Form.Get("record_type") != "A" || r.Form.Get("record_line_id") != "0" || r.Form.Get("ttl") != "60" {
				t.Error("修改了错误范围或属性")
			}
			value, ttl = r.Form.Get("value"), r.Form.Get("ttl")
			writes++
			json.NewEncoder(w).Encode(map[string]any{"status": status, "record": map[string]string{"id": "100"}})
		default:
			t.Errorf("禁止其他服务商动作: %s", r.URL.Path)
			http.Error(w, "bad", 400)
		}
	}))
	defer service.Close()
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.DNSProvider = dnsprovider.DNSPod{Client: service.Client(), URL: service.URL}
	})
	c := h.Client()
	setup(c)
	login(c)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "12345", "token": "adapter-secret"}, 201)["id"].(string)
	id := addNode(c, "入口")["id"].(string)
	gid := entryGroup(c, id)["id"].(string)
	proxied.Store(true)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 400)
	proxied.Store(false)
	b := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	if len(b["warnings"].([]any)) != 1 {
		t.Fatal("未读取后续分页中的 AAAA")
	}
	firstPublish(c, gid, id, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || value != "8.8.8.8" || ttl != "60" {
		t.Fatalf("实际 DNSPod HTTP 写入不正确: %s %s %d", value, ttl, writes)
	}
}
