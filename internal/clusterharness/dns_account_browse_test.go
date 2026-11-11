package clusterharness_test

import (
	"context"
	"errors"
	"testing"

	"nbpass/internal/dnsprovider"
)

// 挑选记录只读：列出域名与 A 记录，标出绑定会被拒绝的原因和已有归属，不写 DNS。
func TestDNSAccountBrowseListsDomainsAndBindableRecords(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	fake.Set("example.com", dnsprovider.Record{ID: "101", Name: "entry", Type: "AAAA", Line: "默认", LineID: "0", Value: "2001:db8::1", TTL: 300, Enabled: true})
	fake.Set("example.com", dnsprovider.Record{ID: "200", Name: "split", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 600, Enabled: false})
	fake.Set("example.com", dnsprovider.Record{ID: "201", Name: "split", Type: "A", Line: "电信", LineID: "10=0", Value: "8.8.4.4", TTL: 600, Enabled: true})
	fake.Set("example.com", dnsprovider.Record{ID: "300", Name: "@", Type: "A", Line: "默认", LineID: "0", Value: "1.1.1.1", TTL: 600, Enabled: false})
	fake.Set("example.com", dnsprovider.Record{ID: "400", Name: "www", Type: "CNAME", Line: "默认", LineID: "0", Value: "entry.example.com.", TTL: 600, Enabled: true})
	fake.Set("example.net", dnsprovider.Record{ID: "500", Name: "edge", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	h.Client().Do("GET", "/api/dns-accounts/"+account+"/domains", nil, 401)
	h.Client().Do("GET", "/api/dns-accounts/"+account+"/records?domain=example.com", nil, 401)

	domains := c.Do("GET", "/api/dns-accounts/"+account+"/domains", nil, 200)["domains"].([]any)
	if len(domains) != 2 || domains[0].(map[string]any)["name"] != "example.com" || domains[1].(map[string]any)["name"] != "example.net" {
		t.Fatalf("域名列表 %v", domains)
	}
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	records := map[string]map[string]any{}
	for _, row := range c.Do("GET", "/api/dns-accounts/"+account+"/records?domain=EXAMPLE.COM.", nil, 200)["records"].([]any) {
		r := row.(map[string]any)
		records[r["id"].(string)] = r
	}
	if len(records) != 4 || records["101"] != nil || records["400"] != nil {
		t.Fatalf("只应列出 A 记录：%v", records)
	}
	entry := records["100"]
	if entry["hostname"] != "entry.example.com" || entry["line_id"] != "0" || entry["value"] != "9.9.9.9" || entry["reason"] != "" || entry["owner"] != "入口组 入口组 A" {
		t.Fatalf("可绑定记录及归属 %v", entry)
	}
	if records["200"]["reason"] == "" || records["201"]["reason"] == "" {
		t.Fatalf("同名多线路未标为不可绑定 %v %v", records["200"], records["201"])
	}
	if apex := records["300"]; apex["hostname"] != "example.com" || apex["reason"] != "" || apex["enabled"] != false || apex["owner"] != "" {
		t.Fatalf("根域暂停记录 %v", apex)
	}
	if fake.Writes() != 0 {
		t.Fatal("浏览记录修改了 DNS")
	}

	c.Do("GET", "/api/dns-accounts/"+account+"/records", nil, 400)
	c.Do("GET", "/api/dns-accounts/missing/domains", nil, 404)
	fake.Read = func(context.Context) error { return &dnsprovider.RejectionError{Code: "10004"} }
	if e := c.Do("GET", "/api/dns-accounts/"+account+"/domains", nil, 502); e["error"] != "DNSPod 登录失败，请核对 API Token ID 与 Token" {
		t.Fatalf("登录失败提示 %v", e)
	}
	fake.Read = func(context.Context) error { return errors.New("timeout") }
	c.Do("GET", "/api/dns-accounts/"+account+"/records?domain=example.com", nil, 502)
}
