package clusterharness_test

import (
	"context"
	"fmt"
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
	"nbpass/internal/rotation"
)

type blockedEntryRig struct {
	h        *clusterharness.Harness
	c        *clusterharness.Client
	dns      *clusterharness.DNSPod
	telegram *fakeTelegram
	ids      [2]string
	sites    [2]*domesticSites
	group    string
	calls    atomic.Int32
	ip       [2]atomic.Value
}

// 第一次被阻塞的随机读取正常完成；后续动作分配失败，防止合法重试遮蔽过期回滚。
type blockedExpiryRandom struct {
	offlineActionRandom
	exhausted atomic.Bool
}

func (r *blockedExpiryRandom) Read(b []byte) (int, error) {
	if r.exhausted.Load() {
		return 0, io.ErrUnexpectedEOF
	}
	blocked := r.block.Load()
	n, err := r.offlineActionRandom.Read(b)
	if blocked {
		r.exhausted.Store(true)
	}
	return n, err
}

func newBlockedEntryRig(t *testing.T, candidate bool, options ...func(*panel.Dependencies)) *blockedEntryRig {
	t.Helper()
	f := &blockedEntryRig{dns: clusterharness.NewDNSPod(), telegram: &fakeTelegram{}}
	f.dns.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	f.h, f.c = telegramBrowser(t, f.telegram, append([]func(*panel.Dependencies){func(d *panel.Dependencies) { d.DNSProvider = f.dns }}, options...)...)
	settings := f.c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["interval_seconds"], settings["jitter_seconds"] = 30, 0
	settings["round_timeout_seconds"] = 20
	f.c.Do("PUT", "/api/settings/self-check", settings, 204)
	a, b := addNode(f.c, "首选入口"), addNode(f.c, "候选入口")
	f.ids = [2]string{a["id"].(string), b["id"].(string)}
	f.group = f.c.Do("POST", "/api/entry-groups", map[string]any{"name": "被墙入口切换", "members": f.ids[:]}, 201)["id"].(string)
	for i, created := range []map[string]any{a, b} {
		ip := []string{"8.8.8.8", "1.1.1.1"}[i]
		f.ip[i].Store(ip)
		f.sites[i] = &domesticSites{}
		f.sites[i].mode.Store(2)
		f.h.StartNode(created["installation_ticket"].(string), f.sites[i].dependencies, func(_ *node.Config, d *node.Dependencies) {
			d.PublicIPv4 = func(context.Context) (string, error) { return f.ip[i].Load().(string), nil }
			d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, d.PublicIPv4}
			d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{f.ip[i].Load().(string)}, nil }
			d.RotationHTTP = func(context.Context, rotation.Config, rotation.Variables) error { f.calls.Add(1); return nil }
			d.Forwarding.Listen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
		})
		awaitNode(t, f.c, f.ids[i], connected)
		if i == 0 || candidate {
			f.c.Do("PUT", "/api/nodes/"+f.ids[i]+"/self-check", map[string]bool{"enabled": true}, 204)
			awaitSelfCheck(t, f.c, f.ids[i], 1, "normal", 0)
		}
		path := "/api/nodes/" + f.ids[i] + "/ip-rotation"
		f.c.Do("PUT", path, map[string]any{"url": "https://vendor.example/change", "method": "POST"}, 200)
		f.c.Do("PUT", path+"/automatic", map[string]bool{"enabled": true}, 200)
	}
	rule := f.c.Do("POST", "/api/rules", directBody(f.group, 29001, "127.0.0.1", 12345), 201)
	awaitRule(t, f.c, rule["id"].(string), "正常")
	account := f.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "12345", "token": "secret"}, 201)["id"].(string)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/managed-record", binding(account), 200)
	firstPublish(f.c, f.group, f.ids[0], 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "switch_entry"}, 200)
	return f
}

func (f *blockedEntryRig) samples(t *testing.T, count int) {
	t.Helper()
	for range count {
		// 控制空闲不代表自测 goroutine 已运行；到期时等待真实轮次上报后才再快进。
		// 否则高负载下会跳过自测定时器，而后续冻结假时钟无法等到下一轮。
		due := map[string]float64{}
		for _, id := range f.ids {
			v := f.c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
			rounds := v["rounds"].([]any)
			if v["enabled"] != true || len(rounds) == 0 {
				continue
			}
			last := rounds[0].(map[string]any)
			started, err := time.Parse(time.RFC3339Nano, last["started_at"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if !started.Add(30 * time.Second).After(f.h.Clock.Now().Add(10 * time.Second)) {
				due[id] = last["sequence"].(float64)
			}
		}
		sampleEntryState(t, f.h, f.c, f.group, f.ids[0])
		for id, sequence := range due {
			deadline := time.Now().Add(5 * time.Second)
			for {
				v := f.c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
				if rounds := v["rounds"].([]any); len(rounds) > 0 && rounds[0].(map[string]any)["sequence"].(float64) > sequence {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("到期自测未上报: %s seq=%v %v", id, sequence, v)
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
}

func (f *blockedEntryRig) block(t *testing.T, index int) {
	t.Helper()
	f.sites[index].mode.Store(0)
	f.samples(t, 9)
	awaitSelfCheck(t, f.c, f.ids[index], 4, "blocked", 3)
	f.samples(t, 1)
}

func (f *blockedEntryRig) assertPublication(t *testing.T, active string, version, writes int, ip string) {
	t.Helper()
	pub := publication(f.c, f.group)
	if pub["active_node_id"] != active || pub["desired_version"] != float64(version) || f.dns.Writes() != writes || f.dns.Record("example.com", "100").Value != ip {
		t.Fatalf("入口发布与 DNS 不符: %v，写入 %d 次，记录 %s", pub, f.dns.Writes(), f.dns.Record("example.com", "100").Value)
	}
	for _, id := range f.ids {
		if view := f.c.Do("GET", "/api/nodes/"+id+"/ip-rotation", nil, 200); view["action"] != nil {
			t.Fatalf("策略②隐式发起换 IP: %v", view)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("策略②调用了换 IP 接口")
	}
}

func (f *blockedEntryRig) awaitPublication(t *testing.T, active string, version int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pub := publication(f.c, f.group)
		if pub["active_node_id"] == active && pub["desired_version"] == float64(version) && pub["action"].(map[string]any)["stage"] == "服务商已写入" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("入口发布未推进到版本 %d: %v", version, pub)
		}
		time.Sleep(time.Millisecond)
	}
}

// 捕获策略未开放、绕过协调器、过早回切或策略②误申请换 IP。
func TestEntryBlockedSwitchAndPreferredRecovery(t *testing.T) {
	f := newBlockedEntryRig(t, true)
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	f.assertPublication(t, f.ids[1], 2, 2, "1.1.1.1")
	if publication(f.c, f.group)["auto_failback_paused"] != false {
		t.Fatal("自动故障切换误暂停回切")
	}
	f.sites[0].mode.Store(2)
	f.samples(t, 2)
	f.samples(t, 5)
	f.assertPublication(t, f.ids[1], 2, 2, "1.1.1.1")
	f.samples(t, 6)
	f.awaitPublication(t, f.ids[0], 3)
	f.assertPublication(t, f.ids[0], 3, 3, "8.8.8.8")
}

// 无候选通知须持久去重；重启和持续被墙不能改变记录或退化为换 IP。
func TestEntryBlockedNoCandidateNotifiesWithoutRotation(t *testing.T) {
	f := newBlockedEntryRig(t, false)
	f.block(t, 0)
	f.assertPublication(t, f.ids[0], 1, 1, "8.8.8.8")
	waitOfflineEvents(t, f.c, "?type=entry_blocked_waiting", 1)
	f.samples(t, 2)
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitControlIdle(t, f.h)
	f.h.Clock.Advance(time.Second)
	awaitNode(t, f.c, f.ids[0], connected)
	f.samples(t, 2)
	f.assertPublication(t, f.ids[0], 1, 1, "8.8.8.8")
	if n := len(f.c.Do("GET", "/api/events?type=entry_blocked_waiting", nil, 200)["events"].([]any)); n != 1 {
		t.Fatalf("重复等待通知: %d", n)
	}
	waitTelegramStatus(t, f.c, func(v map[string]any) bool { return v["pending"] == float64(0) && v["reachability"] == "reachable" })
	_, messages := f.telegram.snapshot()
	found := false
	for _, m := range messages {
		found = found || strings.Contains(m.Text, "没有就绪候选") && strings.Contains(m.Text, "不发起换 IP")
	}
	if !found {
		t.Fatal("缺少策略②无候选通知")
	}
	// 后续成员就绪后可继续切换，不用重置策略或再判一次被墙。
	f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, f.c, f.ids[1], 1, "normal", 0)
	f.samples(t, 1)
	f.awaitPublication(t, f.ids[1], 2)
	f.assertPublication(t, f.ids[1], 2, 2, "1.1.1.1")
}

func TestEntryBlockedInactiveMemberIsExcludedWithoutSwitch(t *testing.T) {
	f := newBlockedEntryRig(t, true)
	f.block(t, 1)
	f.assertPublication(t, f.ids[0], 1, 1, "8.8.8.8")
	state := entryState(t, f.c, f.group, f.ids[1])
	if state["candidate"] != false || !hasEntryReason(state, "candidate_reasons", "blocked") {
		t.Fatalf("被墙成员仍是候选: %v", state)
	}
	if n := len(f.c.Do("GET", "/api/events?type=entry_blocked_switch", nil, 200)["events"].([]any)); n != 0 {
		t.Fatal("非活动成员被墙触发切换")
	}
}

func TestEntryBlockedSwitchBypassesPauseAndResidence(t *testing.T) {
	f := newBlockedEntryRig(t, true)
	manualSwitch(f.c, f.group, f.ids[1], "1.1.1.1", 1, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	f.block(t, 1)
	f.awaitPublication(t, f.ids[0], 3)
	f.assertPublication(t, f.ids[0], 3, 3, "8.8.8.8")
	if publication(f.c, f.group)["auto_failback_paused"] != true {
		t.Fatal("故障切换改变手动暂停")
	}
}

func TestEntryBlockedMaintenanceAndDisabledPolicyPreventSwitch(t *testing.T) {
	for _, reason := range []string{"maintenance", "disabled"} {
		t.Run(reason, func(t *testing.T) {
			f := newBlockedEntryRig(t, true)
			if reason == "maintenance" {
				f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/maintenance", map[string]bool{"enabled": true}, 204)
			} else {
				f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "disabled"}, 200)
			}
			f.block(t, 0)
			f.assertPublication(t, f.ids[0], 1, 1, "8.8.8.8")
		})
	}
}

func TestEntryBlockedPolicySettingsPersistAndRejectUnsupported(t *testing.T) {
	f := newBlockedEntryRig(t, true)
	path := "/api/entry-groups/" + f.group + "/blocked-policy"
	f.c.Do("PUT", path, map[string]string{"policy": "switch_then_rotate"}, 400)
	f.c.Do("PUT", path, map[string]string{"policy": "invalid"}, 400)
	f.h.Client().Do("PUT", path, map[string]string{"policy": "disabled"}, 401)
	f.c.Do("PUT", "/api/entry-groups/missing/blocked-policy", map[string]string{"policy": "switch_entry"}, 404)
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	if v := f.c.Do("GET", path, nil, 200); v["policy"] != "switch_entry" {
		t.Fatalf("策略未保存: %v", v)
	}
}

func TestEntryBlockedActionStorageFailureDoesNotWriteDNS(t *testing.T) {
	faults := &clusterharness.PersistenceFaults{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { faults.Base = d.Persistence; d.Persistence = faults })
	if err := faults.FailEntryActions("INSERT"); err != nil {
		t.Fatal(err)
	}
	f.block(t, 0)
	f.assertPublication(t, f.ids[0], 1, 1, "8.8.8.8")
	if err := faults.FailEntryActions(""); err != nil {
		t.Fatal(err)
	}
	f.samples(t, 1)
	f.awaitPublication(t, f.ids[1], 2)
	f.assertPublication(t, f.ids[1], 2, 2, "1.1.1.1")
}

func TestEntryBlockedNotificationFailureDoesNotBlockDNSAndResumes(t *testing.T) {
	persistence := &interruptedPersistence{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { persistence.base = d.Persistence; d.Persistence = persistence })
	if _, err := persistence.db.Exec(`CREATE TRIGGER fail_blocked_notifications BEFORE INSERT ON telegram_deliveries WHEN NEW.incident_id LIKE 'entry-blocked:%' BEGIN SELECT RAISE(ABORT,'queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	f.assertPublication(t, f.ids[1], 2, 2, "1.1.1.1")
	f.samples(t, 1)
	waitOfflineEvents(t, f.c, "?type=entry_blocked_switch", 1)
	if _, err := persistence.db.Exec("DROP TRIGGER fail_blocked_notifications"); err != nil {
		t.Fatal(err)
	}
	f.samples(t, 1)
	waitTelegramStatus(t, f.c, func(v map[string]any) bool { return v["pending"] == float64(0) && v["reachability"] == "reachable" })
	_, messages := f.telegram.snapshot()
	found := false
	for _, m := range messages {
		found = found || strings.Contains(m.Text, "活动入口被墙，入口切换已写入 DNSPod")
	}
	if !found {
		deadline := time.Now().Add(5 * time.Second)
		for !found && time.Now().Before(deadline) {
			_, messages = f.telegram.snapshot()
			for _, m := range messages {
				found = found || strings.Contains(m.Text, "活动入口被墙，入口切换已写入 DNSPod")
			}
			time.Sleep(time.Millisecond)
		}
		if !found {
			t.Fatal("已完成切换的通知没有续投")
		}
	}
	f.samples(t, 2)
	if n := len(f.c.Do("GET", "/api/events?type=entry_blocked_switch", nil, 200)["events"].([]any)); n != 1 {
		t.Fatalf("续投重复事件: %d", n)
	}
}

func TestEntryBlockedCandidateExpiryDuringSubmissionRollsBack(t *testing.T) {
	random := &blockedExpiryRandom{offlineActionRandom: offlineActionRandom{entered: make(chan struct{}), release: make(chan struct{})}}
	var once sync.Once
	defer once.Do(func() { close(random.release) })
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { random.base = d.Random; d.Random = random })
	// 先在维护期收集被墙证据，避免触发动作；解除后阻塞首次动作 ID 分配。
	f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/maintenance", map[string]bool{"enabled": true}, 204)
	f.block(t, 0)
	awaitControlIdle(t, f.h)
	random.block.Store(true)
	f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/maintenance", map[string]bool{"enabled": false}, 204)
	f.h.Clock.Advance(time.Second)
	waitSignal(t, random.entered)
	f.h.Clock.Advance(11 * time.Second)
	once.Do(func() { close(random.release) })
	f.assertPublication(t, f.ids[0], 1, 1, "8.8.8.8")
	actions := f.c.Do("GET", "/api/entry-groups/"+f.group+"/actions", nil, 200)["actions"].([]any)
	if len(actions) != 1 {
		t.Fatal("过期样本提交留下动作")
	}
}

// 手动换 IP 不属于策略②申请；新地址仍异常时须能重新建立本组切换事故。
func TestEntryBlockedChangedAddressRequiresFreshEvidenceAndCanSwitch(t *testing.T) {
	f := newBlockedEntryRig(t, false)
	f.block(t, 0)
	path := "/api/nodes/" + f.ids[0] + "/ip-rotation"
	f.c.Do("POST", path+"/actions", nil, 202)
	awaitRotation(t, f.c, path, "observing")
	f.ip[0].Store("4.2.2.2")
	f.samples(t, 6)
	awaitRotation(t, f.c, path, "changed")
	f.awaitPublication(t, f.ids[0], 2)
	f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, f.c, f.ids[1], 1, "normal", 0)
	f.samples(t, 1)
	if pub := publication(f.c, f.group); pub["active_node_id"] != f.ids[0] {
		t.Fatalf("旧 IP 的被墙证据驱动了新地址切换: %v", pub)
	}
	// 当前地址连续三轮异常后允许切换，即使 node_self_check 从未离开 blocked。
	f.samples(t, 9)
	f.awaitPublication(t, f.ids[1], 3)
	if f.calls.Load() != 1 || f.dns.Record("example.com", "100").Value != "1.1.1.1" {
		t.Fatal("策略②未切换或重复换 IP")
	}
}

func TestEntryBlockedChangedAddressDoesNotCombineSelfCheckConfigs(t *testing.T) {
	f := newBlockedEntryRig(t, false)
	f.block(t, 0)
	path := "/api/nodes/" + f.ids[0] + "/ip-rotation"
	f.c.Do("POST", path+"/actions", nil, 202)
	awaitRotation(t, f.c, path, "observing")
	f.ip[0].Store("4.2.2.2")
	f.samples(t, 6)
	awaitRotation(t, f.c, path, "changed")
	f.awaitPublication(t, f.ids[0], 2)
	for i := range 3 {
		before := len(f.c.Do("GET", "/api/nodes/"+f.ids[0]+"/self-check", nil, 200)["rounds"].([]any))
		settings := f.c.Do("GET", "/api/settings/self-check", nil, 200)
		settings["sites"] = []string{fmt.Sprintf("one-%d.test", i), "two.test", "three.test", "four.test"}
		f.c.Do("PUT", "/api/settings/self-check", settings, 204)
		awaitSelfCheck(t, f.c, f.ids[0], before+1, "blocked", 1)
	}
	f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, f.c, f.ids[1], 1, "normal", 0)
	f.samples(t, 1)
	// 已有 blocked 状态不能把不同配置下的三个异常轮拼成当前地址的新事故。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pub := publication(f.c, f.group); pub["active_node_id"] != f.ids[0] {
			t.Fatalf("不同配置异常轮被拼成连续三轮: %v", pub)
		}
		time.Sleep(time.Millisecond)
	}
	f.samples(t, 9)
	f.awaitPublication(t, f.ids[1], 3)
}
