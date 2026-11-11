package fakedns

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nbpass/internal/dnsprovider"
)

func TestFakeDNSPodModifyWithoutStatusEnablesAndStatusRejectionDoesNotChangeRecord(t *testing.T) {
	f := NewDNSPod()
	f.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: false})
	service := httptest.NewServer(f)
	defer service.Close()
	req, _ := http.NewRequest("POST", service.URL, strings.NewReader(`{"Domain":"example.com","RecordId":100,"SubDomain":"entry","RecordType":"A","RecordLine":"默认","RecordLineId":"0","Value":"8.8.8.8","TTL":60}`))
	req.Header.Set("Authorization", "TC3-HMAC-SHA256 Credential=AKIDtest/2026-01-01/dnspod/tc3_request")
	req.Header.Set("X-TC-Action", "ModifyRecord")
	resp, err := service.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if r := f.Record("example.com", "100"); !r.Enabled || r.Value != "8.8.8.8" {
		t.Fatal("替身未模拟省略 Status 默认启用", r)
	}
	d := dnsprovider.DNSPod{URL: service.URL, Client: service.Client()}
	if err := d.SetRecordEnabled(context.Background(), dnsprovider.Credentials{}, dnsprovider.Key{Domain: "example.com", RecordID: "100"}, false); err != nil {
		t.Fatal(err)
	}
	f.EnableError = "FailedOperation.DomainIsLocked"
	err = d.SetRecordEnabled(context.Background(), dnsprovider.Credentials{}, dnsprovider.Key{Domain: "example.com", RecordID: "100"}, true)
	var rejection *dnsprovider.RejectionError
	if !errors.As(err, &rejection) || rejection.Code != "FailedOperation.DomainIsLocked" || f.Record("example.com", "100").Enabled {
		t.Fatal("拒绝启用没有保持暂停", err)
	}
}
