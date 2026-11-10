package clusterharness_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

func policyThree(t *testing.T, candidate bool) *blockedEntryRig {
	t.Helper()
	f := newBlockedEntryRig(t, candidate)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "switch_then_rotate"}, 200)
	return f
}

func rotationPath(f *blockedEntryRig) string { return "/api/nodes/" + f.ids[0] + "/ip-rotation" }

func strategyRotationView(t *testing.T, f *blockedEntryRig, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	return awaitRotationView(t, &rotationFixture{h: f.h, c: f.c, id: f.ids[0], path: rotationPath(f)}, ready)
}

func strategyPublication(t *testing.T, f *blockedEntryRig, active string, version, writes int, ip string) {
	t.Helper()
	pub := publication(f.c, f.group)
	if pub["active_node_id"] != active || pub["desired_version"] != float64(version) || f.dns.Writes() != writes || f.dns.Record("example.com", "100").Value != ip {
		t.Fatalf("入口发布与 DNS 不符: %v，写入 %d 次，记录 %s", pub, f.dns.Writes(), f.dns.Record("example.com", "100").Value)
	}
}

func awaitStrategyRotation(t *testing.T, f *blockedEntryRig) map[string]any {
	t.Helper()
	for range 6 {
		strategySamples(t, f, 1)
		v := f.c.Do("GET", rotationPath(f), nil, 200)
		if a, ok := v["action"].(map[string]any); ok && a["state"] == "observing" {
			return a
		}
	}
	v := f.c.Do("GET", "/api/nodes/"+f.ids[0]+"/self-check", nil, 200)
	t.Logf("许可未推进，原始自测=%v/%v，通知=%v，发布=%v", v["state"], v["enabled"], f.c.Do("GET", "/api/events?type=ip_rotation_entry_waiting", nil, 200), publication(f.c, f.group))
	return awaitRotation(t, f.c, rotationPath(f), "observing")
}

// 这里只等待已发生的候选采样与两端活性，不从上一轮自测时间推算调度期限。
// 重连/设置变更后自测计时器可错峰；需要复检轮次时由调用方经 API 单独等待。
func strategySamples(t *testing.T, f *blockedEntryRig, count int) {
	t.Helper()
	for range count {
		before := [2]any{nodeRow(t, f.c, f.ids[0])["last_seen_at"], nodeRow(t, f.c, f.ids[1])["last_seen_at"]}
		sampleEntryState(t, f.h, f.c, f.group, f.ids[0])
		for i, id := range f.ids {
			awaitNode(t, f.c, id, func(v map[string]any) bool { return v["last_seen_at"] != before[i] })
		}
	}
}

func strategyReconnect(t *testing.T, f *blockedEntryRig) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if nodeRow(t, f.c, f.ids[0])["connected"] == true && nodeRow(t, f.c, f.ids[1])["connected"] == true {
			return
		}
		awaitControlIdle(t, f.h)
		f.h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("面板重建后节点未重连")
}

func strategyNotice(t *testing.T, f *blockedEntryRig, text string) {
	t.Helper()
	strategySamples(t, f, 1)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, sent := f.telegram.snapshot()
		for _, m := range sent {
			if strings.Contains(m.Text, "策略③") && strings.Contains(m.Text, text) {
				return
			}
		}
		strategySamples(t, f, 1)
	}
	t.Fatalf("策略③没有通知 %s", text)
}

// 删除“写入已确认”屏障会在 DNS 不可用时提前调用商家；恢复后只能切一次、换一次。
func TestEntryBlockedRotationWaitsForSwitchThenRechecksAndFailsBack(t *testing.T) {
	f := policyThree(t, true)
	var dnsUnavailable atomic.Bool
	dnsUnavailable.Store(true)
	f.dns.Read = func(context.Context) error {
		if dnsUnavailable.Load() {
			return errors.New("DNSPod unavailable")
		}
		return nil
	}
	f.block(t, 0)
	strategySamples(t, f, 3)
	if f.calls.Load() != 0 || f.dns.Writes() != 1 {
		t.Fatal("切离未确认就调用商家")
	}
	dnsUnavailable.Store(false)
	strategySamples(t, f, 1)
	f.awaitPublication(t, f.ids[1], 2)
	strategySamples(t, f, 1)
	awaitStrategyRotation(t, f)
	strategyPublication(t, f, f.ids[1], 2, 2, "1.1.1.1")
	// 复检报告独立于周期自测；关闭周期自测后仍应完成复检与回切。
	f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/self-check", map[string]bool{"enabled": false}, 204)
	awaitRevision(t, f.c, f.ids[0], 0)
	f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/maintenance", map[string]bool{"enabled": true}, 204)
	f.sites[0].mode.Store(2)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	strategyPublication(t, f, f.ids[1], 2, 2, "1.1.1.1")
	f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/maintenance", map[string]bool{"enabled": false}, 204)
	strategySamples(t, f, 5)
	strategyPublication(t, f, f.ids[1], 2, 2, "1.1.1.1")
	strategySamples(t, f, 1)
	f.awaitPublication(t, f.ids[0], 3)
	strategyPublication(t, f, f.ids[0], 3, 3, "4.2.2.2")
	incident := awaitIncident(t, f.h, f.c, "blocked")
	awaitIncidentEntry(t, f.h, f.c, incident["id"].(string), func(e map[string]any) bool { return e["stage"] == "回切已提交" })
	if f.calls.Load() != 1 {
		t.Fatal("重复调用商家")
	}
}

func TestEntryBlockedRotationWithoutCandidateFallsBackToPolicyOne(t *testing.T) {
	f := policyThree(t, false)
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	strategyPublication(t, f, f.ids[0], 1, 1, "8.8.8.8")
	f.sites[0].mode.Store(2)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	f.awaitPublication(t, f.ids[0], 2)
	strategyPublication(t, f, f.ids[0], 2, 2, "4.2.2.2")
	if f.calls.Load() != 1 {
		t.Fatal("退化重复调用商家")
	}
}

// 第一次无改善后原节点已非活动成员，仍须在共享额度释放后可重试，第二次暂停。
func TestEntryBlockedRotationNoImprovementRetriesAndPauses(t *testing.T) {
	f := policyThree(t, true)
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	for attempt, ip := range []string{"4.2.2.2", "8.8.4.4"} {
		f.ip[0].Store(ip)
		strategySamples(t, f, 6)
		strategyRotationView(t, f, func(v map[string]any) bool {
			a := v["action"].(map[string]any)
			return a["new_ip"] == ip && len(a["recheck_rounds"].([]any)) >= 1
		})
		for rounds := 2; rounds <= 3; rounds++ {
			strategySamples(t, f, 3)
			strategyRotationView(t, f, func(v map[string]any) bool {
				return len(v["action"].(map[string]any)["recheck_rounds"].([]any)) >= rounds
			})
		}
		v := strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "no_improvement" })
		if v["no_improvement"] != float64(attempt+1) || v["paused"] != (attempt == 1) {
			t.Fatalf("无改善未累计: %v", v)
		}
		strategyPublication(t, f, f.ids[1], 2, 2, "1.1.1.1")
		if attempt == 0 {
			// 保留磁盘重建面板，工作流和节点级额度不能丢失。
			if err := f.h.Restart(); err != nil {
				t.Fatal(err)
			}
			login(f.c)
			strategyReconnect(t, f)
			strategySamples(t, f, 360)
			awaitStrategyRotation(t, f)
		}
	}
	strategyNotice(t, f, "留在备用入口")
	waitOfflineEvents(t, f.c, "?node_id="+f.ids[0]+"&type=ip_rotation_paused", 1)
	if f.calls.Load() != 2 {
		t.Fatal("暂停后仍调用商家")
	}
}

func TestEntryBlockedRotationPendingDoesNotUsePeriodicSuccessToFailback(t *testing.T) {
	f := policyThree(t, true)
	settings := f.c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["sites"] = []string{"one.test", "two.test", "three.test", "four.test"}
	f.c.Do("PUT", "/api/settings/self-check", settings, 204)
	for _, id := range f.ids {
		awaitSelfCheck(t, f.c, id, 2, "normal", 0)
	}
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/self-check", map[string]bool{"enabled": false}, 204)
	awaitRevision(t, f.c, f.ids[0], 0)
	f.sites[0].mode.Store(1)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	for n := 1; n <= 3; n++ {
		if n > 1 {
			strategySamples(t, f, 3)
		}
		strategyRotationView(t, f, func(v map[string]any) bool { return len(v["action"].(map[string]any)["recheck_rounds"].([]any)) >= n })
	}
	v := strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "pending" })
	if v["no_improvement"] != float64(0) {
		t.Fatal("未知累计无改善")
	}
	f.sites[0].mode.Store(2)
	f.c.Do("PUT", "/api/nodes/"+f.ids[0]+"/self-check", map[string]bool{"enabled": true}, 204)
	strategySamples(t, f, 15)
	strategyPublication(t, f, f.ids[1], 2, 2, "1.1.1.1")
	strategyNotice(t, f, "待确认")
	// 服务商偏差写回只是修复同一活动入口，不能解除新 IP 复检门槛。
	r := f.dns.Record("example.com", "100")
	r.Value = "9.9.9.9"
	f.dns.Set("example.com", r)
	strategySamples(t, f, 4)
	f.awaitPublication(t, f.ids[1], 3)
	strategySamples(t, f, 8)
	strategyPublication(t, f, f.ids[1], 3, 3, "1.1.1.1")
	// 管理员用一次合法手动换 IP 处理待确认后，新动作的正常复检可恢复回切。
	f.c.Do("POST", rotationPath(f)+"/actions", nil, 202)
	awaitStrategyRotation(t, f)
	f.ip[0].Store("8.8.4.4")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	strategySamples(t, f, 8)
	f.awaitPublication(t, f.ids[0], 4)
	strategyPublication(t, f, f.ids[0], 4, 4, "8.8.4.4")
}

func TestEntryBlockedRotationManualNodeRecoveryCanFailback(t *testing.T) {
	f := policyThree(t, true)
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	for n := 1; n <= 3; n++ {
		if n > 1 {
			strategySamples(t, f, 3)
		}
		strategyRotationView(t, f, func(v map[string]any) bool { return len(v["action"].(map[string]any)["recheck_rounds"].([]any)) >= n })
	}
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "no_improvement" })
	f.c.Do("POST", rotationPath(f)+"/actions", nil, 202)
	awaitStrategyRotation(t, f)
	f.sites[0].mode.Store(2)
	f.ip[0].Store("8.8.4.4")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	strategySamples(t, f, 8)
	f.awaitPublication(t, f.ids[0], 3)
	strategyPublication(t, f, f.ids[0], 3, 3, "8.8.4.4")
	if f.calls.Load() != 2 {
		t.Fatal("手动恢复重复换 IP")
	}
}

func TestEntryBlockedRotationQuotaAndFreezeKeepStandbyAndNotify(t *testing.T) {
	for _, reason := range []string{"quota", "frozen"} {
		t.Run(reason, func(t *testing.T) {
			f := policyThree(t, true)
			// 先手动消耗共享额度；不改变 IP 时将进入冻结。
			f.c.Do("POST", rotationPath(f)+"/actions", nil, 202)
			awaitStrategyRotation(t, f)
			if reason == "quota" {
				f.ip[0].Store("4.2.2.2")
				strategySamples(t, f, 6)
				strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
			} else {
				for n := 1; n <= 5; n++ {
					strategySamples(t, f, 6)
					strategyRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(n) })
				}
				awaitRotation(t, f.c, rotationPath(f), "unconfirmed")
			}
			f.block(t, 0)
			version := 2
			if reason == "quota" {
				version = 3
			}
			f.awaitPublication(t, f.ids[1], version)
			strategySamples(t, f, 3)
			strategyPublication(t, f, f.ids[1], version, version, "1.1.1.1")
			strategyNotice(t, f, "留在备用入口")
			if f.calls.Load() != 1 {
				t.Fatal("受限或冻结后仍调用商家")
			}
		})
	}
}

func TestEntryBlockedRotationManualSwitchInvalidatesOriginalFailback(t *testing.T) {
	f := policyThree(t, true)
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	manualSwitch(f.c, f.group, f.ids[1], "1.1.1.1", 2, 202)
	f.awaitPublication(t, f.ids[1], 3)
	f.sites[0].mode.Store(2)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	strategySamples(t, f, 15)
	strategyPublication(t, f, f.ids[1], 3, 2, "1.1.1.1")
	if publication(f.c, f.group)["auto_failback_paused"] != true {
		t.Fatal("手动暂停丢失")
	}
	// 显式恢复后，允许一个新版本的普通回切，旧动作不能抢先执行。
	f.c.Do("POST", "/api/entry-groups/"+f.group+"/resume-failback", map[string]int{"expected_version": 3}, 204)
	strategySamples(t, f, 1)
	f.awaitPublication(t, f.ids[0], 4)
	strategyPublication(t, f, f.ids[0], 4, 3, "4.2.2.2")
}

func TestEntryBlockedRotationStorageFailureCannotStartVendor(t *testing.T) {
	faults := &clusterharness.PersistenceFaults{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { faults.Base = d.Persistence; d.Persistence = faults })
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "switch_then_rotate"}, 200)
	if err := faults.FailEntryActions("INSERT"); err != nil {
		t.Fatal(err)
	}
	f.block(t, 0)
	strategySamples(t, f, 3)
	strategyPublication(t, f, f.ids[0], 1, 1, "8.8.8.8")
	if f.calls.Load() != 0 {
		t.Fatal("切离存储失败却调用商家")
	}
	if err := faults.FailEntryActions(""); err != nil {
		t.Fatal(err)
	}
	awaitStrategyRotation(t, f)
	f.awaitPublication(t, f.ids[1], 2)
	if f.calls.Load() != 1 {
		t.Fatal("持久化恢复后未续接")
	}
}

func TestEntryBlockedRotationOwnUnconfirmedResultKeepsStandby(t *testing.T) {
	f := policyThree(t, true)
	await := func(phase string) {
		strategyRotationView(t, f, func(v map[string]any) bool { a, ok := v["action"].(map[string]any); return ok && a["state"] == phase })
	}
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	for n := 1; n <= 5; n++ {
		strategySamples(t, f, 6)
		strategyRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(n) })
	}
	await("unconfirmed")
	strategyPublication(t, f, f.ids[1], 2, 2, "1.1.1.1")
	strategyNotice(t, f, "冻结")
	if f.calls.Load() != 1 {
		t.Fatal("未确认动作重复调用商家")
	}
}

// 策略③等待原节点恢复时，新活动入口的真实故障仍须立即走既有切换路径。
func TestEntryBlockedRotationDoesNotSuppressStandbyFailure(t *testing.T) {
	f := policyThree(t, true)
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	f.sites[0].mode.Store(2)
	strategySamples(t, f, 6)
	strategyPublication(t, f, f.ids[1], 2, 2, "1.1.1.1")
	if entryState(t, f.c, f.group, f.ids[0])["candidate"] != true {
		t.Fatal("原节点没有正常候选证据")
	}
	f.sites[1].mode.Store(0)
	for range 15 {
		strategySamples(t, f, 1)
		if f.c.Do("GET", "/api/nodes/"+f.ids[1]+"/self-check", nil, 200)["state"] == "blocked" {
			break
		}
	}
	if f.c.Do("GET", "/api/nodes/"+f.ids[1]+"/self-check", nil, 200)["state"] != "blocked" {
		t.Fatal("备用入口没有真实被墙证据")
	}
	strategySamples(t, f, 1)
	f.awaitPublication(t, f.ids[0], 3)
	strategyPublication(t, f, f.ids[0], 3, 3, "8.8.8.8")
}

func TestEntryBlockedRotationManualSwitchBeforePermitInvalidatesWorkflow(t *testing.T) {
	f := policyThree(t, true)
	var unavailable atomic.Bool
	unavailable.Store(true)
	f.dns.Read = func(context.Context) error {
		if unavailable.Load() {
			return errors.New("DNS unavailable")
		}
		return nil
	}
	f.block(t, 0)
	manualSwitch(f.c, f.group, f.ids[1], "1.1.1.1", 2, 202)
	unavailable.Store(false)
	strategySamples(t, f, 2)
	f.awaitPublication(t, f.ids[1], 3)
	strategySamples(t, f, 6)
	strategyPublication(t, f, f.ids[1], 3, 2, "1.1.1.1")
	if f.calls.Load() != 0 {
		t.Fatal("手动入口切换后旧工作流仍申请许可")
	}
}

func TestEntryBlockedRotationPolicyChangeHasItsOwnDecision(t *testing.T) {
	f := newBlockedEntryRig(t, false)
	f.block(t, 0)
	waitOfflineEvents(t, f.c, "?type=entry_blocked_waiting", 1)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "switch_then_rotate"}, 200)
	awaitStrategyRotation(t, f)
	waitOfflineEvents(t, f.c, "?type=entry_blocked_waiting", 2)
	rows := f.c.Do("GET", "/api/events?type=entry_blocked_waiting", nil, 200)["events"].([]any)
	if !strings.Contains(rows[0].(map[string]any)["message"].(string), "退化为策略①") {
		t.Fatal("策略③复用了策略②不换 IP 的决策说明")
	}
}

func TestEntryBlockedRotationWritebackMustBeConfirmedBeforeRetry(t *testing.T) {
	f := policyThree(t, true)
	f.c.Do("POST", rotationPath(f)+"/actions", nil, 202)
	awaitStrategyRotation(t, f)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 3)
	var blockReads atomic.Bool
	f.dns.Read = func(context.Context) error {
		if blockReads.Load() {
			return errors.New("writeback unconfirmed")
		}
		return nil
	}
	f.dns.Write = func(context.Context) error { blockReads.Store(true); return errors.New("response lost") }
	r := f.dns.Record("example.com", "100")
	r.Value = "9.9.9.9"
	f.dns.Set("example.com", r)
	for range 10 {
		strategySamples(t, f, 1)
		if blockReads.Load() {
			break
		}
	}
	if !blockReads.Load() {
		t.Fatal("没有触发真实偏差写回")
	}
	strategySamples(t, f, 360)
	if pub := publication(f.c, f.group); pub["desired_version"] != float64(4) || pub["written_at"] != nil {
		t.Fatalf("写回没有保持未确认: %v", pub)
	}
	if f.calls.Load() != 1 {
		t.Fatal("当前切离写回未确认就签发了许可")
	}
	blockReads.Store(false)
	awaitStrategyRotation(t, f)
	f.awaitPublication(t, f.ids[1], 4)
	if f.calls.Load() != 2 {
		t.Fatal("核对确认后未继续换 IP")
	}
}
