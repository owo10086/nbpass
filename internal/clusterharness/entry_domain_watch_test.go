package clusterharness_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/dnswatch"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

const entryDomain = "entry.example.com"
const entryDoH = "https://dns.example/dns-query"

type entryWatchRig struct {
	h                    *clusterharness.Harness
	c                    *clusterharness.Client
	dns                  *clusterharness.DNSPod
	resolver             *clusterharness.DNSResolver
	telegram             *fakeTelegram
	a, b, group, account string
}

func entryWatchSetup(t *testing.T, domestic bool, lookup func(context.Context, string, dnswatch.Resolver) ([]string, error), nodeOptions ...func(*node.Dependencies)) *entryWatchRig {
	t.Helper()
	f := &entryWatchRig{dns: clusterharness.NewDNSPod(), resolver: clusterharness.NewDNSResolver(), telegram: &fakeTelegram{}}
	f.dns.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	f.resolver.Set(entryDomain, "223.5.5.5:53", "8.8.8.8")
	f.resolver.Set(entryDomain, entryDoH, "8.8.8.8")
	if lookup == nil {
		lookup = f.resolver.Lookup
	}
	f.h, f.c = telegramBrowser(t, f.telegram, func(d *panel.Dependencies) { d.DNSProvider = f.dns; d.DNSWatchLookup = lookup })
	a := addNode(f.c, "入口 A")
	f.a = a["id"].(string)
	f.b = addNode(f.c, "入口 B")["id"].(string)
	if domestic {
		f.c.Do("PUT", "/api/nodes/"+f.a+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
		f.h.StartNode(a["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
			d.DNSWatchLookup = f.resolver.Lookup
			for _, option := range nodeOptions {
				option(d)
			}
		})
		awaitNode(t, f.c, f.a, connected)
	}
	f.group = f.c.Do("POST", "/api/entry-groups", map[string]any{"name": "传播观察", "members": []string{f.a, f.b}}, 201)["id"].(string)
	f.account = f.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "12345", "token": "secret"}, 201)["id"].(string)
	f.c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "dns", "address": "223.5.5.5:53"}, {"kind": "doh", "address": entryDoH}}}, 200)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/managed-record", binding(f.account), 200)
	return f
}

func entryWatch(c *clusterharness.Client, group string) map[string]any {
	return c.Do("GET", "/api/entry-groups/"+group+"/dns-watch", nil, 200)
}

func (f *entryWatchRig) advance(t *testing.T, seconds int) {
	t.Helper()
	for range seconds / 10 {
		if connectedNode := nodeRow(t, f.c, f.a); connectedNode["connected"] == true {
			advanceCheckerClock(t, f.h, f.c, f.a, 10)
		} else {
			f.h.Clock.Advance(10 * time.Second)
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func (f *entryWatchRig) sample(t *testing.T, paths int) map[string]any {
	t.Helper()
	row := entryWatch(f.c, f.group)
	old := row["round"].(float64)
	f.advance(t, 30)
	return nextWatchRound(t, f.h, f.c, row["id"].(string), old, paths)
}

// 删除自动纳管、忽略任一已采样路径或允许手改期望都会使本场景失败。
func TestEntryDomainWatchAutomaticOwnershipAndTwoMatches(t *testing.T) {
	f := entryWatchSetup(t, true, nil)
	row := entryWatch(f.c, f.group)
	if row["domain"] != entryDomain || len(row["expected_ips"].([]any)) != 0 {
		t.Fatalf("未发布的自动监控错误: %v", row)
	}
	id := row["id"].(string)
	f.c.Do("PUT", "/api/dns-watches/"+id, map[string]any{"expected_ips": []string{"4.2.2.2"}}, 409)
	f.c.Do("DELETE", "/api/dns-watches/"+id, nil, 409)
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	row = watchResult(t, f.c, id, func(r map[string]any) bool { return r["expected_version"] == float64(1) && sampledPaths(r, 2) })
	if row["propagation_status"] != "传播中" {
		t.Fatalf("一次采样即生效: %v", row)
	}
	// 国内连续命中不能掩盖已采样的 DoH 不符。
	f.resolver.Set(entryDomain, entryDoH, "4.2.2.2")
	row = f.sample(t, 2)
	if row["propagation_status"] != "传播中" {
		t.Fatal("忽略了有采样的 DoH 路径")
	}
	f.resolver.Set(entryDomain, entryDoH, "8.8.8.8")
	row = f.sample(t, 2)
	if row["propagation_status"] != "传播中" {
		t.Fatal("未连续两次命中就生效")
	}
	row = f.sample(t, 2)
	row = watchResult(t, f.c, id, func(r map[string]any) bool { return r["propagation_status"] == "已生效" })
	if row["propagation_status"] != "已生效" {
		t.Fatalf("连续两次未生效: %v", row)
	}
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	if entryWatch(f.c, f.group)["propagation_status"] != "已生效" {
		t.Fatal("重启丢失传播状态")
	}
}

// 传播观察独立于协调器：真实离线确认仍能提交并完成下一次故障切换。
func TestEntryDomainWatchOfflineSwitchInvalidatesObservation(t *testing.T) {
	resolver := clusterharness.NewDNSResolver()
	resolver.Set(entryDomain, entryDoH, "9.9.9.9")
	f := offlineEntry(t, true, func(d *panel.Dependencies) { d.DNSWatchLookup = resolver.Lookup })
	f.c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": entryDoH}}}, 200)
	row := entryWatch(f.c, f.group)
	if row["propagation_status"] != "传播中" {
		t.Fatalf("未进入传播观察: %v", row)
	}
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.disconnect(t)
	awaitOfflineCheck(t, f, "unreachable")
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	row = entryWatch(f.c, f.group)
	if publication(f.c, f.group)["active_node_id"] != f.b || row["expected_version"] != float64(2) || row["expected_ips"].([]any)[0] != "1.1.1.1" || row["propagation_status"] != "传播中" || f.dns.Writes() != 2 {
		t.Fatalf("传播观察阻塞故障切换或旧观察未失效: %v", row)
	}
	// 新切换旧 TTL 为 60 秒，观察窗口应取十分钟。
	end, _ := time.Parse(time.RFC3339Nano, row["observation_deadline"].(string))
	written, _ := time.Parse(time.RFC3339Nano, publication(f.c, f.group)["written_at"].(string))
	if end.Sub(written) != 10*time.Minute {
		t.Fatal("未采用最短十分钟观察窗口")
	}
}

// 迟到的服务商核对也携带期望版本，不能在新切换后提交旧入口写回。
func TestEntryDomainWatchLateProviderAuditCannotWriteOldExpectation(t *testing.T) {
	f := entryWatchSetup(t, false, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var block atomic.Bool
	f.dns.Read = func(ctx context.Context) error {
		if block.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	block.Store(true)
	f.advance(t, 30)
	waitSignal(t, entered)
	manualSwitch(f.c, f.group, f.b, "1.1.1.1", 1, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	once.Do(func() { close(release) })
	f.advance(t, 30)
	if f.dns.Writes() != 2 || publication(f.c, f.group)["desired_version"] != float64(2) || f.dns.Record("example.com", "100").Value != "1.1.1.1" {
		t.Fatal("迟到核对写回旧期望")
	}
}

func TestEntryDomainWatchObservationIsCappedAtOneHour(t *testing.T) {
	f := entryWatchSetup(t, false, nil)
	r := f.dns.Record("example.com", "100")
	r.TTL = 7200
	f.dns.Set("example.com", r)
	// 协调器必须采用写前实际 TTL，而非绑定时保存的 600 秒。
	firstPublish(f.c, f.group, f.a, 202)
	a := awaitDNSStage(t, f.c, f.group, "服务商已写入")
	row := entryWatch(f.c, f.group)
	written, _ := time.Parse(time.RFC3339Nano, a["written_at"].(string))
	end, _ := time.Parse(time.RFC3339Nano, row["observation_deadline"].(string))
	if end.Sub(written) != time.Hour {
		t.Fatalf("未封顶一小时: %v", row)
	}
}

// 连续切换后仍有尚未到期的更早缓存，不能只记最后一次切换的旧 IP。
func TestEntryDomainWatchRapidSwitchKeepsEarlierCacheGrace(t *testing.T) {
	f := entryWatchSetup(t, true, nil)
	f.resolver.Set(entryDomain, "223.5.5.5:53", "9.9.9.9")
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	manualSwitch(f.c, f.group, f.b, "1.1.1.1", 1, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	f.resolver.Set(entryDomain, entryDoH, "1.1.1.1")
	id := entryWatch(f.c, f.group)["id"].(string)
	watchResult(t, f.c, id, func(r map[string]any) bool { return sampledPaths(r, 2) && r["expected_version"] == float64(2) })
	for range 4 {
		row := f.sample(t, 2)
		if row["status"] == "疑似污染" || row["propagation_status"] == "已生效" {
			t.Fatalf("早期缓存宽限丢失或被当成生效证据: %v", row)
		}
		for _, path := range row["paths"].([]any) {
			p := path.(map[string]any)
			if p["node_id"] != "" && p["consecutive_mismatches"] != float64(0) {
				t.Fatalf("早期缓存误计异常: %v", p)
			}
		}
	}
	if f.dns.Writes() != 2 {
		t.Fatal("早期递归缓存触发额外写回")
	}
}

// 30 秒采样轮次不能删掉同在 30 秒到期的漏报任务，必须保留失败历史并通知。
func TestEntryDomainWatchMissingSampleIsExpiredBeforeNextRound(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	f := entryWatchSetup(t, true, nil, func(d *node.Dependencies) {
		d.DNSWatchLookup = func(context.Context, string, dnswatch.Resolver) ([]string, error) {
			<-gate
			return []string{"8.8.8.8"}, nil
		}
	})
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	row := entryWatch(f.c, f.group)
	id := row["id"].(string)
	watchResult(t, f.c, id, func(r map[string]any) bool { return len(r["paths"].([]any)) == 2 })
	f.advance(t, 30)
	watchResult(t, f.c, id, func(r map[string]any) bool {
		history, _ := r["history"].([]any)
		for _, h := range history {
			item := h.(map[string]any)
			if strings.HasPrefix(item["path_id"].(string), f.a+"|") && item["status"] == "采样失败" {
				return true
			}
		}
		return false
	})
	awaitEvents(t, f.h, f.c, "?type=dns_watch_failed", 1)
}

// 同名的不同受管记录仍各自拥有活动入口、期望版本和解析观察。
func TestEntryDomainWatchSameDomainGroupsKeepIndependentExpectations(t *testing.T) {
	f := entryWatchSetup(t, false, nil)
	group := entryGroup(f.c, f.b)["id"].(string)
	f.dns.Set("example.com", dnsprovider.Record{ID: "102", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "1.1.1.1", TTL: 60, Enabled: true})
	body := binding(f.account)
	body["record_id"] = "102"
	f.c.Do("PUT", "/api/entry-groups/"+group+"/managed-record", body, 200)
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	f.c.Do("POST", "/api/entry-groups/"+group+"/publish", map[string]any{"active_node_id": f.b, "ipv4": "1.1.1.1", "confirm_inbound": true}, 202)
	awaitDNSStage(t, f.c, group, "服务商已写入")
	one, two := entryWatch(f.c, f.group), entryWatch(f.c, group)
	if one["id"] == two["id"] || one["expected_ips"].([]any)[0] != "8.8.8.8" || two["expected_ips"].([]any)[0] != "1.1.1.1" {
		t.Fatalf("同名组互相覆盖监控: %v / %v", one, two)
	}
	f.c.Do("DELETE", "/api/entry-groups/"+group, nil, 204)
	if entryWatch(f.c, f.group)["expected_ips"].([]any)[0] != "8.8.8.8" {
		t.Fatal("删除同名组影响另一组监控")
	}
}

// 只准 DNSPod 实际记录偏差发起写回；旧 TTL 缓存及 AAAA 都不属于写入目标。
func TestEntryDomainWatchOldCacheAndAAAAStayReadOnlyButProviderDriftWritesBack(t *testing.T) {
	f := entryWatchSetup(t, true, nil)
	other := dnsprovider.Record{ID: "101", Name: "entry", Type: "AAAA", Line: "默认", LineID: "0", Value: "2001:db8::1", TTL: 300, Enabled: true}
	f.dns.Set("example.com", other)
	f.resolver.Set(entryDomain, "223.5.5.5:53", "9.9.9.9")
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	id := entryWatch(f.c, f.group)["id"].(string)
	watchResult(t, f.c, id, func(r map[string]any) bool { return sampledPaths(r, 2) })
	for range 4 {
		row := f.sample(t, 2)
		if row["status"] == "疑似污染" {
			t.Fatalf("旧 TTL 内误报污染: %v", row)
		}
		for _, path := range row["paths"].([]any) {
			p := path.(map[string]any)
			if p["node_id"] != "" && (p["status"] != "传播中" || p["consecutive_mismatches"] != float64(0)) {
				t.Fatalf("缓存被计异常: %v", p)
			}
		}
	}
	if f.dns.Writes() != 1 {
		t.Fatal("递归差异触发写回")
	}
	changed := f.dns.Record("example.com", "100")
	changed.Value = "4.2.2.2"
	f.dns.Set("example.com", changed)
	f.sample(t, 2)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && publication(f.c, f.group)["desired_version"] != float64(2) {
		f.advance(t, 10)
	}
	pub := publication(f.c, f.group)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	if pub["active_node_id"] != f.a || pub["action"].(map[string]any)["kind"] != "writeback" || f.dns.Record("example.com", "100").Value != "8.8.8.8" || f.dns.Writes() != 2 {
		t.Fatalf("偏差未由协调器写回: %v", pub)
	}
	if f.dns.Record("example.com", "101") != other {
		t.Fatal("修改了残留 AAAA")
	}
}

// 没有国内样本不可仅凭 DoH 宣布生效；到期通知一次且迟到成功不改终态。
func TestEntryDomainWatchDeadlineWithoutDomesticCoverage(t *testing.T) {
	f := entryWatchSetup(t, false, nil)
	firstPublish(f.c, f.group, f.a, 202)
	a := awaitDNSStage(t, f.c, f.group, "服务商已写入")
	row := entryWatch(f.c, f.group)
	id := row["id"].(string)
	row = watchResult(t, f.c, id, func(r map[string]any) bool { return sampledPaths(r, 1) })
	start, _ := time.Parse(time.RFC3339Nano, a["written_at"].(string))
	end, err := time.Parse(time.RFC3339Nano, row["observation_deadline"].(string))
	if err != nil || end.Sub(start) != 20*time.Minute {
		t.Fatalf("没有沿用旧 TTL 的两倍: %v", row)
	}
	row = f.sample(t, 1)
	if row["propagation_status"] != "传播中" || row["coverage_label"] != "国内覆盖缺失" {
		t.Fatalf("无国内覆盖却已生效: %v", row)
	}
	f.h.Clock.Advance(end.Sub(f.h.Clock.Now()) - time.Second)
	time.Sleep(20 * time.Millisecond)
	if entryWatch(f.c, f.group)["propagation_status"] != "传播中" {
		t.Fatal("提前结束观察")
	}
	f.h.Clock.Advance(time.Second)
	watchResult(t, f.c, id, func(r map[string]any) bool { return r["propagation_status"] == "传播未确认" })
	awaitEvents(t, f.h, f.c, "?type=entry_propagation_unconfirmed", 1)
	f.advance(t, 30)
	waitTelegramStatus(t, f.c, func(s map[string]any) bool { return s["pending"] == float64(0) })
	f.telegram.mu.Lock()
	defer f.telegram.mu.Unlock()
	if len(f.telegram.delivered) != 1 || !strings.Contains(f.telegram.delivered[0].Text, "传播未确认") {
		t.Fatalf("传播超时通知错误: %v", f.telegram.delivered)
	}
	if f.dns.Writes() != 1 || publication(f.c, f.group)["desired_version"] != float64(1) {
		t.Fatal("传播失败触发了入口动作")
	}
}

// 旧版本的真实 DoH 调用晚于新动作返回时，既不能确认新版本也不能恢复旧入口。
func TestEntryDomainWatchLateSampleCannotRestoreOldEntry(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var block atomic.Bool
	lookup := func(ctx context.Context, _ string, _ dnswatch.Resolver) ([]string, error) {
		if block.CompareAndSwap(true, false) {
			close(entered)
			select {
			case <-release:
				return []string{"8.8.8.8"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []string{"1.1.1.1"}, nil
	}
	f := entryWatchSetup(t, false, lookup)
	block.Store(true)
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	waitSignal(t, entered)
	manualSwitch(f.c, f.group, f.b, "1.1.1.1", 1, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	row := entryWatch(f.c, f.group)
	watchResult(t, f.c, row["id"].(string), func(r map[string]any) bool { return r["expected_version"] == float64(2) && sampledPaths(r, 1) })
	once.Do(func() { close(release) })
	f.sample(t, 1)
	row = entryWatch(f.c, f.group)
	for _, path := range row["paths"].([]any) {
		p := path.(map[string]any)
		if p["expected_version"] != float64(2) || p["ips"].([]any)[0] != "1.1.1.1" {
			t.Fatalf("旧采样污染了新版本: %v", p)
		}
	}
	if f.dns.Writes() != 2 || f.dns.Record("example.com", "100").Value != "1.1.1.1" || publication(f.c, f.group)["active_node_id"] != f.b {
		t.Fatal("旧结果改回了旧入口")
	}
}
