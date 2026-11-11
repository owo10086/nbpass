package dnsprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDNSPodModifyExplicitlyPreservesRecordStatus(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "暂停", true: "启用"}[enabled], func(t *testing.T) {
			calls := 0
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				want := "disable"
				if enabled {
					want = "enable"
				}
				if r.URL.Path != "/Record.Modify" || r.Form.Get("status") != want {
					t.Errorf("改值必须显式保留状态，收到 %s %v", r.URL.Path, r.Form)
				}
				json.NewEncoder(w).Encode(map[string]any{"status": map[string]string{"code": "1"}})
			}))
			defer service.Close()
			d := DNSPod{URL: service.URL, Client: service.Client()}
			err := d.WriteRecord(context.Background(), Credentials{}, Key{"example.com", "100"}, Record{ID: "100", Name: "entry", Type: "A", LineID: "0", Value: "8.8.8.8", TTL: 60, Enabled: enabled})
			if err != nil || calls != 1 {
				t.Fatalf("改值未完成：调用 %d，错误 %v", calls, err)
			}
		})
	}
}

func TestDNSPodStatusRejectionIsDefinitiveAndTimeoutIsUnknown(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path != "/Record.Status" || r.Form.Get("status") != "enable" {
			t.Errorf("启用必须是明确的状态步骤：%s %v", r.URL.Path, r.Form)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": map[string]string{"code": "500025", "message": "不能泄露服务商原文"}})
	}))
	defer service.Close()
	d := DNSPod{URL: service.URL, Client: service.Client()}
	err := d.SetRecordEnabled(context.Background(), Credentials{}, Key{"example.com", "100"}, true)
	var rejected *RejectionError
	if !errors.As(err, &rejected) || rejected.Code != "500025" {
		t.Fatalf("明确拒绝未分类：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = d.SetRecordEnabled(ctx, Credentials{}, Key{"example.com", "100"}, true)
	if err == nil || errors.As(err, &rejected) {
		t.Fatalf("结果未知被当成明确拒绝：%v", err)
	}
}

func TestDNSPodListsPageThroughDomainsAndRecords(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		ok := map[string]string{"code": "1"}
		switch {
		case r.URL.Path == "/Domain.List" && r.Form.Get("offset") == "0":
			json.NewEncoder(w).Encode(map[string]any{"status": ok, "info": map[string]any{"domain_total": "2"}, "domains": []any{map[string]any{"name": "例子.com", "punycode": "xn--fsqu00a.com", "status": "enable"}}})
		case r.URL.Path == "/Domain.List" && r.Form.Get("offset") == "1":
			json.NewEncoder(w).Encode(map[string]any{"status": ok, "info": map[string]any{"domain_total": 2}, "domains": []any{map[string]any{"name": "example.com", "status": "pause"}}})
		case r.URL.Path == "/Record.List" && r.Form.Get("domain") == "example.com" && r.Form.Get("sub_domain") == "":
			records := []any{
				map[string]any{"id": 100, "name": "entry", "type": "A", "line": "默认", "line_id": "0", "value": "8.8.8.8", "ttl": "600", "enabled": "1"},
				map[string]any{"id": 101, "name": "bad", "type": "A", "line": "默认", "line_id": "0", "value": "8.8.4.4", "ttl": "x"},
			}
			if r.Form.Get("offset") != "0" {
				records = []any{map[string]any{"id": 102, "name": "@", "type": "NS", "line": "默认", "line_id": "0", "value": "f1g1ns1.dnspod.net.", "ttl": "86400", "enabled": "0"}}
			}
			json.NewEncoder(w).Encode(map[string]any{"status": ok, "info": map[string]any{"record_total": "3"}, "records": records})
		default:
			t.Errorf("意外请求 %s %v", r.URL.Path, r.Form)
			http.Error(w, "unexpected", 400)
		}
	}))
	defer service.Close()
	d := DNSPod{URL: service.URL, Client: service.Client()}
	domains, err := d.ListDomains(context.Background(), Credentials{})
	if err != nil || len(domains) != 2 || domains[0] != (Domain{"xn--fsqu00a.com", "enable"}) || domains[1] != (Domain{"example.com", "pause"}) {
		t.Fatalf("域名列表 %v %v", domains, err)
	}
	records, err := d.ListRecords(context.Background(), Credentials{}, "example.com")
	if err != nil || len(records) != 2 || records[0].ID != "100" || !records[0].Enabled || records[1].Type != "NS" {
		t.Fatalf("记录列表 %+v %v", records, err)
	}
}

func TestDNSPodListDomainsTreatsNoDomainsAsEmpty(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": map[string]string{"code": "9"}})
	}))
	defer service.Close()
	domains, err := DNSPod{URL: service.URL, Client: service.Client()}.ListDomains(context.Background(), Credentials{})
	if err != nil || len(domains) != 0 {
		t.Fatalf("空账号 %v %v", domains, err)
	}
}
