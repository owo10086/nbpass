package clusterharness_test

import (
	"context"
	"fmt"
	"nbpass/internal/clusterharness"
	"nbpass/internal/dnswatch"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 若解析监控或解析器设置缺少持久化、域名校验或管理员门禁，此场景失败。
func TestDNSWatchManagementAndResolverSettings(t *testing.T) {
	h, c := nodeBrowser(t)
	h.Client().Do("GET", "/api/dns-watches", nil, 401)
	settings := map[string]any{"resolvers": []map[string]string{{"kind": "dns", "address": "223.5.5.5:53"}, {"kind": "doh", "address": "https://dns.example/dns-query"}}}
	c.Do("PUT", "/api/settings/dns-watch", settings, 200)
	c.Do("POST", "/api/dns-watches", map[string]any{"domain": "https://bad.example"}, 400)
	created := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "Example.COM.", "expected_ips": []string{"8.8.8.8"}}, 201)
	id := created["id"].(string)
	row := c.Do("GET", "/api/dns-watches/"+id, nil, 200)
	if row["domain"] != "example.com" || row["coverage_label"] != "国内覆盖缺失" {
		t.Fatalf("监控状态错误: %v", row)
	}
	c.Do("POST", "/api/dns-watches", map[string]any{"domain": "example.com"}, 409)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	row = c.Do("GET", "/api/dns-watches/"+id, nil, 200)
	if row["expected_version"] != float64(1) {
		t.Fatalf("期望丢失: %v", row)
	}
	saved := c.Do("GET", "/api/settings/dns-watch", nil, 200)
	if len(saved["resolvers"].([]any)) != 2 {
		t.Fatalf("解析器丢失: %v", saved)
	}
	c.Do("PUT", "/api/dns-watches/"+id, map[string]any{"expected_ips": []string{}}, 200)
	c.Do("DELETE", "/api/dns-watches/"+id, nil, 204)
	c.Do("GET", "/api/dns-watches/"+id, nil, 404)
}

func watchResult(t *testing.T, c *clusterharness.Client, id string, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row := c.Do("GET", "/api/dns-watches/"+id, nil, 200)
		if predicate(row) {
			return row
		}
		time.Sleep(time.Millisecond * 5)
	}
	t.Fatalf("解析监控未达到预期: %v", c.Do("GET", "/api/dns-watches/"+id, nil, 200))
	return nil
}
func sampledPaths(row map[string]any, n int) bool {
	paths := row["paths"].([]any)
	if len(paths) != n {
		return false
	}
	for _, p := range paths {
		if p.(map[string]any)["sampled_at"] == nil || p.(map[string]any)["status"] == "等待采样" {
			return false
		}
	}
	return true
}

// 快进后持续驱动调度用的假时钟，直到后台真正开始下一轮；不以真实等待替代业务时间。
func nextWatchRound(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, old float64, n int) map[string]any {
	t.Helper()
	for range 30 {
		if c.Do("GET", "/api/dns-watches/"+id, nil, 200)["round"].(float64) > old {
			break
		}
		h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	return watchResult(t, c, id, func(row map[string]any) bool { return row["round"].(float64) > old && sampledPaths(row, n) })
}
func advanceWatch(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string) map[string]any {
	t.Helper()
	round := c.Do("GET", "/api/dns-watches/"+id, nil, 200)["round"].(float64)
	for range 30 {
		before := c.Do("GET", "/api/nodes", nil, 200)["nodes"].([]any)
		h.Clock.Advance(10 * time.Second)
		if len(before) > 0 {
			nodeID := before[0].(map[string]any)["id"].(string)
			last := before[0].(map[string]any)["last_seen_at"]
			awaitNode(t, c, nodeID, func(r map[string]any) bool { return r["last_seen_at"] != last })
		} else {
			time.Sleep(2 * time.Millisecond)
		}
	}
	return nextWatchRound(t, h, c, id, round, 2)
}

// 若把不同路径混计、未达到三次就告警，或任何 DNS 差异触发写入，此场景失败。
func TestDNSWatchPoisoningRequiresThreeSamePathSamplesAndNeverWrites(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	provider := clusterharness.NewDNSPod()
	telegram := &fakeTelegram{}
	fake.Set("watch.example", "223.5.5.5:53", "9.9.9.9")
	fake.Set("watch.example", "https://dns.example/dns-query", "8.8.8.8")
	h, c := telegramBrowser(t, telegram, func(d *panel.Dependencies) { d.DNSWatchLookup = fake.Lookup; d.DNSProvider = provider })
	created := addNode(c, "国内检测点")
	nid := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+nid+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) { d.DNSWatchLookup = fake.Lookup })
	awaitNode(t, c, nid, connected)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "dns", "address": "223.5.5.5:53"}, {"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "watch.example", "expected_ips": []string{"8.8.8.8"}}, 201)["id"].(string)
	row := watchResult(t, c, id, func(r map[string]any) bool { return sampledPaths(r, 2) })
	if row["status"] == "疑似污染" {
		t.Fatal("第一次不符即污染")
	}
	row = advanceWatch(t, h, c, id)
	if row["status"] == "疑似污染" {
		t.Fatal("第二次不符即污染")
	}
	row = advanceWatch(t, h, c, id)
	if row["status"] != "疑似污染" {
		t.Fatalf("第三次未判疑似污染: %v", row)
	}
	awaitEvents(t, h, c, "?type=dns_watch_poisoned", 1)
	deadline := time.Now().Add(time.Second * 3)
	for time.Now().Before(deadline) {
		telegram.mu.Lock()
		sent := len(telegram.delivered) > 0
		telegram.mu.Unlock()
		if sent {
			break
		}
		time.Sleep(time.Millisecond * 5)
	}
	telegram.mu.Lock()
	defer telegram.mu.Unlock()
	if len(telegram.delivered) == 0 || !strings.Contains(telegram.delivered[0].Text, "疑似污染") {
		t.Fatalf("缺少通知: %v", telegram.delivered)
	}
	if provider.Writes() != 0 {
		t.Fatal("只读解析监控触发了 DNS 写入")
	}
}

// 若 DoH 故障被当成污染或没有国内检测点时伪称已覆盖，此场景失败。
func TestDNSWatchMissingCoverageAndDoHFailure(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSWatchLookup = fake.Lookup })
	c := h.Client()
	setup(c)
	login(c)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "missing.example"}, 201)["id"].(string)
	row := watchResult(t, c, id, func(r map[string]any) bool { return r["status"] == "采样失败" })
	if row["coverage_label"] != "国内覆盖缺失" {
		t.Fatalf("虚假覆盖: %v", row)
	}
	awaitEvents(t, h, c, "?type=dns_watch_failed", 1)
}

// 期望更新、解析失败必须中断连续异常，不能沿用旧版本证据。
func TestDNSWatchExpectedVersionAndFailuresResetEvidence(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	fake.Set("version.example", "223.5.5.5:53", "9.9.9.9")
	fake.Set("version.example", "https://dns.example/dns-query", "8.8.8.8")
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSWatchLookup = fake.Lookup })
	c := h.Client()
	setup(c)
	login(c)
	created := addNode(c, "国内检测点")
	nid := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+nid+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) { d.DNSWatchLookup = fake.Lookup })
	awaitNode(t, c, nid, connected)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "dns", "address": "223.5.5.5:53"}, {"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "version.example", "expected_ips": []string{"8.8.8.8"}}, 201)["id"].(string)
	watchResult(t, c, id, func(r map[string]any) bool { return sampledPaths(r, 2) })
	advanceWatch(t, h, c, id)
	fake.Set("version.example", "https://dns.example/dns-query", "1.1.1.1")
	c.Do("PUT", "/api/dns-watches/"+id, map[string]any{"expected_ips": []string{"1.1.1.1"}}, 200)
	row := watchResult(t, c, id, func(r map[string]any) bool { return r["expected_version"] == float64(2) && sampledPaths(r, 2) })
	if row["status"] == "疑似污染" {
		t.Fatal("旧期望的异常被沿用")
	}
	fake.Fail("version.example", "223.5.5.5:53")
	row = advanceWatch(t, h, c, id)
	if row["status"] != "采样失败" {
		t.Fatalf("解析失败未保留未知: %v", row)
	}
	fake.Set("version.example", "223.5.5.5:53", "9.9.9.9")
	for range 2 {
		row = advanceWatch(t, h, c, id)
		if row["status"] == "疑似污染" {
			t.Fatal("失败没有中断连续异常")
		}
	}
	row = advanceWatch(t, h, c, id)
	if row["status"] != "疑似污染" {
		t.Fatalf("新期望第三次异常未告警: %v", row)
	}
}

// 变化按路径比较成功地址集合；顺序、重复和失败不能造成虚假变化，所有成功 IP 跨重启累计。
func TestDNSWatchHistoryCountsSetsAndSurvivesRestart(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	domain := "history.example"
	address := "https://dns.example/dns-query"
	fake.Set(domain, address, "8.8.8.8", "1.1.1.1", "8.8.8.8")
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSWatchLookup = fake.Lookup })
	c := h.Client()
	setup(c)
	login(c)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": address}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": domain}, 201)["id"].(string)
	row := watchResult(t, c, id, func(r map[string]any) bool { return sampledPaths(r, 1) })
	if row["change_count"] != float64(0) || row["different_ip_count"] != float64(2) {
		t.Fatalf("初始统计: %v", row)
	}
	sample := func() map[string]any {
		old := c.Do("GET", "/api/dns-watches/"+id, nil, 200)["round"].(float64)
		h.Clock.Advance(5 * time.Minute)
		return nextWatchRound(t, h, c, id, old, 1)
	}
	fake.Set(domain, address, "1.1.1.1", "8.8.8.8")
	row = sample()
	if row["change_count"] != float64(0) {
		t.Fatal("地址顺序被记作变化")
	}
	fake.Set(domain, address, "9.9.9.9")
	row = sample()
	if row["change_count"] != float64(1) || row["different_ip_count"] != float64(3) {
		t.Fatalf("变化统计: %v", row)
	}
	fake.Fail(domain, address)
	row = sample()
	if row["change_count"] != float64(1) || row["status"] != "采样失败" {
		t.Fatalf("失败变成地址变化: %v", row)
	}
	fake.Set(domain, address, "9.9.9.9")
	row = sample()
	if row["change_count"] != float64(1) {
		t.Fatal("恢复原集合被记作变化")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	row = c.Do("GET", "/api/dns-watches/"+id, nil, 200)
	if row["change_count"] != float64(1) || row["different_ip_count"] != float64(3) || len(row["history"].([]any)) != 2 {
		t.Fatalf("历史重建丢失: %v", row)
	}
}

// 在途旧期望和旧解析器结果不能进入新版本或重新建立旧路径。
func TestDNSWatchIgnoresLateDoHAfterExpectedAndResolverChange(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var count atomic.Int64
	lookup := func(ctx context.Context, domain string, r dnswatch.Resolver) ([]string, error) {
		if count.Add(1) == 1 {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []string{"9.9.9.9"}, nil
		}
		return []string{"1.1.1.1"}, nil
	}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSWatchLookup = lookup })
	c := h.Client()
	setup(c)
	login(c)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://old.example/dns-query"}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "late.example", "expected_ips": []string{"8.8.8.8"}}, 201)["id"].(string)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("未开始采样")
	}
	c.Do("PUT", "/api/dns-watches/"+id, map[string]any{"expected_ips": []string{"1.1.1.1"}}, 200)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://new.example/dns-query"}}}, 200)
	close(release)
	row := watchResult(t, c, id, func(r map[string]any) bool { return sampledPaths(r, 1) && r["status"] == "正常" })
	if row["different_ip_count"] != float64(1) || row["paths"].([]any)[0].(map[string]any)["resolver"] != "https://new.example/dns-query" {
		t.Fatalf("迟到样本污染了新版本: %v", row)
	}
}

// 存在国内节点但设置未使用国内 DNS 时，不能显示国内已有覆盖。
func TestDNSWatchCoverageRequiresConfiguredDomesticPath(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	fake.Set("coverage.example", "https://dns.example/dns-query", "8.8.8.8")
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSWatchLookup = fake.Lookup })
	c := h.Client()
	setup(c)
	login(c)
	created := addNode(c, "国内检测点")
	id := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) { d.DNSWatchLookup = fake.Lookup })
	awaitNode(t, c, id, connected)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	watch := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "coverage.example"}, 201)["id"].(string)
	row := watchResult(t, c, watch, func(r map[string]any) bool { return sampledPaths(r, 1) })
	if row["coverage_label"] != "国内覆盖缺失" {
		t.Fatalf("没有国内解析路径却展示覆盖: %v", row)
	}
}

// 一个公共 DNS 卡住时，其他健康解析路径仍应完成；排队时间不能冒充查询失败。
func TestDNSWatchSlowResolverDoesNotBlockOtherDomesticPaths(t *testing.T) {
	started := make(chan struct{}, 1)
	lookup := func(ctx context.Context, _ string, r dnswatch.Resolver) ([]string, error) {
		if r.Address == "223.5.5.5:53" {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []string{"8.8.8.8"}, nil
	}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSWatchLookup = lookup })
	c := h.Client()
	setup(c)
	login(c)
	created := addNode(c, "国内检测点")
	nid := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+nid+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) { d.DNSWatchLookup = lookup })
	awaitNode(t, c, nid, connected)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "dns", "address": "223.5.5.5:53"}, {"kind": "dns", "address": "119.29.29.29:53"}, {"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "parallel.example", "expected_ips": []string{"8.8.8.8"}}, 201)["id"].(string)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("没有开始慢解析")
	}
	watchResult(t, c, id, func(row map[string]any) bool {
		for _, item := range row["paths"].([]any) {
			p := item.(map[string]any)
			if p["resolver"] == "119.29.29.29:53" && p["status"] == "符合期望" {
				return true
			}
		}
		return false
	})
}

// 通知队列写失败不能阻止事件和后续采样，存储恢复后应补投而不重复事件。
func TestDNSWatchNotificationStorageFailureDoesNotBlockSampling(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	telegram := &fakeTelegram{}
	disk := &interruptedPersistence{}
	h, c := telegramBrowser(t, telegram, func(d *panel.Dependencies) {
		disk.base = d.Persistence
		d.Persistence = disk
		d.DNSWatchLookup = fake.Lookup
	})
	if _, err := disk.db.Exec("CREATE TRIGGER fail_dns_notice BEFORE INSERT ON telegram_deliveries BEGIN SELECT RAISE(ABORT,'测试通知存储故障'); END"); err != nil {
		t.Fatal(err)
	}
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "notice.example"}, 201)["id"].(string)
	watchResult(t, c, id, func(r map[string]any) bool { return r["status"] == "采样失败" })
	awaitEvents(t, h, c, "?type=dns_watch_failed", 1)
	fake.Set("notice.example", "https://dns.example/dns-query", "8.8.8.8")
	for range 30 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(time.Millisecond * 2)
	}
	nextWatchRound(t, h, c, id, 1, 1)
	watchResult(t, c, id, func(r map[string]any) bool { return r["different_ip_count"] == float64(1) })
	if _, err := disk.db.Exec("DROP TRIGGER fail_dns_notice"); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		h.Clock.Advance(time.Second)
		time.Sleep(time.Millisecond * 5)
	}
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	if len(eventRows(c, "?type=dns_watch_failed")) != 1 {
		t.Fatal("补投导致事件重复")
	}
}

// 通知存储持续失败超过单批上限时，仍必须记录所有域名的异常事件。
func TestDNSWatchNotificationBacklogDoesNotStarveEvents(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	disk := &interruptedPersistence{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		disk.base = d.Persistence
		d.Persistence = disk
		d.DNSWatchLookup = fake.Lookup
	})
	c := h.Client()
	setup(c)
	login(c)
	if _, err := disk.db.Exec("CREATE TRIGGER fail_dns_notice BEFORE INSERT ON telegram_deliveries BEGIN SELECT RAISE(ABORT,'测试通知存储故障'); END"); err != nil {
		t.Fatal(err)
	}
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	for i := range 35 {
		c.Do("POST", "/api/dns-watches", map[string]any{"domain": fmt.Sprintf("notice-%d.example", i)}, 201)
	}
	awaitEvents(t, h, c, "?type=dns_watch_failed", 35)
}
