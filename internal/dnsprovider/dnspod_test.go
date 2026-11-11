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
