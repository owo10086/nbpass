package clusterharness_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
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

type failbackRig struct {
	h                  *clusterharness.Harness
	c                  *clusterharness.Client
	dns                *clusterharness.DNSPod
	telegram           *fakeTelegram
	preferred, standby string
	group              string
	nodes              [2]*clusterharness.Node
	ip                 atomic.Value
}

func newFailbackRig(t *testing.T, options ...func(*panel.Dependencies)) *failbackRig {
	t.Helper()
	f := &failbackRig{dns: clusterharness.NewDNSPod(), telegram: &fakeTelegram{}}
	f.ip.Store("8.8.8.8")
	f.dns.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	f.h, f.c = telegramBrowser(t, f.telegram, append([]func(*panel.Dependencies){func(d *panel.Dependencies) { d.DNSProvider = f.dns }}, options...)...)
	a, b := addNode(f.c, "首选入口"), addNode(f.c, "备用入口")
	f.preferred, f.standby = a["id"].(string), b["id"].(string)
	f.group = f.c.Do("POST", "/api/entry-groups", map[string]any{"name": "首选回切", "members": []string{f.preferred, f.standby}}, 201)["id"].(string)
	sites := &domesticSites{}
	sites.mode.Store(2)
	for i, created := range []map[string]any{a, b} {
		f.nodes[i] = f.h.StartNode(created["installation_ticket"].(string), sites.dependencies, func(_ *node.Config, d *node.Dependencies) {
			d.PublicIPv4 = func(context.Context) (string, error) {
				if i == 0 {
					return f.ip.Load().(string), nil
				}
				return "1.1.1.1", nil
			}
			d.Forwarding.Listen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
		})
		awaitNode(t, f.c, created["id"].(string), connected)
	}
	rule := f.c.Do("POST", "/api/rules", directBody(f.group, 29001, "127.0.0.1", 12345), 201)
	awaitRule(t, f.c, rule["id"].(string), "正常")
	account := f.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "testSecretKey"}, 201)["id"].(string)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/managed-record", binding(account), 200)
	firstPublish(f.c, f.group, f.preferred, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	manualSwitch(f.c, f.group, f.standby, "1.1.1.1", 1, 202)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	return f
}

func (f *failbackRig) resume() {
	f.c.Do("POST", "/api/entry-groups/"+f.group+"/resume-failback", map[string]int{"expected_version": 2}, 204)
}

func (f *failbackRig) recoverPreferred(t *testing.T) {
	t.Helper()
	before := f.c.Do("GET", "/api/nodes/"+f.preferred+"/self-check", nil, 200)["rounds"].([]any)
	var sequence float64
	if len(before) > 0 {
		sequence = before[0].(map[string]any)["sequence"].(float64)
	}
	f.c.Do("PUT", "/api/nodes/"+f.preferred+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitRevision(t, f.c, f.preferred, 0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view := f.c.Do("GET", "/api/nodes/"+f.preferred+"/self-check", nil, 200)
		for _, raw := range view["rounds"].([]any) {
			r := raw.(map[string]any)
			if r["result"] == "normal" && r["sequence"].(float64) > sequence {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("首选入口没有当前地址的正常自测")
}

func (f *failbackRig) samples(t *testing.T, count int) {
	t.Helper()
	for range count {
		sampleEntryState(t, f.h, f.c, f.group, f.preferred)
	}
}

func (f *failbackRig) remainsOnStandby(t *testing.T) {
	t.Helper()
	if pub := publication(f.c, f.group); pub["active_node_id"] != f.standby || pub["desired_version"] != float64(2) || f.dns.Writes() != 2 || f.dns.Record("example.com", "100").Value != "1.1.1.1" {
		t.Fatalf("条件未满足就回切: %v，DNS 写入 %d 次", pub, f.dns.Writes())
	}
}

func (f *failbackRig) returnedToPreferred(t *testing.T) {
	t.Helper()
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	pub := publication(f.c, f.group)
	if pub["active_node_id"] != f.preferred || pub["desired_version"] != float64(3) || pub["action"].(map[string]any)["kind"] != "failback" || f.dns.Writes() != 3 || f.dns.Record("example.com", "100").Value != f.ip.Load().(string) {
		t.Fatalf("满足条件没有回切到首选入口: %v，DNS 写入 %d 次", pub, f.dns.Writes())
	}
}

// 捕获用自然流逝替代六个成功样本，以及把驻留起点错误地放在动作提交时。
func TestEntryFailbackRequiresSixChecksAndTwoMinuteResidence(t *testing.T) {
	for _, limit := range []string{"recovery", "residence"} {
		t.Run(limit, func(t *testing.T) {
			f := newFailbackRig(t)
			f.resume()
			if limit == "recovery" {
				f.samples(t, 12)
			}
			f.recoverPreferred(t)
			if limit == "recovery" {
				f.samples(t, 5)
			} else {
				f.samples(t, 11)
			}
			// 重复读取不能制造成功样本。
			for range 10 {
				entryState(t, f.c, f.group, f.preferred)
				f.remainsOnStandby(t)
			}
			f.samples(t, 1)
			f.returnedToPreferred(t)
			f.samples(t, 2)
			if f.dns.Writes() != 3 {
				t.Fatal("重复回切")
			}
		})
	}
}

func TestEntryFailbackResidenceStartsWhenDNSWriteIsConfirmed(t *testing.T) {
	f := newFailbackRig(t)
	// 再手动发布同一个备用入口，延迟这一次写入确认。
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.dns.Write = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// 强制产生外部写入，假服务已执行但响应被延迟。
	r := f.dns.Record("example.com", "100")
	r.Value = "9.9.9.9"
	f.dns.Set("example.com", r)
	manualSwitch(f.c, f.group, f.standby, "1.1.1.1", 2, 202)
	waitSignal(t, entered)
	f.recoverPreferred(t)
	// DNS 请求十秒超时；在期限以内放行，区分提交和确认起点。
	awaitControlIdle(t, f.h)
	f.h.Clock.Advance(9 * time.Second)
	if publication(f.c, f.group)["written_at"] != nil {
		t.Fatal("写入尚未确认就开始驻留")
	}
	once.Do(func() { close(release) })
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	f.c.Do("POST", "/api/entry-groups/"+f.group+"/resume-failback", map[string]int{"expected_version": 3}, 204)
	f.samples(t, 11)
	if pub := publication(f.c, f.group); pub["desired_version"] != float64(3) || pub["active_node_id"] != f.standby {
		t.Fatalf("从提交而非写入确认开始计时: %v", pub)
	}
	// 后续写入不再阻塞。
	f.dns.Write = nil
	f.samples(t, 1)
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	if pub := publication(f.c, f.group); pub["active_node_id"] != f.preferred || pub["desired_version"] != float64(4) {
		t.Fatalf("写入确认满两分钟未回切: %v", pub)
	}
}

func TestEntryFailbackPauseAndResumeUseTheSameRules(t *testing.T) {
	f := newFailbackRig(t)
	f.recoverPreferred(t)
	f.samples(t, 14)
	f.remainsOnStandby(t)
	if publication(f.c, f.group)["auto_failback_paused"] != true {
		t.Fatal("手动维护未暂停回切")
	}
	f.resume()
	f.remainsOnStandby(t)
	f.samples(t, 1)
	f.returnedToPreferred(t)
}

func TestEntryFailbackInterruptedWindowRequiresSixFreshChecks(t *testing.T) {
	for _, interruption := range []string{"failure", "unknown", "expired", "missed", "restart"} {
		t.Run(interruption, func(t *testing.T) {
			f := newFailbackRig(t)
			f.samples(t, 12)
			f.recoverPreferred(t)
			f.samples(t, 5)
			switch interruption {
			case "failure":
				f.c.Do("PUT", "/api/nodes/"+f.preferred+"/maintenance", map[string]bool{"enabled": true}, 204)
				if state := sampleEntryState(t, f.h, f.c, f.group, f.preferred); state["candidate"] != false {
					t.Fatal("维护没有中断候选资格")
				}
				f.c.Do("PUT", "/api/nodes/"+f.preferred+"/maintenance", map[string]bool{"enabled": false}, 204)
			case "unknown":
				f.c.Do("PUT", "/api/nodes/"+f.preferred+"/self-check", map[string]bool{"enabled": false}, 204)
				awaitRevision(t, f.c, f.preferred, 0)
				f.ip.Store("4.2.2.2")
				f.samples(t, 1)
				awaitNode(t, f.c, f.preferred, func(v map[string]any) bool { return v["public_ipv4"] == "4.2.2.2" })
				if state := sampleEntryState(t, f.h, f.c, f.group, f.preferred); state["candidate_status"] != "unknown" {
					t.Fatalf("没有当前IP证据时应未知: %v", state)
				}
				f.recoverPreferred(t)
			case "expired":
				f.c.Do("PUT", "/api/nodes/"+f.preferred+"/self-check", map[string]bool{"enabled": false}, 204)
				f.samples(t, 25)
				if state := entryState(t, f.c, f.group, f.preferred); !hasEntryReason(state, "candidate_reasons", "domestic_evidence_expired") {
					t.Fatalf("证据未过期: %v", state)
				}
				f.recoverPreferred(t)
			case "missed":
				// 只投递一次定时器，不能用流逝的六十秒补成六次检查。
				f.resume()
				awaitControlIdle(t, f.h)
				f.h.Clock.Advance(60 * time.Second)
				deadline := time.Now().Add(5 * time.Second)
				for entryState(t, f.c, f.group, f.preferred)["checked_at"] != f.h.Clock.Now().Format(time.RFC3339Nano) {
					if time.Now().After(deadline) {
						t.Fatal("延迟的候选检查没有完成")
					}
					time.Sleep(time.Millisecond)
				}
				f.remainsOnStandby(t)
				f.samples(t, 4)
				f.remainsOnStandby(t)
				f.samples(t, 1)
				f.returnedToPreferred(t)
				return
			case "restart":
				if err := f.h.Restart(); err != nil {
					t.Fatal(err)
				}
				awaitControlIdle(t, f.h)
				f.h.Clock.Advance(time.Second)
				awaitNode(t, f.c, f.preferred, connected)
			}
			f.resume()
			f.samples(t, 5)
			f.remainsOnStandby(t)
			f.samples(t, 1)
			f.returnedToPreferred(t)
		})
	}
}

func TestEntryFailbackResidenceAndPauseDoNotDelayRealFailure(t *testing.T) {
	f := newFailbackRig(t)
	f.recoverPreferred(t)
	// 仍处手动暂停，备用入口刚开始驻留，便发生真实离线。
	startChecker(t, f.h, f.c, 0, 1, nil, func(_ *node.Config, d *node.Dependencies) {
		base := d.CheckerDial
		d.CheckerDial = func(ctx context.Context, address string) (net.Conn, error) {
			if address == "1.1.1.1:18080" {
				address = "8.8.8.8:18080"
			}
			return base(ctx, address)
		}
	})
	f.nodes[1].Disconnect()
	awaitNode(t, f.c, f.standby, func(v map[string]any) bool { return v["connected"] == false })
	f.samples(t, 9)
	awaitControlIdle(t, f.h)
	f.h.Clock.Advance(time.Second)
	o := &offlineEntryRig{h: f.h, c: f.c, group: f.group}
	awaitOfflineCheck(t, o, "unreachable")
	awaitControlIdle(t, f.h)
	f.h.Clock.Advance(time.Second)
	awaitOfflineCheck(t, o, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	pub := publication(f.c, f.group)
	if pub["active_node_id"] != f.preferred || pub["action"].(map[string]any)["kind"] != "switch" || pub["auto_failback_paused"] != true || f.dns.Writes() != 3 {
		t.Fatalf("真实故障被驻留或暂停阻挡: %v", pub)
	}
}

func TestEntryFailbackNotificationFailureDoesNotBlockDNS(t *testing.T) {
	persistence := &interruptedPersistence{}
	f := newFailbackRig(t, func(d *panel.Dependencies) { persistence.base = d.Persistence; d.Persistence = persistence })
	f.recoverPreferred(t)
	f.samples(t, 12)
	if _, err := persistence.db.Exec(`CREATE TRIGGER fail_failback_notifications BEFORE INSERT ON telegram_deliveries BEGIN SELECT RAISE(ABORT,'queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	f.resume()
	f.samples(t, 1)
	f.returnedToPreferred(t)
	f.samples(t, 1)
	waitOfflineEvents(t, f.c, "?type=entry_failback", 1)
	if _, err := persistence.db.Exec("DROP TRIGGER fail_failback_notifications"); err != nil {
		t.Fatal(err)
	}
	f.samples(t, 1)
	waitTelegramStatus(t, f.c, func(s map[string]any) bool { return s["pending"] == float64(0) && s["reachability"] == "reachable" })
	_, messages := f.telegram.snapshot()
	found := false
	for _, m := range messages {
		found = found || strings.Contains(m.Text, "入口回切")
	}
	if !found {
		t.Fatal("回切通知没有续投")
	}
}

func TestEntryFailbackCandidateExpiryDuringSubmissionRollsBack(t *testing.T) {
	random := &offlineActionRandom{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(random.release) })
	f := newFailbackRig(t, func(d *panel.Dependencies) { random.base = d.Random; d.Random = random })
	f.samples(t, 12)
	f.recoverPreferred(t)
	f.samples(t, 5)
	f.resume()
	awaitControlIdle(t, f.h)
	random.block.Store(true)
	f.h.Clock.Advance(10 * time.Second)
	waitSignal(t, random.entered)
	// 动作ID分配时停顿，六次中的最后一次样本变旧；不得把它提交。
	f.h.Clock.Advance(11 * time.Second)
	once.Do(func() { close(random.release) })
	f.remainsOnStandby(t)
	actions := f.c.Do("GET", "/api/entry-groups/"+f.group+"/actions", nil, 200)["actions"].([]any)
	if len(actions) != 2 {
		t.Fatal("失效提交留下动作或推进期望版本")
	}
}

func TestEntryFailbackPersistenceFailureDoesNotWriteDNS(t *testing.T) {
	faults := &clusterharness.PersistenceFaults{}
	f := newFailbackRig(t, func(d *panel.Dependencies) { faults.Base = d.Persistence; d.Persistence = faults })
	f.recoverPreferred(t)
	f.samples(t, 12)
	if err := faults.FailEntryActions("INSERT"); err != nil {
		t.Fatal(err)
	}
	f.resume()
	f.samples(t, 1)
	f.remainsOnStandby(t)
	if err := faults.FailEntryActions(""); err != nil {
		t.Fatal(err)
	}
	f.samples(t, 5)
	f.remainsOnStandby(t)
	f.samples(t, 1)
	f.returnedToPreferred(t)
}

func TestEntryFailbackRestoredPendingActionDoesNotAnnounceSuccess(t *testing.T) {
	f := newFailbackRig(t)
	f.recoverPreferred(t)
	f.samples(t, 12)
	var unavailable atomic.Bool
	unavailable.Store(true)
	f.dns.Read = func(context.Context) error {
		if unavailable.Load() {
			return errors.New("服务商读回暂不可用")
		}
		return nil
	}
	f.resume()
	f.samples(t, 1)
	if pub := publication(f.c, f.group); pub["desired_version"] != float64(3) || pub["written_at"] != nil {
		t.Fatalf("没有形成待确认回切: %v", pub)
	}
	backup := filepath.Join(f.h.Dir, "pending-failback.db")
	if err := f.h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	for _, n := range f.nodes {
		n.Disconnect()
	}
	unavailable.Store(false)
	if err := f.h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	for i, id := range []string{f.preferred, f.standby} {
		f.nodes[i].Restore()
		f.nodes[i].Restart()
		awaitRevision(t, f.c, id, 0)
	}
	f.c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	f.samples(t, 1)
	if pub := publication(f.c, f.group); pub["active_node_id"] != f.standby || pub["desired_version"] != float64(4) || f.dns.Writes() != 2 {
		t.Fatalf("封存旧动作被重放: %v", pub)
	}
	events := f.c.Do("GET", "/api/events?type=entry_failback", nil, 200)["events"].([]any)
	if len(events) != 0 {
		t.Fatal("未写入的旧回切被宣称成功")
	}
}
