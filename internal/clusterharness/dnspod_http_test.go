package clusterharness_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/panel"
)

// 真实适配器同样通过面板 HTTP 验收；假服务仅模拟腾讯云 API 3.0 的外部 HTTP 边界。
func TestManagedDNSRealAdapterUsesSecretIdAndOnlyModifiesBoundRecord(t *testing.T) {
	var mu sync.Mutex
	value, ttl, writes := "9.9.9.9", 600, 0
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body struct {
			Domain       string
			RecordId     int
			SubDomain    string
			RecordType   string
			RecordLine   string
			RecordLineId string
			Value        string
			TTL          int
			Status       string
			Offset       int
		}
		if r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=AKIDtest12345/") || r.Header.Get("X-TC-Version") != "2021-03-23" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Domain != "example.com" {
			t.Error("DNSPod 请求缺少认证或记录范围")
			http.Error(w, "bad", 400)
			return
		}
		var response map[string]any
		switch r.Header.Get("X-TC-Action") {
		case "DescribeRecord":
			if body.RecordId != 100 {
				t.Error("读取了错误的记录")
			}
			response = map[string]any{"RecordInfo": map[string]any{"Id": 100, "SubDomain": "entry", "RecordType": "A", "RecordLine": "默认", "RecordLineId": "0", "Value": value, "TTL": ttl, "Enabled": 1}}
		case "DescribeRecordList":
			if body.SubDomain != "entry" {
				t.Error("同名记录查询范围错误")
			}
			records := []any{map[string]any{"RecordId": 100, "Name": "entry", "Type": "A", "Line": "默认", "LineId": "0", "Value": value, "TTL": ttl, "Status": "ENABLE"}, map[string]any{"RecordId": 101, "Name": "entry", "Type": "AAAA", "Line": "默认", "LineId": "0", "Value": "2001:db8::1", "TTL": 600, "Status": "ENABLE"}}
			// 同名列表分页，确保不会因首批只有受管记录漏掉其他记录。
			if body.Offset == 0 {
				records = records[:1]
			} else {
				records = records[1:]
			}
			response = map[string]any{"RecordCountInfo": map[string]int{"TotalCount": 2}, "RecordList": records}
		case "ModifyRecord":
			if body.RecordId != 100 || body.SubDomain != "entry" || body.RecordType != "A" || body.RecordLine != "默认" || body.RecordLineId != "0" || body.TTL != 60 || body.Status != "ENABLE" {
				t.Error("修改了错误范围或属性")
			}
			value, ttl = body.Value, body.TTL
			writes++
			response = map[string]any{"RecordId": 100}
		default:
			t.Errorf("禁止其他服务商动作: %s", r.Header.Get("X-TC-Action"))
			http.Error(w, "bad", 400)
			return
		}
		response["RequestId"] = "test"
		json.NewEncoder(w).Encode(map[string]any{"Response": response})
	}))
	defer service.Close()
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.DNSProvider = dnsprovider.DNSPod{Client: service.Client(), URL: service.URL}
	})
	c := h.Client()
	setup(c)
	login(c)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "adapterSecretKey"}, 201)["id"].(string)
	id := addNode(c, "入口")["id"].(string)
	gid := entryGroup(c, id)["id"].(string)
	b := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	if len(b["warnings"].([]any)) != 1 {
		t.Fatal("未读取后续分页中的 AAAA")
	}
	firstPublish(c, gid, id, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	mu.Lock()
	defer mu.Unlock()
	if writes != 1 || value != "8.8.8.8" || ttl != 60 {
		t.Fatalf("实际 DNSPod HTTP 写入不正确: %s %d %d", value, ttl, writes)
	}
}
