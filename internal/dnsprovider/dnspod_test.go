package dnsprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// 腾讯云签名 v3 文档示例：规范请求的哈希与密钥无关，可逐字核对拼接。
func TestTC3CanonicalRequestMatchesDocumentedExample(t *testing.T) {
	payload := `{"Limit": 1, "Filters": [{"Values": ["\u672a\u547d\u540d"], "Name": "instance-name"}]}`
	canonical := sha256.Sum256([]byte(tc3CanonicalRequest("cvm.tencentcloudapi.com", "application/json; charset=utf-8", "DescribeInstances", []byte(payload))))
	if got := hex.EncodeToString(canonical[:]); got != "7019a55be8395899b900fb5564e4200d984910f34794a27cb3fb7d10ff6a1e84" {
		t.Fatalf("规范请求哈希 %s", got)
	}
}

// tc3Server 校验签名后按动作应答；Response 为 nil 时返回 400 让测试失败。
func tc3Server(t *testing.T, handle func(action string, body map[string]any) map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		timestamp, _ := strconv.ParseInt(r.Header.Get("X-TC-Timestamp"), 10, 64)
		host, _ := url.Parse("http://" + r.Host)
		want := tc3Authorization(Credentials{"AKIDexample", "exampleKey"}, "dnspod", host.Host, r.Header.Get("Content-Type"), r.Header.Get("X-TC-Action"), data, timestamp)
		var body map[string]any
		if r.Header.Get("Authorization") != want || r.Header.Get("X-TC-Version") != "2021-03-23" || json.Unmarshal(data, &body) != nil {
			t.Errorf("签名或公共参数错误：%s", r.Header.Get("Authorization"))
			http.Error(w, "bad", 400)
			return
		}
		response := handle(r.Header.Get("X-TC-Action"), body)
		if response == nil {
			t.Errorf("意外请求 %s %v", r.Header.Get("X-TC-Action"), body)
			http.Error(w, "unexpected", 400)
			return
		}
		response["RequestId"] = "test"
		json.NewEncoder(w).Encode(map[string]any{"Response": response})
	}))
}

var testAuth = Credentials{"AKIDexample", "exampleKey"}

func TestDNSPodModifyExplicitlyPreservesRecordStatus(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "暂停", true: "启用"}[enabled], func(t *testing.T) {
			calls := 0
			service := tc3Server(t, func(action string, body map[string]any) map[string]any {
				calls++
				want := map[bool]string{false: "DISABLE", true: "ENABLE"}[enabled]
				if action != "ModifyRecord" || body["Status"] != want || body["RecordId"] != float64(100) || body["RecordLine"] != "默认" || body["RecordLineId"] != "0" {
					t.Errorf("改值必须显式保留状态，收到 %s %v", action, body)
				}
				return map[string]any{"RecordId": 100}
			})
			defer service.Close()
			d := DNSPod{URL: service.URL, Client: service.Client()}
			err := d.WriteRecord(context.Background(), testAuth, Key{"example.com", "100"}, Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 60, Enabled: enabled})
			if err != nil || calls != 1 {
				t.Fatalf("改值未完成：调用 %d，错误 %v", calls, err)
			}
		})
	}
}

func TestDNSPodStatusRejectionIsDefinitiveAndInternalErrorIsUnknown(t *testing.T) {
	code := "FailedOperation.DomainIsLocked"
	service := tc3Server(t, func(action string, body map[string]any) map[string]any {
		if action != "ModifyRecordStatus" || body["Status"] != "ENABLE" {
			t.Errorf("启用必须是明确的状态步骤：%s %v", action, body)
		}
		return map[string]any{"Error": map[string]string{"Code": code, "Message": "不能泄露服务商原文"}}
	})
	defer service.Close()
	d := DNSPod{URL: service.URL, Client: service.Client()}
	err := d.SetRecordEnabled(context.Background(), testAuth, Key{"example.com", "100"}, true)
	var rejected *RejectionError
	if !errors.As(err, &rejected) || rejected.Code != code || strings.Contains(err.Error(), "原文") {
		t.Fatalf("明确拒绝未分类：%v", err)
	}
	code = "InternalError.UndefinedError"
	if err = d.SetRecordEnabled(context.Background(), testAuth, Key{"example.com", "100"}, true); err == nil || errors.As(err, &rejected) {
		t.Fatalf("内部错误被当成明确拒绝：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = d.SetRecordEnabled(ctx, testAuth, Key{"example.com", "100"}, true)
	if err == nil || errors.As(err, &rejected) {
		t.Fatalf("结果未知被当成明确拒绝：%v", err)
	}
	if err = d.SetRecordEnabled(context.Background(), testAuth, Key{"example.com", "abc"}, true); err == nil {
		t.Fatal("非数字记录 ID 未拒绝")
	}
}

func TestDNSPodListsPageThroughDomainsAndRecords(t *testing.T) {
	service := tc3Server(t, func(action string, body map[string]any) map[string]any {
		switch {
		case action == "DescribeDomainList" && body["Offset"] == float64(0):
			return map[string]any{"DomainCountInfo": map[string]any{"DomainTotal": 2}, "DomainList": []any{map[string]any{"Name": "例子.com", "Punycode": "xn--fsqu00a.com", "Status": "ENABLE"}}}
		case action == "DescribeDomainList" && body["Offset"] == float64(1):
			return map[string]any{"DomainCountInfo": map[string]any{"DomainTotal": 2}, "DomainList": []any{map[string]any{"Name": "example.com", "Status": "PAUSE"}}}
		case action == "DescribeRecordList" && body["Domain"] == "example.com" && body["SubDomain"] == nil && body["ErrorOnEmpty"] == "no":
			records := []any{
				map[string]any{"RecordId": 100, "Name": "entry", "Type": "A", "Line": "默认", "LineId": "0", "Value": "8.8.8.8", "TTL": 600, "Status": "ENABLE"},
				map[string]any{"RecordId": 101, "Name": "bad", "Type": "A", "Line": "默认", "LineId": "0", "Value": "8.8.4.4", "TTL": 0},
			}
			if body["Offset"] != float64(0) {
				records = []any{map[string]any{"RecordId": 102, "Name": "@", "Type": "NS", "Line": "默认", "Value": "f1g1ns1.dnspod.net.", "TTL": 86400, "Status": "DISABLE"}}
			}
			return map[string]any{"RecordCountInfo": map[string]any{"TotalCount": 3}, "RecordList": records}
		}
		return nil
	})
	defer service.Close()
	d := DNSPod{URL: service.URL, Client: service.Client()}
	domains, err := d.ListDomains(context.Background(), testAuth)
	if err != nil || len(domains) != 2 || domains[0] != (Domain{"xn--fsqu00a.com", "enable"}) || domains[1] != (Domain{"example.com", "pause"}) {
		t.Fatalf("域名列表 %v %v", domains, err)
	}
	records, err := d.ListRecords(context.Background(), testAuth, "example.com")
	if err != nil || len(records) != 2 || records[0].ID != "100" || !records[0].Enabled || records[1].Type != "NS" || records[1].LineID != "0" || records[1].Enabled {
		t.Fatalf("记录列表 %+v %v", records, err)
	}
}

func TestDNSPodReadRecordChecksSameNameList(t *testing.T) {
	value := "9.9.9.9"
	service := tc3Server(t, func(action string, body map[string]any) map[string]any {
		switch action {
		case "DescribeRecord":
			return map[string]any{"RecordInfo": map[string]any{"Id": 100, "SubDomain": "entry", "RecordType": "A", "RecordLine": "默认", "RecordLineId": "0", "Value": "9.9.9.9", "TTL": 600, "Enabled": 1}}
		case "DescribeRecordList":
			if body["SubDomain"] != "entry" {
				return nil
			}
			return map[string]any{"RecordCountInfo": map[string]any{"TotalCount": 2}, "RecordList": []any{
				map[string]any{"RecordId": 100, "Name": "entry", "Type": "A", "Line": "默认", "LineId": "0", "Value": value, "TTL": 600, "Status": "ENABLE"},
				map[string]any{"RecordId": 101, "Name": "entry", "Type": "AAAA", "Line": "默认", "LineId": "0", "Value": "2001:db8::1", "TTL": 600, "Status": "ENABLE"},
			}}
		}
		return nil
	})
	defer service.Close()
	d := DNSPod{URL: service.URL, Client: service.Client()}
	result, err := d.ReadRecord(context.Background(), testAuth, Key{"example.com", "100"})
	if err != nil || result.Record != (Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true}) || len(result.Related) != 2 {
		t.Fatalf("读取记录 %+v %v", result, err)
	}
	value = "1.1.1.1"
	if _, err = d.ReadRecord(context.Background(), testAuth, Key{"example.com", "100"}); err == nil {
		t.Fatal("详情与列表不一致时未拒绝")
	}
}

func TestDNSPodListDomainsTreatsNoDataAsEmpty(t *testing.T) {
	service := tc3Server(t, func(string, map[string]any) map[string]any {
		return map[string]any{"Error": map[string]string{"Code": "ResourceNotFound.NoDataOfDomain"}}
	})
	defer service.Close()
	domains, err := DNSPod{URL: service.URL, Client: service.Client()}.ListDomains(context.Background(), testAuth)
	if err != nil || len(domains) != 0 {
		t.Fatalf("空账号 %v %v", domains, err)
	}
}

func TestDNSPodCreatesPausedRecordAndFindsByName(t *testing.T) {
	service := tc3Server(t, func(action string, body map[string]any) map[string]any {
		switch action {
		case "CreateRecord":
			if body["SubDomain"] != "app" || body["RecordType"] != "A" || body["RecordLine"] != "默认" || body["RecordLineId"] != "0" || body["Status"] != "DISABLE" || body["Value"] != "8.8.8.8" || body["TTL"] != float64(600) {
				t.Errorf("新建参数错误：%v", body)
			}
			return map[string]any{"RecordId": 9001}
		case "DescribeRecordList":
			if body["SubDomain"] != "app" {
				t.Errorf("按名字查询须带 SubDomain：%v", body)
			}
			return map[string]any{"RecordCountInfo": map[string]any{"TotalCount": 2}, "RecordList": []any{
				map[string]any{"RecordId": 9001, "Name": "app", "Type": "A", "Line": "默认", "LineId": "0", "Value": "8.8.8.8", "TTL": 600, "Status": "DISABLE"},
				map[string]any{"RecordId": 9002, "Name": "app.cdn", "Type": "CNAME", "Line": "默认", "LineId": "0", "Value": "x.example.net.", "TTL": 600, "Status": "ENABLE"},
			}}
		}
		return nil
	})
	defer service.Close()
	d := DNSPod{URL: service.URL, Client: service.Client()}
	id, err := d.CreateRecord(context.Background(), testAuth, "example.com", Record{Name: "app", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 600})
	if err != nil || id != "9001" {
		t.Fatalf("新建 %q %v", id, err)
	}
	if _, err = d.CreateRecord(context.Background(), testAuth, "example.com", Record{Name: "app", Type: "AAAA", Line: "默认", LineID: "0", Value: "::1", TTL: 600}); err == nil {
		t.Fatal("不支持的类型未拒绝")
	}
	records, err := d.FindRecords(context.Background(), testAuth, "example.com", "app")
	if err != nil || len(records) != 1 || records[0].ID != "9001" || records[0].Enabled {
		t.Fatalf("按名字查询混入其他记录：%+v %v", records, err)
	}
}

func TestDNSPodWriteCanChangeRecordType(t *testing.T) {
	service := tc3Server(t, func(action string, body map[string]any) map[string]any {
		if action != "ModifyRecord" || body["RecordType"] != "A" || body["Status"] != "DISABLE" {
			t.Errorf("改类型须带新类型与状态：%s %v", action, body)
		}
		return map[string]any{"RecordId": 100}
	})
	defer service.Close()
	d := DNSPod{URL: service.URL, Client: service.Client()}
	if err := d.WriteRecord(context.Background(), testAuth, Key{"example.com", "100"}, Record{ID: "100", Name: "web", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 600}); err != nil {
		t.Fatal(err)
	}
}
