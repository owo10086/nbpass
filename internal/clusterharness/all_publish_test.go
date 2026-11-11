package clusterharness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/dnswatch"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func allGroup(c *clusterharness.Client, members ...string) string {
	return c.Do("POST", "/api/entry-groups", map[string]any{"name": "全部发布测试", "members": members, "publication_mode": "all"}, 201)["id"].(string)
}
func memberBindingPath(g, n string) string {
	return "/api/entry-groups/" + g + "/members/" + n + "/managed-record"
}
func bindMember(c *clusterharness.Client, g, n, a, r string, status int) map[string]any {
	b := binding(a)
	b["record_id"] = r
	return c.Do("PUT", memberBindingPath(g, n), b, status)
}
func TestAllPublishModeAndMemberBinding(t *testing.T) {
	_, c, fake, a, legacy := dnsBrowser(t)
	n1, n2 := memberOf(c, legacy), addNode(c, "成员二")["id"].(string)
	g := allGroup(c, n1, n2)
	c.Do("PUT", "/api/entry-groups/"+g, map[string]any{"name": "改模式", "members": []string{n1, n2}, "publication_mode": "primary_backup"}, 409)
	groups := c.Do("GET", "/api/entry-groups", nil, 200)["entry_groups"].([]any)
	for _, v := range groups {
		row := v.(map[string]any)
		if row["id"] == g && row["publication_mode"] != "all" || row["id"] == legacy && row["publication_mode"] != "primary_backup" {
			t.Fatalf("发布模式错误：%v", row)
		}
	}
	r := fake.Record("example.com", "100")
	r.Enabled = false
	fake.Set("example.com", r)
	b := bindMember(c, g, n1, a, "100", 200)
	if b["current_enabled"] != false {
		t.Fatalf("暂停记录不能绑定：%v", b)
	}
	r.ID = "101"
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "100", 409)
	r.Name = "other"
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "101", 400)
	r.Name = "entry"
	r.Type = "AAAA"
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "101", 400)
	r.Type = "A"
	r.LineID = "1"
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "101", 400)
	r.LineID = "0"
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "101", 200)
	c.Do("PUT", "/api/entry-groups/"+g+"/managed-record", binding(a), 409)
	c.Do("PUT", "/api/entry-groups/"+g+"/blocked-policy", map[string]string{"policy": "switch_entry"}, 409)
	if fake.Writes() != 0 {
		t.Fatal("首次发布前发生 DNS 写入")
	}
}

func TestAllPublishRequiresEveryBindingAndHealthyMember(t *testing.T) {
	_, c, fake, a, legacy := dnsBrowser(t)
	n1, n2 := memberOf(c, legacy), addNode(c, "缺记录成员")["id"].(string)
	g := allGroup(c, n1, n2)
	bindMember(c, g, n1, a, "100", 200)
	result := c.Do("POST", "/api/entry-groups/"+g+"/publish", map[string]any{"confirm_inbound": true, "member_ipv4": map[string]string{n1: "8.8.8.8"}}, 409)
	if !strings.Contains(result["error"].(string), "缺记录成员") {
		t.Fatalf("未指出缺少绑定的成员：%v", result)
	}
	r := fake.Record("example.com", "100")
	r.ID = "101"
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "101", 200)
	c.Do("POST", "/api/entry-groups/"+g+"/publish", map[string]any{"confirm_inbound": false}, 400)
	// 单独的离线组即使有地址也不能首次发布。
	g2 := allGroup(c, n2)
	r.ID = "102"
	fake.Set("example.com", r)
	bindMember(c, g2, n2, a, "102", 200)
	c.Do("POST", "/api/entry-groups/"+g2+"/publish", map[string]any{"confirm_inbound": true, "member_ipv4": map[string]string{n2: "1.1.1.1"}}, 409)
	if fake.Writes() != 0 {
		t.Fatal("拒绝发布仍写了 DNS")
	}
}

func TestAllPublishFirstPublicationPreservesUnknownAddress(t *testing.T) {
	h, c, fake, a, legacy := dnsBrowser(t)
	n1, n2 := memberOf(c, legacy), addNode(c, "地址未知")["id"].(string)
	g := allGroup(c, n1, n2)
	r := fake.Record("example.com", "100")
	r.Enabled = false
	fake.Set("example.com", r)
	bindMember(c, g, n1, a, "100", 200)
	r.ID = "101"
	r.Value = "4.2.2.2"
	r.Enabled = true
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "101", 200)
	h.Clock.Advance(2e10)
	if fake.Writes() != 0 {
		t.Fatal("首次发布前自动写 DNS")
	}
	c.Do("POST", "/api/entry-groups/"+g+"/publish", map[string]any{"confirm_inbound": true, "member_ipv4": map[string]string{n1: "8.8.8.8"}}, 202)
	awaitDNSStage(t, c, g, "服务商已写入")
	one, two := fake.Record("example.com", "100"), fake.Record("example.com", "101")
	if one.Value != "8.8.8.8" || !one.Enabled || two.Value != "4.2.2.2" || two.Enabled {
		t.Fatalf("首次发布结果错误：%+v / %+v", one, two)
	}
	pub := publication(c, g)
	if pub["first_published"] != true {
		t.Fatalf("未记首次发布：%v", pub)
	}
	found := false
	for _, v := range pub["members"].([]any) {
		row := v.(map[string]any)
		if row["node_id"] == n2 {
			found = row["reason"] == "地址待确认" && row["expected_enabled"] == false
		}
	}
	if !found {
		t.Fatalf("没有地址待确认提示：%v", pub)
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	if publication(c, g)["first_published"] != true {
		t.Fatal("重启丢失首次发布标记")
	}
}

func TestAllPublishNewMemberWaitsPausedAndRemovalPausesBeforeUnbinding(t *testing.T) {
	h, c, fake, a, legacy := dnsBrowser(t)
	n1 := memberOf(c, legacy)
	g := allGroup(c, n1)
	bindMember(c, g, n1, a, "100", 200)
	c.Do("POST", "/api/entry-groups/"+g+"/publish", map[string]any{"confirm_inbound": true, "member_ipv4": map[string]string{n1: "8.8.8.8"}}, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	n2 := addNode(c, "后来新增")["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+g, map[string]any{"name": "全部发布", "members": []string{n1, n2}}, 200)
	r := fake.Record("example.com", "100")
	r.ID = "101"
	r.Enabled = true
	fake.Set("example.com", r)
	bindMember(c, g, n2, a, "101", 200)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	if fake.Record("example.com", "101").Enabled {
		t.Fatal("新增绑定未暂停待加回")
	}
	// 已发布的暂停成员仍可换绑，旧记录保持原状；唯一启用成员不能借换绑清空集合。
	old := fake.Record("example.com", "101")
	replacement := old
	replacement.ID = "102"
	replacement.Enabled = true
	fake.Set("example.com", replacement)
	rebound := bindMember(c, g, n2, a, "102", 200)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	if fake.Record("example.com", "101") != old || fake.Record("example.com", "102").Enabled || len(rebound["warnings"].([]any)) == 0 {
		t.Fatal("换绑未保持旧记录或未暂停新记录")
	}
	// 外部误启用待加回的成员时，逐记录审计恢复暂停，不影响其他成员。
	replacement = fake.Record("example.com", "102")
	replacement.Enabled = true
	fake.Set("example.com", replacement)
	for range 35 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(3 * time.Millisecond)
	}
	deadline := time.Now().Add(5 * time.Second)
	for fake.Record("example.com", "102").Enabled {
		if time.Now().After(deadline) {
			t.Fatal("逐记录审计没有恢复外部误启用的暂停成员")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	awaitDNSStage(t, c, g, panel.EntryWritten)
	if !fake.Record("example.com", "100").Enabled || fake.Record("example.com", "101") != old {
		t.Fatal("恢复误启用时改动了其他成员或旧记录")
	}
	replacement.ID = "103"
	fake.Set("example.com", replacement)
	bindMember(c, g, n1, a, "103", 409)
	c.Do("DELETE", "/api/entry-groups/"+g+"/members/"+n1, nil, 409)
	c.Do("PUT", "/api/entry-groups/"+g, map[string]any{"name": "不能绕过暂停", "members": []string{n1}}, 409)
	c.Do("DELETE", "/api/entry-groups/"+g+"/members/"+n2, nil, 202)
	for i := 0; i < 100; i++ {
		if len(publication(c, g)["members"].([]any)) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(publication(c, g)["members"].([]any)) != 1 || fake.Record("example.com", "102").Enabled {
		t.Fatal("成员没有先暂停再移出")
	}
	c.Do("GET", memberBindingPath(g, n2), nil, 404)
	writes := fake.Writes()
	result := c.Do("DELETE", "/api/entry-groups/"+g, nil, 200)
	if !strings.Contains(result["warning"].(string), "保持原状") || fake.Writes() != writes || !fake.Record("example.com", "100").Enabled {
		t.Fatal("删除组改变了记录或缺少提示")
	}
}

// 通过真实 DNSPod HTTP 适配器控制服务商边界，面板仍只用产品 API 操作。
func allHTTPRig(t *testing.T, intercept func(http.ResponseWriter, *http.Request, *clusterharness.DNSPod) bool, healthyB ...bool) (*clusterharness.Harness, *clusterharness.Client, *clusterharness.DNSPod, string, string, string) {
	ready := len(healthyB) > 0 && healthyB[0]
	return allHTTPRigWith(t, intercept, ready)
}
func allHTTPRigWith(t *testing.T, intercept func(http.ResponseWriter, *http.Request, *clusterharness.DNSPod) bool, readyB bool, options ...func(*panel.Dependencies)) (*clusterharness.Harness, *clusterharness.Client, *clusterharness.DNSPod, string, string, string) {
	t.Helper()
	fake := clusterharness.NewDNSPod()
	fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 60, Enabled: false})
	r := fake.Record("example.com", "100")
	r.ID = "101"
	fake.Set("example.com", r)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 把 API 3.0 的 JSON 请求摊平到 Form 供拦截判断，再原样交给替身。
		data, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(data))
		var fields map[string]any
		_ = json.Unmarshal(data, &fields)
		r.Form = url.Values{"Action": {r.Header.Get("X-TC-Action")}}
		for k, v := range fields {
			r.Form.Set(k, fmt.Sprint(v))
		}
		if intercept != nil && intercept(w, r, fake) {
			return
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.DNSProvider = &dnsprovider.DNSPod{Client: server.Client(), URL: server.URL}
		for _, option := range options {
			option(d)
		}
	})
	c := h.Client()
	setup(c)
	login(c)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "测试 DNSPod", "secret_id": "AKIDtest123", "secret_key": "testSecretKey"}, 201)["id"].(string)
	n := addNode(c, "健康成员")
	a := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, a, connected)
	second := addNode(c, "成员 B")
	b := second["id"].(string)
	if readyB {
		h.StartNode(second["installation_ticket"].(string))
		awaitNode(t, c, b, connected)
	}
	g := allGroup(c, a, b)
	bindMember(c, g, a, account, "100", 200)
	bindMember(c, g, b, account, "101", 200)
	return h, c, fake, g, a, b
}
func publishAll(c *clusterharness.Client, g string, ips map[string]string, status int) map[string]any {
	return c.Do("POST", "/api/entry-groups/"+g+"/publish", map[string]any{"confirm_inbound": true, "member_ipv4": ips}, status)
}

func TestAllPublishEnableRejectionKeepsLastOriginalAndNotifies(t *testing.T) {
	h, c, fake, g, a, b := allHTTPRig(t, func(w http.ResponseWriter, r *http.Request, _ *clusterharness.DNSPod) bool {
		if r.Form.Get("Action") == "ModifyRecordStatus" && r.Form.Get("RecordId") == "100" && r.Form.Get("Status") == "ENABLE" {
			w.Write([]byte(`{"Response":{"Error":{"Code":"FailedOperation.DomainIsLocked"}}}`))
			return true
		}
		return false
	})
	original := fake.Record("example.com", "101")
	original.Enabled = true
	fake.Set("example.com", original)
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "1.1.1.1"}, 202)
	pub := awaitDNSStage(t, c, g, panel.EntryFailed)
	if !fake.Record("example.com", "101").Enabled || fake.Record("example.com", "100").Enabled {
		t.Fatal("启用失败却暂停最后原记录")
	}
	if pub["first_published"] == true {
		t.Fatal("启用失败错误标成已首次发布")
	}
	h.Clock.Advance(time.Second)
	awaitEvents(t, h, c, "?type=entry_action", 4)
	actions := publication(c, g)["action"].(map[string]any)["records"].([]any)
	found := false
	for _, v := range actions {
		r := v.(map[string]any)
		found = found || strings.Contains(r["last_error"].(string), "FailedOperation.DomainIsLocked")
	}
	if !found {
		t.Fatalf("没有逐条拒绝码：%v", pub)
	}
}

func TestAllPublishUnknownEnableRestartReadsBackBeforePausingOriginal(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var blocked atomic.Bool
	blocked.Store(true)
	h, c, fake, g, a, b := allHTTPRig(t, func(w http.ResponseWriter, r *http.Request, f *clusterharness.DNSPod) bool {
		if r.Form.Get("Action") == "ModifyRecordStatus" && r.Form.Get("RecordId") == "100" && blocked.CompareAndSwap(true, false) {
			// 服务商已启用但响应丢失，面板不能拿未知结果作为暂停的保底。
			f.ServeHTTP(httptest.NewRecorder(), r)
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return true
		}
		return false
	})
	r := fake.Record("example.com", "101")
	r.Enabled = true
	fake.Set("example.com", r)
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "1.1.1.1"}, 202)
	waitSignal(t, entered)
	c.Do("DELETE", "/api/entry-groups/"+g+"/members/"+b, nil, 409)
	bindMember(c, g, b, c.Do("GET", memberBindingPath(g, b), nil, 200)["account_id"].(string), "101", 409)
	if !fake.Record("example.com", "101").Enabled {
		t.Fatal("启用未确认前暂停了原记录")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	awaitDNSStage(t, c, g, panel.EntryWritten)
	if fake.Writes() != 4 || fake.Record("example.com", "101").Enabled {
		t.Fatalf("重启重发已执行的启用或没有暂停原记录：%d %v", fake.Writes(), fake.Calls())
	}
}

func TestAllPublishPartialSuccessPersistsAndRejectsOnlyFailedEnable(t *testing.T) {
	h, c, fake, g, a, b := allHTTPRig(t, func(w http.ResponseWriter, r *http.Request, _ *clusterharness.DNSPod) bool {
		if r.Form.Get("Action") == "ModifyRecordStatus" && r.Form.Get("RecordId") == "101" {
			w.Write([]byte(`{"Response":{"Error":{"Code":"FailedOperation.DomainIsLocked"}}}`))
			return true
		}
		return false
	}, true)
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "1.1.1.1"}, 202)
	awaitDNSStage(t, c, g, panel.EntryPartial)
	pub := publication(c, g)
	for _, raw := range pub["members"].([]any) {
		m := raw.(map[string]any)
		if m["node_id"] == b && m["reason"] != "启用失败" {
			t.Fatalf("成员未显示启用失败：%v", m)
		}
	}
	if pub["first_published"] != true || !fake.Record("example.com", "100").Enabled || fake.Record("example.com", "101").Enabled {
		t.Fatalf("部分成功状态错误：%v", pub)
	}
	unbound := addNode(c, "未绑定新增成员")["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+g, map[string]any{"name": "部分完成", "members": []string{a, b, unbound}}, 200)
	c.Do("PUT", "/api/entry-groups/"+g, map[string]any{"name": "部分完成", "members": []string{a, b}}, 200)
	writes := fake.Writes()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	if publication(c, g)["action"].(map[string]any)["stage"] != panel.EntryPartial || fake.Writes() != writes {
		t.Fatal("重启重发了部分成功动作")
	}
}

func TestAllPublishSetWatchAndPerRecordStatusAudit(t *testing.T) {
	resolver := clusterharness.NewDNSResolver()
	resolver.Set(entryDomain, entryDoH, "8.8.8.8")
	const otherDoH = "https://other.example/dns-query"
	resolver.Set(entryDomain, otherDoH, "1.1.1.1")
	h, c, fake, g, a, b := allHTTPRigWith(t, nil, true, func(d *panel.Dependencies) { d.DNSWatchLookup = resolver.Lookup })
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": entryDoH}, {"kind": "doh", "address": otherDoH}}}, 200)
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "1.1.1.1"}, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	w := entryWatch(c, g)
	id := w["id"].(string)
	row := watchResult(t, c, id, func(r map[string]any) bool { return sampledPaths(r, 2) })
	if len(row["expected_ips"].([]any)) != 2 || row["status"] != "正常" {
		t.Fatalf("子集未算一致：%v", row)
	}
	// 集合启用传播只需任一路径返回各成员地址，不要求国内覆盖或所有路径含全量地址。
	h.Clock.Advance(time.Second)
	watchResult(t, c, id, func(r map[string]any) bool { return r["propagation_status"] == "已生效" })
	// 集合外地址连续出现才报疑似污染；解析偏差不会写 DNS。
	resolver.Set(entryDomain, otherDoH, "4.2.2.2")
	beforeWrites := fake.Writes()
	for i := 0; i < 3; i++ {
		watch := entryWatch(c, g)
		old := watch["round"].(float64)
		next, _ := time.Parse(time.RFC3339Nano, watch["next_sample_at"].(string))
		for h.Clock.Now().Before(next) {
			h.Clock.Advance(min(10*time.Second, next.Sub(h.Clock.Now())))
			time.Sleep(2 * time.Millisecond)
		}
		nextWatchRound(t, h, c, id, old, 2)
	}
	watchResult(t, c, id, func(r map[string]any) bool { return r["status"] == "疑似污染" })
	if fake.Writes() != beforeWrites {
		t.Fatal("解析集合外地址触发了 DNS 写入")
	}
	resolver.Set(entryDomain, otherDoH, "1.1.1.1")
	// 外部误暂停已发布记录、改值和误启用待加回记录，都须按逐条期望恢复。
	r := fake.Record("example.com", "100")
	r.Value = "4.2.2.2"
	r.Enabled = false
	fake.Set("example.com", r)
	for i := 0; i < 35; i++ {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(3 * time.Millisecond)
	}
	awaitDNSStage(t, c, g, panel.EntryWritten)
	deadline := time.Now().Add(5 * time.Second)
	for fake.Record("example.com", "100").Value != "8.8.8.8" || !fake.Record("example.com", "100").Enabled {
		if time.Now().After(deadline) {
			t.Fatal("未按记录期望核对地址和启用状态")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	c.Do("DELETE", "/api/entry-groups/"+g+"/members/"+b, nil, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	if entryWatch(c, g)["propagation_status"] == "已生效" {
		t.Fatal("其他路径仍含被移出地址却已生效")
	}
	resolver.Set(entryDomain, otherDoH, "8.8.8.8")
	h.Clock.Advance(30 * time.Second)
	watchResult(t, c, id, func(r map[string]any) bool { return r["propagation_status"] == "已生效" })
}

func TestAllPublishPrimaryOnlyOperationsAreRejected(t *testing.T) {
	_, c, _, _, legacy := dnsBrowser(t)
	n := memberOf(c, legacy)
	g := allGroup(c, n)
	c.Do("POST", "/api/entry-groups/"+g+"/resume-failback", map[string]any{"expected_version": 1}, 409)
	manualSwitch(c, g, n, "8.8.8.8", 0, 409)
}

func TestAllPublishUnknownPauseCannotCountAsLastEnabledGuard(t *testing.T) {
	var pauseCount atomic.Int32
	_, c, fake, g, a, b := allHTTPRig(t, func(w http.ResponseWriter, r *http.Request, f *clusterharness.DNSPod) bool {
		if r.Form.Get("Action") != "ModifyRecordStatus" {
			return false
		}
		if r.Form.Get("Status") == "ENABLE" {
			w.Write([]byte(`{"Response":{"Error":{"Code":"FailedOperation.DomainIsLocked"}}}`))
			return true
		}
		if pauseCount.Add(1) == 1 {
			f.ServeHTTP(httptest.NewRecorder(), r)
			w.Write([]byte("响应丢失"))
			return true
		}
		return false
	})
	third := addNode(c, "原成员二")["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+g, map[string]any{"name": "三成员", "members": []string{a, b, third}}, 200)
	r := fake.Record("example.com", "101")
	r.Enabled = true
	fake.Set("example.com", r)
	r.ID = "102"
	fake.Set("example.com", r)
	account := c.Do("GET", memberBindingPath(g, a), nil, 200)["account_id"].(string)
	bindMember(c, g, third, account, "102", 200)
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "1.1.1.1", third: "2.2.2.2"}, 202)
	awaitDNSStage(t, c, g, panel.EntryUnconfirmed)
	if !fake.Record("example.com", "101").Enabled && !fake.Record("example.com", "102").Enabled {
		t.Fatal("暂停结果未知被当成仍启用的保底，全部记录已暂停")
	}
}

func TestAllPublishTwoRecordRestartOnlyReadsUnconfirmedRecord(t *testing.T) {
	var statuses, reads atomic.Int32
	var lose atomic.Bool
	lose.Store(true)
	h, c, fake, g, a, b := allHTTPRig(t, func(w http.ResponseWriter, r *http.Request, f *clusterharness.DNSPod) bool {
		if r.Form.Get("Action") == "DescribeRecord" {
			reads.Add(1)
		}
		if r.Form.Get("Action") == "ModifyRecordStatus" && statuses.Add(1) == 2 && lose.CompareAndSwap(true, false) {
			f.ServeHTTP(httptest.NewRecorder(), r)
			w.Write([]byte("响应丢失"))
			return true
		}
		return false
	}, true)
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "1.1.1.1"}, 202)
	awaitDNSStage(t, c, g, panel.EntryUnconfirmed)
	beforeWrites, beforeReads := fake.Writes(), reads.Load()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitDNSStage(t, c, g, panel.EntryWritten)
	if fake.Writes() != beforeWrites || reads.Load() != beforeReads+1 {
		t.Fatalf("重启未只读回未确认记录：写 %d→%d，读 %d→%d", beforeWrites, fake.Writes(), beforeReads, reads.Load())
	}
}

func TestAllPublishDuplicateAddressWithdrawalFinishesAtProvider(t *testing.T) {
	resolver := clusterharness.NewDNSResolver()
	resolver.Set(entryDomain, entryDoH, "8.8.8.8")
	h, c, fake, g, a, b := allHTTPRigWith(t, nil, true, func(d *panel.Dependencies) { d.DNSWatchLookup = resolver.Lookup })
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": entryDoH}}}, 200)
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "8.8.8.8"}, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	c.Do("DELETE", "/api/entry-groups/"+g+"/members/"+b, nil, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	h.Clock.Advance(time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		pub := publication(c, g)
		records := pub["action"].(map[string]any)["records"].([]any)
		r := records[0].(map[string]any)
		if r["propagation_status"] == "已生效" && r["propagation_note"] == "该地址仍由其他成员发布" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("同地址摘除仍等待地址消失：%v", pub)
		}
		h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	if fake.Record("example.com", "101").Enabled || !fake.Record("example.com", "100").Enabled {
		t.Fatal("同地址摘除修改了其他成员")
	}
}

func TestAllPublishPausedMemberCanLeaveBeforeFirstPublish(t *testing.T) {
	_, c, fake, g, a, b := allHTTPRig(t, nil)
	c.Do("DELETE", "/api/entry-groups/"+g+"/members/"+b, nil, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	deadline := time.Now().Add(5 * time.Second)
	for len(publication(c, g)["members"].([]any)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("暂停成员未移出")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if fake.Writes() != 0 || publication(c, g)["first_published"] == true || fake.Record("example.com", "100").Enabled {
		t.Fatal("移出暂停成员触发首次发布")
	}
	publishAll(c, g, map[string]string{a: "8.8.8.8"}, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
}

func TestAllPublishHealthChangesBeforeFirstPublishNeverWriteDNS(t *testing.T) {
	h, c, fake, account, legacy := dnsBrowser(t)
	first := memberOf(c, legacy)
	second := addNode(c, "首发前异常与恢复")
	id := second["id"].(string)
	g := allGroup(c, first, id)
	r := fake.Record("example.com", "100")
	original := r
	r.ID = "101"
	fake.Set("example.com", r)
	bindMember(c, g, first, account, "100", 200)
	bindMember(c, g, id, account, "101", 200)
	n := h.StartNode(second["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(v map[string]any) bool { return v["connected"] == false })
	h.Clock.Advance(91 * time.Second)
	awaitNode(t, c, id, func(v map[string]any) bool { return v["status"] == "offline" })
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	for range 35 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(3 * time.Millisecond)
	}
	if fake.Writes() != 0 || fake.Record("example.com", "100") != original || fake.Record("example.com", "101") != r || publication(c, g)["first_published"] == true {
		t.Fatal("首次发布前的成员异常或恢复触发了 DNS 写入")
	}
}

func TestAllPublishMoreThan32AddressesCanPublishAndResolve(t *testing.T) {
	resolver := clusterharness.NewDNSResolver()
	h, c, fake, g, a, b := allHTTPRigWith(t, nil, true, func(d *panel.Dependencies) { d.DNSWatchLookup = resolver.Lookup })
	members := []string{a, b}
	ips := []string{"11.0.0.1", "11.0.0.2"}
	addresses := map[string]string{a: ips[0], b: ips[1]}
	account := c.Do("GET", memberBindingPath(g, a), nil, 200)["account_id"].(string)
	for i := 2; i < 33; i++ {
		n := addNode(c, fmt.Sprintf("成员%d", i))
		id := n["id"].(string)
		h.StartNode(n["installation_ticket"].(string))
		awaitNode(t, c, id, connected)
		members = append(members, id)
		ip := fmt.Sprintf("11.0.0.%d", i+1)
		ips = append(ips, ip)
		addresses[id] = ip
		r := fake.Record("example.com", "100")
		r.ID = fmt.Sprintf("%d", 200+i)
		fake.Set("example.com", r)
	}
	c.Do("PUT", "/api/entry-groups/"+g, map[string]any{"name": "33个成员", "members": members}, 200)
	for i := 2; i < 33; i++ {
		bindMember(c, g, members[i], account, fmt.Sprintf("%d", 200+i), 200)
	}
	resolver.Set(entryDomain, entryDoH, ips...)
	resolver.Set(entryDomain, "223.5.5.5:53", ips...)
	checker := addNode(c, "国内解析检测点")
	checkerID := checker["id"].(string)
	c.Do("PUT", "/api/nodes/"+checkerID+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	h.StartNode(checker["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) { d.DNSWatchLookup = resolver.Lookup })
	awaitNode(t, c, checkerID, connected)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": entryDoH}, {"kind": "dns", "address": "223.5.5.5:53"}}}, 200)
	publishAll(c, g, addresses, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	w := entryWatch(c, g)
	if len(w["expected_ips"].([]any)) != 33 {
		t.Fatalf("丢失集合地址：%v", w)
	}
	watchResult(t, c, w["id"].(string), func(r map[string]any) bool {
		if r["status"] != "正常" {
			return false
		}
		for _, raw := range r["paths"].([]any) {
			p := raw.(map[string]any)
			if p["kind"] == "dns" && len(p["ips"].([]any)) == 33 {
				return true
			}
		}
		return false
	})
}

func TestAllPublishRestoreAdoptsRecordStatesAndAllowsUnknownPausedAddress(t *testing.T) {
	fake := clusterharness.NewDNSPod()
	fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 60, Enabled: false})
	r := fake.Record("example.com", "100")
	r.ID = "101"
	r.Value = "4.2.2.2"
	fake.Set("example.com", r)
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = fake })
	c := h.Client()
	setup(c)
	login(c)
	first, second := addNode(c, "在线入口"), addNode(c, "未知地址入口")
	a, b := first["id"].(string), second["id"].(string)
	na := h.StartNode(first["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return "8.8.8.8", nil }
	})
	awaitNode(t, c, a, connected)
	g := allGroup(c, a, b)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest123", "secret_key": "testSecretKey"}, 201)["id"].(string)
	bindMember(c, g, a, account, "100", 200)
	bindMember(c, g, b, account, "101", 200)
	publishAll(c, g, map[string]string{a: "8.8.8.8"}, 202)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	nb := h.StartNode(second["installation_ticket"].(string))
	awaitNode(t, c, b, connected)
	backup := filepath.Join(h.Dir, "before.db")
	if err := h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	r = fake.Record("example.com", "100")
	r.Enabled = false
	fake.Set("example.com", r)
	na.Disconnect()
	nb.Disconnect()
	if err := h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	na.Restore()
	nb.Restore()
	na.Restart()
	nb.Restart()
	awaitRevision(t, c, a, 0)
	awaitRevision(t, c, b, 0)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	recovery := c.Do("GET", "/api/recovery", nil, 200)
	for _, raw := range recovery["records"].([]any) {
		row := raw.(map[string]any)
		if row["verified"] != true {
			t.Fatalf("合法暂停成员未放行：%v", row)
		}
	}
	pub := publication(c, g)
	for _, raw := range pub["members"].([]any) {
		row := raw.(map[string]any)
		if row["expected_enabled"] != false {
			t.Fatalf("恢复沿用旧状态：%v", row)
		}
	}
	if entryWatch(c, g)["expected_version"] != pub["desired_version"] || len(entryWatch(c, g)["expected_ips"].([]any)) != 0 {
		t.Fatal("恢复未重建集合与监控版本")
	}
	replacement := fake.Record("example.com", "101")
	replacement.ID = "102"
	fake.Set("example.com", replacement)
	bindMember(c, g, b, account, "102", 200)
	awaitDNSStage(t, c, g, panel.EntryWritten)
	writes := fake.Writes()
	for i := 0; i < 35; i++ {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if fake.Writes() != writes {
		t.Fatal("恢复后按旧备份状态写回")
	}
}

func TestAllPublishRejectedRecordStillReadsBackManualRepair(t *testing.T) {
	h, c, fake, g, a, b := allHTTPRigWith(t, func(w http.ResponseWriter, r *http.Request, _ *clusterharness.DNSPod) bool {
		if r.Form.Get("Action") == "ModifyRecordStatus" && r.Form.Get("RecordId") == "101" {
			w.Write([]byte(`{"Response":{"Error":{"Code":"FailedOperation.DomainIsLocked"}}}`))
			return true
		}
		return false
	}, true, func(d *panel.Dependencies) {
		d.DNSWatchLookup = func(context.Context, string, dnswatch.Resolver) ([]string, error) {
			return []string{"8.8.8.8", "1.1.1.1"}, nil
		}
	})
	publishAll(c, g, map[string]string{a: "8.8.8.8", b: "1.1.1.1"}, 202)
	awaitDNSStage(t, c, g, panel.EntryPartial)
	r := fake.Record("example.com", "101")
	r.Enabled = true
	fake.Set("example.com", r)
	writes := fake.Writes()
	for i := 0; i < 35; i++ {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(3 * time.Millisecond)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		pub := publication(c, g)
		var repaired bool
		for _, raw := range pub["members"].([]any) {
			m := raw.(map[string]any)
			if m["node_id"] == b {
				repaired = m["last_error"] == "" && m["record"].(map[string]any)["current_enabled"] == true
			}
		}
		if repaired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("明确拒绝后停止读回，服务商人工修复没有被采用")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	if len(entryWatch(c, g)["expected_ips"].([]any)) != 2 {
		t.Fatal("采用服务商人工修复后未更新已发布集合")
	}
	watchResult(t, c, entryWatch(c, g)["id"].(string), func(r map[string]any) bool { return r["status"] == "正常" && r["propagation_status"] == "已生效" })
	if fake.Writes() != writes {
		t.Fatal("明确拒绝被自动重试")
	}
}
