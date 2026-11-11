package clusterharness_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/panel"
)

// 只阻断 B 的读回，让 A 已确认切离也不能提前越过跨组屏障。
type crossGroupDNS struct {
	dnsprovider.Provider
	blocked atomic.Bool
}

func (d *crossGroupDNS) ReadRecord(ctx context.Context, auth dnsprovider.Credentials, key dnsprovider.Key) (dnsprovider.ReadResult, error) {
	if key.RecordID == "101" && d.blocked.Load() {
		return dnsprovider.ReadResult{}, errors.New("B DNSPod unavailable")
	}
	return d.Provider.ReadRecord(ctx, auth, key)
}

func crossGroup(t *testing.T, f *blockedEntryRig, policy string, members []string) string {
	t.Helper()
	g := f.c.Do("POST", "/api/entry-groups", map[string]any{"name": "共享入口组 B", "members": members}, 201)["id"].(string)
	r := f.c.Do("POST", "/api/rules", directBody(g, 29002, "127.0.0.1", 12345), 201)
	awaitRule(t, f.c, r["id"].(string), "正常")
	f.dns.Set("example.com", dnsprovider.Record{ID: "101", Name: "other", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	a := f.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "B DNSPod", "secret_id": "AKIDtest12345", "secret_key": "testSecretKey"}, 201)["id"].(string)
	b := binding(a)
	b["record_id"] = "101"
	f.c.Do("PUT", "/api/entry-groups/"+g+"/managed-record", b, 200)
	firstPublish(f.c, g, f.ids[0], 202)
	awaitDNSStage(t, f.c, g, "服务商已写入")
	f.c.Do("PUT", "/api/entry-groups/"+g+"/blocked-policy", map[string]string{"policy": policy}, 200)
	return g
}

func crossPublication(t *testing.T, f *blockedEntryRig, g, record, active, ip string) {
	t.Helper()
	v := publication(f.c, g)
	if v["active_node_id"] != active || v["written_at"] == nil || f.dns.Record("example.com", record).Value != ip {
		t.Fatalf("共享组发布不符: %v / %s", v, f.dns.Record("example.com", record).Value)
	}
}

// 删除跨组确认屏障时，A 切离完成会在 B DNS 不可用时提前调用商家。
func TestCrossGroupRotationWaitsForEveryDNSWriteAndRespectsFailback(t *testing.T) {
	gate := &crossGroupDNS{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "switch_then_rotate"}, 200)
	g := crossGroup(t, f, "switch_entry", f.ids[:])
	gate.blocked.Store(true)
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	strategySamples(t, f, 3)
	if f.calls.Load() != 0 {
		t.Fatal("B 的 DNS 切离未确认，A 已提前调用换 IP 接口")
	}
	if v := f.c.Do("GET", rotationPath(f), nil, 200); v["action"] != nil || v["quota"].(map[string]any)["used_day"] != float64(0) {
		t.Fatalf("等待跨组切离时提前占用额度: %v", v)
	}
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	login(f.c)
	strategyReconnect(t, f)
	strategySamples(t, f, 2)
	if f.calls.Load() != 0 {
		t.Fatal("重启丢失跨组切离屏障")
	}
	gate.blocked.Store(false)
	awaitStrategyRotation(t, f)
	crossPublication(t, f, g, "101", f.ids[1], "1.1.1.1")
	// B 手动选择当前备用，暂停本组回切，A 仍按自己的窗口恢复。
	p := publication(f.c, g)
	manualSwitch(f.c, g, f.ids[1], "1.1.1.1", int(p["desired_version"].(float64)), 202)
	awaitDNSStage(t, f.c, g, "服务商已写入")
	f.sites[0].mode.Store(2)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	strategySamples(t, f, 14)
	f.awaitPublication(t, f.ids[0], 3)
	crossPublication(t, f, f.group, "100", f.ids[0], "4.2.2.2")
	crossPublication(t, f, g, "101", f.ids[1], "1.1.1.1")
	if f.calls.Load() != 1 {
		t.Fatal("跨组恢复重复换 IP")
	}
}

// 两个策略①同时需要换 IP 也必须先保护所有组，共用一个节点动作及额度。
func TestCrossGroupRotationTwoApplicantsShareActionAndQuota(t *testing.T) {
	for _, policy := range []string{"rotate_ip", "switch_then_rotate"} {
		t.Run(policy, func(t *testing.T) { crossGroupApplicants(t, policy) })
	}
}

func crossGroupApplicants(t *testing.T, policy string) {
	t.Helper()
	f := newBlockedEntryRig(t, true)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": policy}, 200)
	g := crossGroup(t, f, policy, []string{f.ids[0]})
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	crossPublication(t, f, f.group, "100", f.ids[1], "1.1.1.1")
	v := f.c.Do("GET", rotationPath(f), nil, 200)
	if f.calls.Load() != 1 || v["quota"].(map[string]any)["used_day"] != float64(1) {
		t.Fatalf("未共用额度: %v", v)
	}
	f.sites[0].mode.Store(2)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	awaitDNSStage(t, f.c, g, "服务商已写入")
	crossPublication(t, f, g, "101", f.ids[0], "4.2.2.2")
	if f.calls.Load() != 1 {
		t.Fatal("两个组各调用了一次商家")
	}
}

func TestCrossGroupRotationStorageFailureCannotBypassSwitch(t *testing.T) {
	faults := &clusterharness.PersistenceFaults{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { faults.Base = d.Persistence; d.Persistence = faults })
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	crossGroup(t, f, "disabled", f.ids[:])
	if err := faults.FailEntryActions("INSERT"); err != nil {
		t.Fatal(err)
	}
	f.block(t, 0)
	strategySamples(t, f, 3)
	if f.calls.Load() != 0 || f.dns.Writes() != 2 {
		t.Fatal("跨组切离持久化失败却执行了外部动作")
	}
	if err := faults.FailEntryActions(""); err != nil {
		t.Fatal(err)
	}
	awaitStrategyRotation(t, f)
	if f.calls.Load() != 1 || f.dns.Writes() != 4 {
		t.Fatal("存储恢复后未先切离再换 IP")
	}
}

func TestCrossGroupRotationDisabledNodeNeverCallsVendor(t *testing.T) {
	f := policyThree(t, true)
	crossGroup(t, f, "rotate_ip", f.ids[:])
	f.c.Do("PUT", rotationPath(f)+"/automatic", map[string]bool{"enabled": false}, 200)
	f.block(t, 0)
	strategySamples(t, f, 12)
	if v := f.c.Do("GET", rotationPath(f), nil, 200); v["action"] != nil || f.calls.Load() != 0 || v["quota"].(map[string]any)["used_day"] != float64(0) {
		t.Fatalf("关闭总授权仍换 IP: %v", v)
	}
}

// 已按策略③退化保存的工作流，在等待别组期间出现候选，仍须先切离并延续关联。
func TestCrossGroupRotationCandidateAppearsWhileAnotherGroupWaits(t *testing.T) {
	gate := &crossGroupDNS{}
	f := newBlockedEntryRig(t, false, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "switch_then_rotate"}, 200)
	g := crossGroup(t, f, "disabled", f.ids[:])
	gate.blocked.Store(true)
	manualSwitch(f.c, g, f.ids[1], "1.1.1.1", 1, 202)
	f.block(t, 0)
	strategySamples(t, f, 2)
	events := waitOfflineEvents(t, f.c, "?type=entry_blocked_waiting", 1)
	degraded := false
	for _, event := range events {
		message := event.(map[string]any)["message"].(string)
		degraded = degraded || strings.Contains(message, f.group) && strings.Contains(message, "退化")
	}
	if !degraded {
		t.Fatal("未先持久保存 A 的无候选退化决策")
	}
	if f.calls.Load() != 0 {
		t.Fatal("别组仍等待切离时已调用商家")
	}
	f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, f.c, f.ids[1], 1, "normal", 0)
	strategySamples(t, f, 2)
	f.awaitPublication(t, f.ids[1], 2)
	if f.calls.Load() != 0 {
		t.Fatal("候选出现后没有继续等待全部组")
	}
	gate.blocked.Store(false)
	awaitStrategyRotation(t, f)
	crossPublication(t, f, f.group, "100", f.ids[1], "1.1.1.1")
	crossPublication(t, f, g, "101", f.ids[1], "1.1.1.1")
	if f.calls.Load() != 1 {
		t.Fatal("退化工作流没有延续到唯一节点动作")
	}
}

// 必测场景5：所有成员被墙时保留解析，不在坏入口间循环；一次动作共用额度。
func TestCrossGroupRotationAllEntriesBlockedHasBoundedActions(t *testing.T) {
	f := policyThree(t, true)
	g := crossGroup(t, f, "switch_entry", f.ids[:])
	for _, id := range f.ids {
		f.c.Do("PUT", "/api/nodes/"+id+"/maintenance", map[string]bool{"enabled": true}, 204)
	}
	f.sites[0].mode.Store(0)
	f.sites[1].mode.Store(0)
	f.samples(t, 10)
	awaitSelfCheck(t, f.c, f.ids[0], 4, "blocked", 3)
	awaitSelfCheck(t, f.c, f.ids[1], 4, "blocked", 3)
	for _, id := range f.ids {
		f.c.Do("PUT", "/api/nodes/"+id+"/maintenance", map[string]bool{"enabled": false}, 204)
	}
	awaitStrategyRotation(t, f)
	crossPublication(t, f, f.group, "100", f.ids[0], "8.8.8.8")
	crossPublication(t, f, g, "101", f.ids[0], "8.8.8.8")
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["state"] == "changed" })
	strategySamples(t, f, 9)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "no_improvement" })
	strategySamples(t, f, 12)
	if f.calls.Load() != 1 {
		t.Fatal("全部入口被墙时出现无界换 IP")
	}
	if f.dns.Writes() != 4 {
		t.Fatalf("坏入口间反复切换: DNS 写入 %d", f.dns.Writes())
	}
	// 共用的一小时额度释放前没有第二次动作；第二次仍无改善后暂停，
	// 再过一小时也不能无限换 IP。
	for range 360 {
		strategySamples(t, f, 1)
		if f.calls.Load() == 2 {
			break
		}
	}
	awaitStrategyRotation(t, f)
	f.ip[0].Store("8.8.4.4")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["new_ip"] == "8.8.4.4" })
	strategySamples(t, f, 9)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["paused"] == true && v["no_improvement"] == float64(2) })
	strategySamples(t, f, 360)
	if f.calls.Load() != 2 || f.dns.Writes() != 6 {
		t.Fatal("全部入口被墙时未受共享额度和无改善暂停约束")
	}
	events := f.c.Do("GET", "/api/events?type=ip_rotation_cross_group", nil, 200)["events"].([]any)
	found := false
	for _, e := range events {
		message := e.(map[string]any)["message"].(string)
		found = found || strings.Contains(message, g) && strings.Contains(message, "保留")
	}
	if !found {
		t.Fatal("未记录策略②无候选时受他组换 IP 影响")
	}
}
