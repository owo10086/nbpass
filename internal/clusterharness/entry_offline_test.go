package clusterharness_test

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

type offlineEntryRig struct {
	h           *clusterharness.Harness
	c           *clusterharness.Client
	dns         *clusterharness.DNSPod
	telegram    *fakeTelegram
	a, b, group string
	active      *clusterharness.Node
}

// 随机源属于既有外部接缝；在动作ID分配阶段制造调度停顿，不替换业务逻辑。
type offlineActionRandom struct {
	base    io.Reader
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (r *offlineActionRandom) Read(data []byte) (int, error) {
	if r.block.CompareAndSwap(true, false) {
		close(r.entered)
		<-r.release
	}
	return r.base.Read(data)
}

func TestEntryOfflineCandidateExpiryDuringSubmissionDoesNotWriteDNS(t *testing.T) {
	random := &offlineActionRandom{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(random.release) })
	f := offlineEntry(t, true, func(d *panel.Dependencies) { random.base = d.Random; d.Random = random })
	f.c.Do("PUT", "/api/nodes/"+f.b+"/self-check", map[string]bool{"enabled": false}, 204)
	awaitRevision(t, f.c, f.b, 0)
	expires, err := time.Parse(time.RFC3339Nano, entryState(t, f.c, f.group, f.b)["domestic_evidence"].(map[string]any)["expires_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	checkerGate := make(chan struct{})
	var checkerOnce sync.Once
	defer checkerOnce.Do(func() { close(checkerGate) })
	startChecker(t, f.h, f.c, 0, 1, checkerGate)
	f.advance(t, int(expires.Sub(f.h.Clock.Now()).Seconds())-100)
	f.disconnect(t)
	awaitOfflineCheck(t, f, "pending")
	awaitControlIdle(t, f.h)
	random.block.Store(true)
	checkerOnce.Do(func() { close(checkerGate) })
	entered := false
	for range 5 {
		// 此时故意阻塞面板动作，不能再等待控制空闲（动作持有控制锁）。
		// 每步只推进一秒，给检测报告与后台扫描完成的机会。
		f.h.Clock.Advance(time.Second)
		select {
		case <-random.entered:
			entered = true
		case <-time.After(time.Second):
		}
		if entered {
			break
		}
	}
	if !entered {
		t.Fatal("没有进入动作ID分配")
	}
	// 候选检查成功后，提交仍未完成，恰好跨过国内证据有效期。
	f.h.Clock.Advance(expires.Sub(f.h.Clock.Now()))
	once.Do(func() { close(random.release) })
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "no_candidate")
	if f.dns.Writes() != 1 || publication(f.c, f.group)["active_node_id"] != f.a {
		t.Fatal("提交期间过期的候选仍被发布")
	}
}

func offlineEntry(t *testing.T, candidate bool, options ...func(*panel.Dependencies)) *offlineEntryRig {
	t.Helper()
	f := &offlineEntryRig{dns: clusterharness.NewDNSPod(), telegram: &fakeTelegram{}}
	f.dns.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	f.h, f.c = telegramBrowser(t, f.telegram, append([]func(*panel.Dependencies){func(d *panel.Dependencies) { d.DNSProvider = f.dns }}, options...)...)
	a, b := addNode(f.c, "活动入口"), addNode(f.c, "候选入口")
	f.a, f.b = a["id"].(string), b["id"].(string)
	f.group = f.c.Do("POST", "/api/entry-groups", map[string]any{"name": "离线容灾", "members": []string{f.a, f.b}}, 201)["id"].(string)
	sites := &domesticSites{}
	sites.mode.Store(2)
	for i, created := range []map[string]any{a, b} {
		ip := []string{"8.8.8.8", "1.1.1.1"}[i]
		n := f.h.StartNode(created["installation_ticket"].(string), sites.dependencies, func(_ *node.Config, d *node.Dependencies) {
			d.PublicIPv4 = func(context.Context) (string, error) { return ip, nil }
			d.Forwarding.Listen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
		})
		if i == 0 {
			f.active = n
		}
		awaitNode(t, f.c, created["id"].(string), connected)
	}
	rule := f.c.Do("POST", "/api/rules", directBody(f.group, 29001, "127.0.0.1", 12345), 201)
	awaitRule(t, f.c, rule["id"].(string), "正常")
	if candidate {
		f.c.Do("PUT", "/api/nodes/"+f.b+"/self-check", map[string]bool{"enabled": true}, 204)
		awaitSelfCheck(t, f.c, f.b, 1, "normal", 0)
		if row := sampleEntryState(t, f.h, f.c, f.group, f.b); row["candidate"] != true {
			t.Fatalf("测试候选没有就绪或缺少当前配置国内证据: %v", row)
		}
	}
	account := f.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "12345", "token": "secret"}, 201)["id"].(string)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/managed-record", binding(account), 200)
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	return f
}

func TestEntryOfflineQueueStorageFailureDoesNotBlockSwitchOrEvent(t *testing.T) {
	persistence := &interruptedPersistence{}
	f := offlineEntry(t, true, func(d *panel.Dependencies) { persistence.base = d.Persistence; d.Persistence = persistence })
	if _, err := persistence.db.Exec(`CREATE TRIGGER fail_entry_notifications BEFORE INSERT ON telegram_deliveries BEGIN SELECT RAISE(ABORT,'queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.disconnect(t)
	awaitOfflineCheck(t, f, "unreachable")
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	f.advance(t, 1)
	waitOfflineEvents(t, f.c, "?type=entry_failover", 1)
	if _, err := persistence.db.Exec("DROP TRIGGER fail_entry_notifications"); err != nil {
		t.Fatal(err)
	}
	f.advance(t, 1)
	waitTelegramStatus(t, f.c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	if f.dns.Writes() != 2 {
		t.Fatal("通知队列恢复重复切换")
	}
}

func TestEntryOfflineLateFailureCannotOverrideCurrentState(t *testing.T) {
	for _, change := range []string{"manual", "recovery", "maintenance", "candidate", "revision"} {
		t.Run(change, func(t *testing.T) {
			f := offlineEntry(t, true)
			gate := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			defer release()
			started := make(chan struct{}, 1)
			startChecker(t, f.h, f.c, 0, 1, gate, func(_ *node.Config, d *node.Dependencies) {
				dial := d.CheckerDial
				d.CheckerDial = func(ctx context.Context, address string) (net.Conn, error) {
					if address == "8.8.8.8:18080" {
						select {
						case started <- struct{}{}:
						default:
						}
					}
					return dial(ctx, address)
				}
			})
			f.disconnect(t)
			awaitOfflineCheck(t, f, "pending")
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("检测点尚未执行旧目标检测")
			}
			switch change {
			case "manual":
				manualSwitch(f.c, f.group, f.b, "1.1.1.1", 1, 202)
				awaitDNSStage(t, f.c, f.group, "服务商已写入")
			case "recovery":
				f.active.Restore()
				f.active.Restart()
				awaitNode(t, f.c, f.a, connected)
			case "maintenance":
				f.c.Do("PUT", "/api/nodes/"+f.a+"/maintenance", map[string]bool{"enabled": true}, 204)
			case "candidate":
				// HTTP展示仍保留上一轮成功采样；提交必须重新核对。
				f.c.Do("PUT", "/api/nodes/"+f.b+"/maintenance", map[string]bool{"enabled": true}, 204)
			case "revision":
				f.c.Do("PUT", "/api/nodes/"+f.a+"/listener-ports", map[string]int{"tunnel_listen_port": 18443, "check_port": 18081}, 200)
			}
			release()
			if change != "candidate" {
				awaitOfflineCheck(t, f, "invalidated")
			} else {
				awaitOfflineCheck(t, f, "unreachable")
				f.advance(t, 1)
				awaitOfflineCheck(t, f, "no_candidate")
			}
			f.advance(t, 1)
			want := 1
			if change == "manual" {
				want = 2
			}
			if f.dns.Writes() != want {
				t.Fatal("旧离线证据产生额外切换")
			}
		})
	}
}

func (f *offlineEntryRig) advance(t *testing.T, seconds int) {
	t.Helper()
	for range seconds / 10 {
		advanceCheckerClock(t, f.h, f.c, f.b, 10)
	}
	if seconds%10 != 0 {
		awaitControlIdle(t, f.h)
		f.h.Clock.Advance(time.Duration(seconds%10) * time.Second)
	}
}

func (f *offlineEntryRig) disconnect(t *testing.T) {
	t.Helper()
	f.active.Disconnect()
	awaitNode(t, f.c, f.a, func(v map[string]any) bool { return v["connected"] == false })
	f.advance(t, 91)
	awaitNode(t, f.c, f.a, func(v map[string]any) bool { return v["status"] == "offline" })
}

func awaitOfflineCheck(t *testing.T, f *offlineEntryRig, outcome string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var view map[string]any
	for time.Now().Before(deadline) {
		view = f.c.Do("GET", "/api/entry-groups/"+f.group+"/offline-checks", nil, 200)
		for _, raw := range view["checks"].([]any) {
			row := raw.(map[string]any)
			if row["outcome"] == outcome {
				return row
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("离线确认未达到 %s: %v", outcome, view)
	return nil
}

func TestEntryOfflineConfirmedFailureSwitchesAndNotifies(t *testing.T) {
	f := offlineEntry(t, true)
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.disconnect(t)
	awaitOfflineCheck(t, f, "unreachable")
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	if p := publication(f.c, f.group); p["active_node_id"] != f.b || p["desired_version"] != float64(2) || p["action"].(map[string]any)["kind"] != "switch" || f.dns.Record("example.com", "100").Value != "1.1.1.1" {
		t.Fatalf("未切到候选: %v", p)
	}
	f.advance(t, 1)
	waitOfflineEvents(t, f.c, "?type=entry_failover", 1)
	waitTelegramStatus(t, f.c, func(s map[string]any) bool { return s["pending"] == float64(0) && s["reachability"] == "reachable" })
	_, messages := f.telegram.snapshot()
	found := false
	for _, m := range messages {
		found = found || strings.Contains(m.Text, "入口切换")
	}
	if !found {
		t.Fatal("没有入口切换通知")
	}
	if rounds := f.c.Do("GET", "/api/nodes/"+f.a+"/self-check", nil, 200)["rounds"].([]any); len(rounds) != 0 {
		t.Fatal("离线确认混入自测轮次")
	}
	f.advance(t, 20)
	if f.dns.Writes() != 2 {
		t.Fatal("同一离线重复切换")
	}
}

func TestEntryOfflineReachableFromCheckerDoesNotSwitch(t *testing.T) {
	f := offlineEntry(t, true)
	startChecker(t, f.h, f.c, 0, 0, nil)
	startChecker(t, f.h, f.c, 1, 1, nil)
	f.disconnect(t)
	awaitOfflineCheck(t, f, "reachable")
	f.advance(t, 1)
	if publication(f.c, f.group)["active_node_id"] != f.a || f.dns.Writes() != 1 {
		t.Fatal("检测点可达仍发生切换")
	}
}

func TestEntryOfflineNoCoverageOrNoCandidatePreservesRecordAndNotifies(t *testing.T) {
	for _, tc := range []struct {
		name      string
		candidate bool
		modes     []int
		outcome   string
	}{
		{"没有国内检测点", true, nil, "no_checkers"},
		{"对照站点失败", true, []int{2}, "unknown"},
		{"执行错误", true, []int{3}, "unknown"},
		{"没有候选", false, []int{1}, "no_candidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := offlineEntry(t, tc.candidate)
			for i, mode := range tc.modes {
				startChecker(t, f.h, f.c, i, mode, nil)
			}
			f.disconnect(t)
			if tc.outcome == "no_candidate" {
				awaitOfflineCheck(t, f, "unreachable")
				f.advance(t, 1)
			}
			awaitOfflineCheck(t, f, tc.outcome)
			f.advance(t, 1)
			waitTelegramStatus(t, f.c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
			if p := publication(f.c, f.group); p["active_node_id"] != f.a || p["desired_version"] != float64(1) || f.dns.Writes() != 1 || f.dns.Record("example.com", "100").Value != "8.8.8.8" {
				t.Fatalf("没有证据或候选时改动解析: %v", p)
			}
		})
	}
}

func TestEntryOfflineTelegramFailureDoesNotBlockSwitch(t *testing.T) {
	f := offlineEntry(t, true)
	f.telegram.mu.Lock()
	f.telegram.fail = true
	f.telegram.mu.Unlock()
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.disconnect(t)
	awaitOfflineCheck(t, f, "unreachable")
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	f.advance(t, 1)
	waitTelegramStatus(t, f.c, func(s map[string]any) bool {
		return s["reachability"] == "unreachable" && len(s["failures"].([]any)) > 0
	})
	if f.dns.Record("example.com", "100").Value != "1.1.1.1" {
		t.Fatal("Telegram 失败阻塞 DNS")
	}
}

func TestEntryOfflineMissingCheckerResultCannotConfirmFailure(t *testing.T) {
	f := offlineEntry(t, true)
	gate := make(chan struct{})
	defer close(gate)
	startChecker(t, f.h, f.c, 0, 1, nil)
	slow := startChecker(t, f.h, f.c, 1, 0, gate)
	f.disconnect(t)
	check := awaitOfflineCheck(t, f, "pending")
	if len(check["checkers"].([]any)) != 2 {
		t.Fatalf("没有冻结全部检测点: %v", check)
	}
	slow.Disconnect()
	f.advance(t, 60)
	awaitOfflineCheck(t, f, "unknown")
	if f.dns.Writes() != 1 {
		t.Fatal("把缺报当成不可达确认")
	}
}

func TestEntryOfflineHonorsBoundaryAndRestartGrace(t *testing.T) {
	f := offlineEntry(t, true)
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.active.Disconnect()
	awaitNode(t, f.c, f.a, func(v map[string]any) bool { return v["connected"] == false })
	f.advance(t, 90)
	if checks := f.c.Do("GET", "/api/entry-groups/"+f.group+"/offline-checks", nil, 200)["checks"].([]any); len(checks) != 0 || f.dns.Writes() != 1 {
		t.Fatal("恰好90秒提前开始切换确认")
	}
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	// 面板重启后健康节点需按真实重连周期恢复；快进逐秒，不跨控制期限。
	for range 89 {
		awaitControlIdle(t, f.h)
		f.h.Clock.Advance(time.Second)
		// 等候后台一次扫描的HTTP可观察屏障：候选检查时间到达本轮。
		if f.h.Clock.Now().Unix()%10 == 0 {
			awaitNode(t, f.c, f.b, connected)
		}
	}
	if checks := f.c.Do("GET", "/api/entry-groups/"+f.group+"/offline-checks", nil, 200)["checks"].([]any); len(checks) != 0 || f.dns.Writes() != 1 {
		t.Fatal("面板重启90秒宽限内发起确认或切换")
	}
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "unreachable")
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
}

func TestEntryOfflineMaintenanceRequiresFreshObservation(t *testing.T) {
	f := offlineEntry(t, true)
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.c.Do("PUT", "/api/nodes/"+f.a+"/maintenance", map[string]bool{"enabled": true}, 204)
	f.active.Disconnect()
	awaitNode(t, f.c, f.a, func(v map[string]any) bool { return v["connected"] == false })
	f.advance(t, 100)
	if checks := f.c.Do("GET", "/api/entry-groups/"+f.group+"/offline-checks", nil, 200)["checks"].([]any); len(checks) != 0 {
		t.Fatal("维护中发起离线切换")
	}
	f.c.Do("PUT", "/api/nodes/"+f.a+"/maintenance", map[string]bool{"enabled": false}, 204)
	f.advance(t, 90)
	if checks := f.c.Do("GET", "/api/entry-groups/"+f.group+"/offline-checks", nil, 200)["checks"].([]any); len(checks) != 0 {
		t.Fatal("维护结束复用旧离线时长")
	}
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "unreachable")
	f.advance(t, 1)
	awaitOfflineCheck(t, f, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
}
