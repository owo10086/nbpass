package clusterharness_test

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
)

func entryState(t *testing.T, c *clusterharness.Client, group, id string) map[string]any {
	t.Helper()
	rows, ok := groupRow(t, c, group)["member_states"].([]any)
	if !ok {
		t.Fatal("入口组缺少逐成员就绪与候选状态")
	}
	for _, row := range rows {
		v := row.(map[string]any)
		if v["node_id"] == id {
			return v
		}
	}
	t.Fatalf("缺少成员 %s", id)
	return nil
}

func sampleEntryState(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, group, id string) map[string]any {
	t.Helper()
	awaitControlIdle(t, h)
	h.Clock.Advance(10 * time.Second)
	want := h.Clock.Now().Format(time.RFC3339Nano)
	var row map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row = entryState(t, c, group, id)
		if row["checked_at"] == want {
			return row
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("未按十秒周期检查候选: %v", row)
	return nil
}

// 隧道健康每约五秒探测、每五秒上报；按真实采样节奏推进，避免十秒大跳跨过证据有效期。
func sampleTunnelEntryState(t *testing.T, r *failoverRig, group string) map[string]any {
	t.Helper()
	started := r.h.Clock.Now()
	for range 10 {
		awaitControlIdle(t, r.h)
		r.h.Clock.Advance(time.Second)
		sampled := r.c.Do("GET", "/api/nodes/"+r.entryID+"/probe", nil, 200)["sampled_at"]
		if sampled == nil {
			t.Fatal("入口缺少探针样本")
		}
		at, err := time.Parse(time.RFC3339Nano, sampled.(string))
		if err != nil {
			t.Fatal(err)
		}
		if r.h.Clock.Now().Sub(at) >= 5*time.Second {
			deadline := time.Now().Add(5 * time.Second)
			for r.c.Do("GET", "/api/nodes/"+r.entryID+"/probe", nil, 200)["sampled_at"] == sampled {
				if time.Now().After(deadline) {
					t.Fatal("探针未按五秒周期上报")
				}
				time.Sleep(time.Millisecond)
			}
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := entryState(t, r.c, group, r.entryID)
		if s, ok := row["checked_at"].(string); ok {
			at, err := time.Parse(time.RFC3339Nano, s)
			if err == nil && at.After(started) {
				return row
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有新的定时候选检查: %v", row)
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitTunnelEntryReady(t *testing.T, r *failoverRig, group string, want bool) map[string]any {
	t.Helper()
	var row map[string]any
	for range 4 {
		row = sampleTunnelEntryState(t, r, group)
		if row["ready"] == want {
			return row
		}
	}
	t.Fatalf("等待本组就绪=%v失败: %v", want, row)
	return nil
}

func hasEntryReason(row map[string]any, field, code string) bool {
	for _, raw := range row[field].([]any) {
		if raw.(map[string]any)["code"] == code {
			return true
		}
	}
	return false
}

// 捕获按节点整体错误判就绪、停用规则阻塞就绪以及 bind 错误未展示。
func TestEntryReadinessIsScopedToEnabledRulesInItsGroup(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "多组入口")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	good := entryGroup(c, id)["id"].(string)
	bad := entryGroup(c, id)["id"].(string)
	c.Do("POST", "/api/rules", directBody(good, h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "A:")), 201)
	awaitRevision(t, c, id, 0)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	rule := c.Do("POST", "/api/rules", directBody(bad, occupied.Addr().(*net.TCPAddr).Port, "127.0.0.1", 12345), 201)
	awaitRule(t, c, rule["id"].(string), "同步失败")
	row := sampleEntryState(t, h, c, bad, id)
	if row["ready"] != false || !hasEntryReason(row, "readiness_reasons", "rule_apply_failed") || !strings.Contains(row["readiness_reasons"].([]any)[0].(map[string]any)["message"].(string), "bind") {
		t.Fatalf("缺失本组监听失败原因: %v", row)
	}
	if row = entryState(t, c, good, id); row["ready"] != true {
		t.Fatalf("其他组错误污染本组: %v", row)
	}
	body := directBody(bad, occupied.Addr().(*net.TCPAddr).Port, "127.0.0.1", 12345)
	body["enabled"] = false
	c.Do("PUT", "/api/rules/"+rule["id"].(string), body, 200)
	awaitRevision(t, c, id, 0)
	if row = sampleEntryState(t, h, c, bad, id); row["ready"] != true {
		t.Fatalf("停用规则仍阻塞就绪: %v", row)
	}
}

// 握手证据必须逐规则匹配已确认出口，且不能把旧成功或其他出口借给本规则。
func TestEntryReadinessRequiresAnAuthorizedSuccessfulExitForEveryTunnelRule(t *testing.T) {
	r := setupFailover(t, 2)
	group := r.body["entry_group_id"].(string)
	for _, id := range r.exitIDs {
		advanceExitProbe(t, r, id)
	}
	row := awaitTunnelEntryReady(t, r, group, true)
	if row["ready"] != true {
		t.Fatalf("获授权出口握手成功仍未就绪: %v; 健康=%v", row, r.c.Do("GET", "/api/nodes/"+r.entryID+"/exit-health", nil, 200))
	}
	for _, link := range r.links {
		link.block(true)
	}
	for _, id := range r.exitIDs {
		advanceExitProbe(t, r, id)
	}
	row = awaitTunnelEntryReady(t, r, group, false)
	if row["ready"] != false || !hasEntryReason(row, "readiness_reasons", "tunnel_handshake_missing") {
		t.Fatalf("全部出口握手失败仍就绪: %v", row)
	}
	// 先走满失败门槛，再验证恢复成功不能绕过连续 60 秒恢复期。
	var health map[string]any
	for range 6 {
		health = advanceExitProbe(t, r, r.exitIDs[1])
		if health["state"] == "unhealthy" {
			break
		}
	}
	if health["state"] != "unhealthy" {
		t.Fatalf("恢复前未进入不可用状态: %v", health)
	}
	r.links[1].recover()
	for range 3 {
		health = advanceExitProbe(t, r, r.exitIDs[1])
		if health["state"] == "recovering" {
			break
		}
	}
	if health["state"] != "recovering" || health["last_probe"] != "success" {
		t.Fatalf("成功握手未进入恢复期: %v", health)
	}
	start := health["recovery_since_unix"].(float64)
	row = sampleTunnelEntryState(t, r, group)
	if float64(r.h.Clock.Now().Unix())-start >= 60 || row["ready"] != false || !hasEntryReason(row, "readiness_reasons", "tunnel_handshake_missing") {
		t.Fatalf("恢复期成功握手不应提前使入口就绪: 入口=%v 健康=%v", row, exitHealthRow(t, r, r.exitIDs[1]))
	}
	for range 16 {
		health = advanceExitProbe(t, r, r.exitIDs[1])
		elapsed := health["checked_at_unix"].(float64) - start
		if health["last_probe"] != "success" || health["state"] == "recovering" && health["recovery_since_unix"] != start {
			t.Fatalf("恢复期没有保持连续成功: %v", health)
		}
		if elapsed < 60 && health["state"] != "recovering" {
			t.Fatalf("未满 60 秒提前恢复: %v", health)
		}
		if health["state"] == "healthy" {
			break
		}
	}
	if health["state"] != "healthy" {
		t.Fatalf("连续成功满 60 秒仍未健康: %v", health)
	}
	row = awaitTunnelEntryReady(t, r, group, true)
	if row["ready"] != true {
		t.Fatalf("一个获授权出口恢复后仍不就绪: %v", row)
	}
}

func TestEntryCandidatesRequireFreshEvidenceAndExcludeMaintenanceAndOffline(t *testing.T) {
	h, c := selfCheckBrowser(t)
	fake := &domesticSites{}
	fake.mode.Store(2)
	n := addNode(c, "候选入口")
	id := n["id"].(string)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	real := h.StartNode(n["installation_ticket"].(string), fake.dependencies, func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
	})
	awaitNode(t, c, id, connected)
	group := entryGroup(c, id)["id"].(string)
	c.Do("POST", "/api/rules", directBody(group, h.ReserveTCPPort(), "127.0.0.1", 12345), 201)
	awaitRevision(t, c, id, 0)
	row := sampleEntryState(t, h, c, group, id)
	if row["ready"] != true || row["candidate"] != false || row["candidate_status"] != "unknown" || !hasEntryReason(row, "candidate_reasons", "domestic_evidence_missing") {
		t.Fatalf("无国内证据被当候选: %v", row)
	}
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, id, 1, "normal", 0)
	row = sampleEntryState(t, h, c, group, id)
	if row["candidate"] != true || row["domestic_evidence"].(map[string]any)["source"] != "self_check" {
		t.Fatalf("正常自测未成为候选: %v", row)
	}
	// 同一时刻重复读不会新增采样；未满十秒不会产生下次检查。
	checked := row["checked_at"]
	awaitControlIdle(t, h)
	h.Clock.Advance(9 * time.Second)
	if row = entryState(t, c, group, id); row["checked_at"] != checked {
		t.Fatalf("读取推进了候选采样: %v", row)
	}
	c.Do("PUT", "/api/nodes/"+id+"/maintenance", map[string]bool{"enabled": true}, 204)
	row = sampleEntryState(t, h, c, group, id)
	if row["candidate"] != false || !hasEntryReason(row, "candidate_reasons", "maintenance") {
		t.Fatalf("维护节点仍为候选: %v", row)
	}
	c.Do("PUT", "/api/nodes/"+id+"/maintenance", map[string]bool{"enabled": false}, 204)
	real.Disconnect()
	awaitNode(t, c, id, func(v map[string]any) bool { return v["connected"] == false })
	row = sampleEntryState(t, h, c, group, id)
	if row["candidate"] != false || !hasEntryReason(row, "candidate_reasons", "control_offline") {
		t.Fatalf("离线节点仍为候选: %v", row)
	}
}

func TestEntryCandidateEvidenceExpiresAfterFiveMinutes(t *testing.T) {
	h, c := selfCheckBrowser(t)
	fake := &domesticSites{}
	fake.mode.Store(2)
	n := addNode(c, "证据到期")
	id := n["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	h.StartNode(n["installation_ticket"].(string), fake.dependencies, func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return "8.8.8.8", nil }
	})
	awaitSelfCheck(t, c, id, 1, "normal", 0)
	group := entryGroup(c, id)["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": false}, 204)
	awaitRevision(t, c, id, 0)
	for range 29 {
		sampleEntryState(t, h, c, group, id)
	}
	row := entryState(t, c, group, id)
	if row["candidate"] != true {
		t.Fatalf("五分钟之前提前过期: %v", row)
	}
	row = sampleEntryState(t, h, c, group, id)
	if row["candidate"] != false || row["candidate_status"] != "unknown" || !hasEntryReason(row, "candidate_reasons", "domestic_evidence_expired") {
		t.Fatalf("五分钟边界未失效: %v", row)
	}
}

func TestEntryCandidateCheckerEvidenceAndBlockedExclusion(t *testing.T) {
	h, c := selfCheckBrowser(t)
	startChecker(t, h, c, 0, 0, nil)
	n := addNode(c, "补判候选")
	id := n["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	fake := &domesticSites{}
	fake.mode.Store(1)
	h.StartNode(n["installation_ticket"].(string), fake.dependencies, func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return "8.8.8.8", nil }
	})
	awaitArbitration(t, c, id, "normal")
	group := entryGroup(c, id)["id"].(string)
	row := sampleEntryState(t, h, c, group, id)
	if row["candidate"] != true || row["domestic_evidence"].(map[string]any)["source"] != "domestic_checker" {
		t.Fatalf("有效检测点成功未成为证据: %v", row)
	}
	fake.mode.Store(0)
	for round := 2; round <= 4; round++ {
		advanceSelfCheck(t, h, c, id, round, map[int]string{2: "suspected", 3: "suspected", 4: "blocked"}[round], round-1)
	}
	row = sampleEntryState(t, h, c, group, id)
	if row["candidate"] != false || !hasEntryReason(row, "candidate_reasons", "blocked") {
		t.Fatalf("被墙节点仍为候选: %v", row)
	}
}

func TestEntryCandidateIPChangeInvalidatesEvidenceWithoutRevivingOldAddress(t *testing.T) {
	h, c := selfCheckBrowser(t)
	fake := &domesticSites{}
	fake.mode.Store(2)
	n := addNode(c, "地址证据")
	id := n["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	h.StartNode(n["installation_ticket"].(string), fake.dependencies, func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
	})
	awaitSelfCheck(t, c, id, 1, "normal", 0)
	group := entryGroup(c, id)["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": false}, 204)
	awaitRevision(t, c, id, 0)
	if row := sampleEntryState(t, h, c, group, id); row["candidate"] != true {
		t.Fatalf("初始正常证据丢失: %v", row)
	}
	for _, address := range []string{"1.1.1.1", "8.8.8.8"} {
		ip.Store(address)
		sampleEntryState(t, h, c, group, id)
		awaitNode(t, c, id, func(v map[string]any) bool { return v["public_ipv4"] == address })
		row := sampleEntryState(t, h, c, group, id)
		if row["candidate"] != false || !hasEntryReason(row, "candidate_reasons", "domestic_evidence_missing") {
			t.Fatalf("旧地址证据复活: %v", row)
		}
	}
}

func TestEntryReadinessDoesNotBorrowAnotherRulesExitConfirmation(t *testing.T) {
	store := &faultyConfigStore{}
	r := setupFailover(t, 2, func(i int, d *node.Dependencies) {
		if i == 0 {
			store.ConfigStore = d.Store.(node.ConfigStore)
			d.ConfigStore = store
		}
	})
	store.fail.Store(true)
	body := map[string]any{}
	for k, v := range r.body {
		if k != "id" {
			body[k] = v
		}
	}
	body["listen_port"] = r.reservePort()
	created := r.c.Do("POST", "/api/rules", body, 201)
	awaitNode(t, r.c, r.exitIDs[0], func(v map[string]any) bool { return v["config_status"] == "同步失败" })
	awaitRule(t, r.c, created["id"].(string), "正常")
	group := body["entry_group_id"].(string)
	for _, exit := range r.exitIDs {
		advanceExitProbe(t, r, exit)
	}
	if initial := awaitTunnelEntryReady(t, r, group, true); initial["ready"] != true {
		t.Fatalf("初始未就绪: %v", initial)
	}
	r.links[1].block(true)
	var failed map[string]any
	for range 3 {
		failed = advanceExitProbe(t, r, r.exitIDs[1])
		if failed["last_probe"] == "failure" {
			break
		}
	}
	if failed["last_probe"] != "failure" {
		t.Fatalf("尚未收到实际链路失败证据: %v", failed)
	}
	var row map[string]any
	for range 4 {
		row = sampleTunnelEntryState(t, r, group)
		primaryFresh := true
		for _, raw := range row["readiness_reasons"].([]any) {
			if raw.(map[string]any)["rule_id"] == r.body["id"] {
				primaryFresh = false
			}
		}
		if primaryFresh {
			break
		}
	}
	if row["ready"] != false {
		t.Fatalf("新规则借用了旧规则主出口确认: %v", row)
	}
	found := false
	for _, raw := range row["readiness_reasons"].([]any) {
		reason := raw.(map[string]any)
		if reason["rule_id"] == created["id"] && reason["code"] == "tunnel_handshake_missing" {
			found = true
		}
		if reason["rule_id"] == r.body["id"] {
			t.Fatalf("旧规则的已授权主出口仍成功，不应污染它: %v", row)
		}
	}
	if !found {
		t.Fatalf("没有新规则的独立握手原因: %v", row)
	}
}

func TestEntryCandidateEvidenceSurvivesPanelRestartButChecksRestartUnknown(t *testing.T) {
	h, c := selfCheckBrowser(t)
	fake := &domesticSites{}
	fake.mode.Store(2)
	n := addNode(c, "持久证据")
	id := n["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	h.StartNode(n["installation_ticket"].(string), fake.dependencies, func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return "8.8.8.8", nil }
	})
	awaitSelfCheck(t, c, id, 1, "normal", 0)
	group := entryGroup(c, id)["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": false}, 204)
	awaitRevision(t, c, id, 0)
	row := sampleEntryState(t, h, c, group, id)
	before := row["domestic_evidence"].(map[string]any)["observed_at"]
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	if row = entryState(t, c, group, id); row["candidate"] != false || row["candidate_status"] != "unknown" {
		t.Fatalf("重启复用了旧候选成功: %v", row)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !connected(nodeRow(t, c, id)) {
		if time.Now().After(deadline) {
			t.Fatal("节点未重新连接")
		}
		awaitControlIdle(t, h)
		h.Clock.Advance(time.Second)
		time.Sleep(time.Millisecond)
	}
	row = sampleEntryState(t, h, c, group, id)
	if row["candidate"] != true || row["domestic_evidence"].(map[string]any)["observed_at"] != before {
		t.Fatalf("重启丢失或刷新了原证据时间: %v", row)
	}
	h.Client().Do("GET", "/api/entry-groups", nil, 401)
}

func TestEntryCandidateAcceptsNormalIPRotationRecheck(t *testing.T) {
	sites := &domesticSites{}
	sites.mode.Store(2)
	f := newRotationFixture(t, false, sites.dependencies)
	group := entryGroup(f.c, f.id)["id"].(string)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, f, time.Minute)
	awaitRotation(t, f.c, f.path, "changed")
	awaitRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	row := sampleEntryState(t, f.h, f.c, group, f.id)
	if row["candidate"] != true || row["domestic_evidence"].(map[string]any)["public_ipv4"] != "8.8.8.8" {
		t.Fatalf("绑定新IP的正常复检未计为国内证据: %v", row)
	}
}

// 旧IP的被墙判定不能盖过新IP正常复检；之后的新异常证据仍须排除候选。
func TestEntryCandidateNewIPRecoverySupersedesOldBlockedEvidence(t *testing.T) {
	sites := &domesticSites{}
	f := newRotationFixture(t, false, sites.dependencies)
	settings := f.c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["jitter_seconds"] = 0
	f.c.Do("PUT", "/api/settings/self-check", settings, 204)
	group := entryGroup(f.c, f.id)["id"].(string)
	f.c.Do("PUT", "/api/entry-groups/"+group+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	f.c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	f.c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, f.c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, f.h, f.c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, f.h, f.c, f.id, 3, "blocked", 3)
	tickPolicy(t, f)
	awaitRotation(t, f.c, f.path, "observing")
	sites.mode.Store(2)
	f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, f, time.Minute)
	awaitRotation(t, f.c, f.path, "changed")
	awaitRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	row := sampleEntryState(t, f.h, f.c, group, f.id)
	if row["candidate"] != true {
		t.Fatalf("旧IP被墙状态覆盖了新IP正常复检: %v", row)
	}
	sites.mode.Store(0)
	for round := 4; round <= 6; round++ {
		advanceSelfCheck(t, f.h, f.c, f.id, round, "blocked", 3)
	}
	row = sampleEntryState(t, f.h, f.c, group, f.id)
	if row["candidate"] != false || !hasEntryReason(row, "candidate_reasons", "blocked") {
		t.Fatalf("后续异常被旧正常复检覆盖: %v", row)
	}
}
