package fakedns

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"nbpass/internal/dnsprovider"
)

func TestFakeDNSPodModifyWithoutStatusEnablesAndStatusRejectionDoesNotChangeRecord(t *testing.T) {
	f := NewDNSPod()
	f.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: false})
	service := httptest.NewServer(f)
	defer service.Close()
	form := url.Values{"domain": {"example.com"}, "record_id": {"100"}, "sub_domain": {"entry"}, "record_type": {"A"}, "record_line_id": {"0"}, "value": {"8.8.8.8"}, "ttl": {"60"}}
	resp, err := service.Client().PostForm(service.URL+"/Record.Modify", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if r := f.Record("example.com", "100"); !r.Enabled || r.Value != "8.8.8.8" {
		t.Fatal("替身未模拟省略 status 默认启用", r)
	}
	d := dnsprovider.DNSPod{URL: service.URL, Client: service.Client()}
	if err := d.SetRecordEnabled(context.Background(), dnsprovider.Credentials{}, dnsprovider.Key{Domain: "example.com", RecordID: "100"}, false); err != nil {
		t.Fatal(err)
	}
	f.EnableError = "500025"
	err = d.SetRecordEnabled(context.Background(), dnsprovider.Credentials{}, dnsprovider.Key{Domain: "example.com", RecordID: "100"}, true)
	var rejection *dnsprovider.RejectionError
	if !errors.As(err, &rejection) || rejection.Code != "500025" || f.Record("example.com", "100").Enabled {
		t.Fatal("拒绝启用没有保持暂停", err)
	}
}
