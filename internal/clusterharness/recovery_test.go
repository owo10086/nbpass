package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// 回滚数据库后若沿用旧会话/安装票据，或未越过节点已接受的版本，场景会失败。
func TestRestoreInvalidatesCredentialsAndRaisesRevisionAboveNodeHighWater(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "恢复节点")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	body := directBody(gid, h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "R:"))
	rule := c.Do("POST", "/api/rules", body, 201)
	awaitRevision(t, c, id, 0)
	pending := addNode(c, "旧安装票据")
	backup := filepath.Join(h.Dir, "old.db")
	if err := h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		body["enabled"] = !body["enabled"].(bool)
		c.Do("PUT", "/api/rules/"+rule["id"].(string), body, 200)
	}
	high := awaitRevision(t, c, id, 1)
	n.Disconnect()
	if err := h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	c.Do("GET", "/api/overview", nil, 401)
	c = h.Client()
	login(c)
	view := c.Do("GET", "/api/recovery", nil, 200)
	if view["restored_at"] == nil {
		t.Fatal("未进入恢复保护")
	}
	bad := h.StartNode(pending["installation_ticket"].(string))
	select {
	case err := <-bad.Done:
		if err == nil {
			t.Fatal("旧票据仍能接入")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("旧票据未被拒绝")
	}
	n.Restore()
	n.Restart()
	got := awaitRevision(t, c, id, high)
	if got <= high {
		t.Fatalf("配置版本未越过高水位: %v <= %v", got, high)
	}
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	view = c.Do("GET", "/api/recovery", nil, 200)
	states := view["nodes"].([]any)
	for _, raw := range states {
		s := raw.(map[string]any)
		if s["node_id"] == id && s["verified"] != true {
			t.Fatal("在线节点未核对", s)
		}
	}
}

// 真实节点的动作历史补报若遗漏尝试或重复累计，恢复后的共享额度就会错误。
func TestRestoreRebuildsRotationQuotaFromNodeJournal(t *testing.T) {
	f := newRotationFixture(t, false)
	backup := filepath.Join(f.h.Dir, "old.db")
	if err := f.h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	f.n.Disconnect()
	if err := f.h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	f.c.Do("POST", f.path+"/actions", nil, 409)
	f.n.Restore()
	f.n.Restart()
	awaitNode(t, f.c, f.id, connected)
	f.c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	assertRotationQuota(t, f, 1, 1)
	// 重连、再次补报不重复计数。
	f.n.Restart()
	awaitNode(t, f.c, f.id, connected)
	assertRotationQuota(t, f, 1, 1)
}

// 无完整历史时，核对节点配置不能绕过 24 小时自动换 IP 保护。
func TestRestoreMissingRotationHistoryKeepsAutomaticProtection(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "没有动作存储")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.Store = &credentialsOnly{Store: d.Store}
		d.PublicIPv4 = func(context.Context) (string, error) { return "223.5.5.5", nil }
	})
	awaitNode(t, c, id, connected)
	snapshot := filepath.Join(h.Dir, "old.db")
	if err := h.Snapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	n.Disconnect()
	if err := h.RestoreSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	awaitRevision(t, c, id, 0)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	view := c.Do("GET", "/api/recovery", nil, 200)
	state := view["nodes"].([]any)[0].(map[string]any)
	if state["automatic_rotation_paused"] != true {
		t.Fatal("未保留历史缺失保护", state)
	}
	n.Disconnect()
	h.Clock.Advance(24 * time.Hour)
	login(c)
	// 未在线核对的节点即使保护窗口已过，也不能被视为可写。
	view = c.Do("GET", "/api/recovery", nil, 200)
	state = view["nodes"].([]any)[0].(map[string]any)
	if state["verified"] != true || state["automatic_rotation_paused"] != false {
		t.Fatal("24 小时边界错误", state)
	}
}

type credentialsOnly struct{ node.Store }

// 旧快照中的 DNS 待办不能覆盖服务商现状；缺一个成员时整组继续暂停。
func TestRestoreDNSQueueWaitsForEveryMemberAndAdoptsActualRecord(t *testing.T) {
	fake := clusterharness.NewDNSPod()
	record := dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "223.5.5.5", TTL: 60, Enabled: true}
	fake.Set("example.com", record)
	var failRead atomic.Bool
	fake.Read = func(context.Context) error {
		if failRead.Load() {
			return errors.New("读回不可用")
		}
		return nil
	}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = fake })
	c := h.Client()
	setup(c)
	login(c)
	nodes := make([]*clusterharness.Node, 2)
	ids := make([]string, 2)
	for i, ip := range []string{"223.5.5.5", "8.8.8.8"} {
		created := addNode(c, ip)
		ids[i] = created["id"].(string)
		nodes[i] = h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
			d.PublicIPv4 = func(context.Context) (string, error) { return ip, nil }
		})
		awaitNode(t, c, ids[i], connected)
	}
	gid := c.Do("POST", "/api/entry-groups", map[string]any{"name": "恢复入口组", "members": ids}, 201)["id"].(string)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "123", "token": "private"}, 201)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account["id"].(string)), 200)
	c.Do("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": ids[0], "ipv4": "223.5.5.5", "confirm_inbound": true}, 202)
	awaitDNSStage(t, c, gid, panel.EntryWritten)
	failRead.Store(true)
	c.Do("POST", "/api/entry-groups/"+gid+"/switch", map[string]any{"active_node_id": ids[0], "ipv4": "223.5.5.5", "confirm_inbound": true, "expected_version": 1}, 202)
	backup := filepath.Join(h.Dir, "pending.db")
	if err := h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		n.Disconnect()
	}
	record.Value = "8.8.8.8"
	fake.Set("example.com", record)
	failRead.Store(false)
	if err := h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	if g := c.Do("GET", "/api/recovery", nil, 200)["entry_groups"].([]any)[0].(map[string]any); g["verified"] != false {
		t.Fatal("缺失节点仍放行", g)
	}
	nodes[0].Restore()
	nodes[0].Restart()
	awaitRevision(t, c, ids[0], 0)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	c.Do("POST", "/api/entry-groups/"+gid+"/switch", map[string]any{"active_node_id": ids[0], "ipv4": "223.5.5.5", "confirm_inbound": true, "expected_version": 2}, 409)
	nodes[1].Restore()
	nodes[1].Restart()
	awaitRevision(t, c, ids[1], 0)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	pub := publication(c, gid)
	if pub["expected_value"] != "8.8.8.8" || pub["active_node_id"] != ids[1] || pub["desired_version"] != float64(3) {
		t.Fatal("旧队列覆盖了实际DNS或没有提高期望版本", pub)
	}
	// 核对前如果发生过回写，读回值和采纳结果会变成旧地址，此断言会失败。
	managed := c.Do("GET", "/api/entry-groups/"+gid+"/managed-record", nil, 200)
	if managed["current_value"] != "8.8.8.8" {
		t.Fatal("实际DNS未保留", managed)
	}
	c.Do("POST", "/api/entry-groups/"+gid+"/switch", map[string]any{"active_node_id": ids[0], "ipv4": "223.5.5.5", "confirm_inbound": true, "expected_version": 3}, 202)
	awaitDNSStage(t, c, gid, panel.EntryWritten)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	if pub := publication(c, gid); pub["desired_version"] != float64(4) {
		t.Fatal("重复核对干扰了已放行入口组", pub)
	}
}

// 备份之后的未知动作必须可通过既有核对/解除接口继续管理，不能成为永久孤儿冻结。
func TestRestorePostBackupUnconfirmedActionCanBeReleased(t *testing.T) {
	f := newRotationFixture(t, false)
	backup := filepath.Join(f.h.Dir, "before-action.db")
	if err := f.h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	for i := 1; i <= 5; i++ {
		advanceHealthyRotation(t, f, time.Minute)
		awaitRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(i) })
	}
	original := awaitRotation(t, f.c, f.path, "unconfirmed")
	f.n.Disconnect()
	if err := f.h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	f.n.Restore()
	f.n.Restart()
	awaitRevision(t, f.c, f.id, 0)
	f.c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	v := f.c.Do("GET", f.path, nil, 200)
	action, ok := v["action"].(map[string]any)
	if !ok || action["id"] != original["id"] || v["can_unfreeze"] != true {
		t.Fatal("恢复动作不可管理", v)
	}
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": action["id"]}, 202)
	awaitRotation(t, f.c, f.path, "released")
	assertRotationQuota(t, f, 1, 1)
}

// 同序号终态恢复仍要保留观察已完成的证据，否则核对/解除永久409。
func TestRestoreExistingUnconfirmedKeepsObservationComplete(t *testing.T) {
	f := newRotationFixture(t, false)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	for i := 1; i <= 5; i++ {
		advanceHealthyRotation(t, f, time.Minute)
		awaitRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(i) })
	}
	awaitRotation(t, f.c, f.path, "unconfirmed")
	backup := filepath.Join(f.h.Dir, "completed.db")
	if err := f.h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.n.Disconnect()
	if err := f.h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	f.n.Restore()
	f.n.Restart()
	awaitRevision(t, f.c, f.id, 0)
	f.c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	if v := f.c.Do("GET", f.path, nil, 200); v["can_unfreeze"] != true {
		t.Fatal("终态观察证据被抹除", v)
	}
}

func TestRestoreIncompleteHistoryBlocksAutomaticUntilProtectionExpires(t *testing.T) {
	h, c := selfCheckBrowser(t)
	sites := &domesticSites{}
	legacy := &legacyRotationHistory{}
	f := rotationFixtureAt(t, h, c, false, sites.dependencies, func(_ *node.Config, d *node.Dependencies) {
		legacy.RotationStore = d.Store.(node.RotationStore)
		d.RotationStore = legacy
	})
	gid := entryGroup(c, f.id)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	snapshot := filepath.Join(h.Dir, "missing-history.db")
	if err := h.Snapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	f.n.Disconnect()
	h.Clock.Advance(time.Hour)
	legacy.omit.Store(true)
	if err := h.RestoreSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	f.c = h.Client()
	login(f.c)
	f.n.Restore()
	f.n.Restart()
	awaitRevision(t, f.c, f.id, 0)
	f.c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	f.c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	f.c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, f.c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, h, f.c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, h, f.c, f.id, 3, "blocked", 3)
	tickPolicy(t, f)
	if v := f.c.Do("GET", f.path, nil, 200); v["action"] != nil {
		t.Fatal("保护窗口内签发自动许可", v)
	}
	// 手动操作允许绕过缺历史保护，但不允许绕过尚未核对的节点/组。
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	for i := 1; i <= 5; i++ {
		advanceHealthyRotation(t, f, time.Minute)
		awaitRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(i) })
	}
	a := awaitRotation(t, f.c, f.path, "unconfirmed")
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	awaitRotation(t, f.c, f.path, "released")
	f.n.Disconnect()
	h.Clock.Advance(24 * time.Hour)
	login(f.c)
	f.n.Restore()
	f.n.Restart()
	awaitNode(t, f.c, f.id, connected)
	// 下一轮仍异常，保护已届满，应恢复自动申请。
	advanceHealthyRotation(t, f, 10*time.Second)
	tickPolicy(t, f)
	a = awaitRotation(t, f.c, f.path, "observing")
	if a["origin"] != "automatic" {
		t.Fatal("保护期满未恢复自动申请", a)
	}
}

type legacyRotationHistory struct {
	node.RotationStore
	omit atomic.Bool
}

func (s *legacyRotationHistory) LoadRotation(ctx context.Context) ([]byte, error) {
	data, err := s.RotationStore.LoadRotation(ctx)
	if err != nil || !s.omit.Load() {
		return data, err
	}
	var journal map[string]any
	if err = json.Unmarshal(data, &journal); err != nil {
		return nil, err
	}
	delete(journal, "history_since")
	delete(journal, "attempted_at")
	return json.Marshal(journal)
}

func TestRestoreDiscardsHistoricalBlockedDecision(t *testing.T) {
	h, c := selfCheckBrowser(t)
	sites := &domesticSites{}
	f := rotationFixtureAt(t, h, c, false, sites.dependencies)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, h, c, f.id, 3, "blocked", 3)
	snapshot := filepath.Join(h.Dir, "blocked.db")
	if err := h.Snapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	f.n.Disconnect()
	h.Clock.Advance(time.Second)
	if err := h.RestoreSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	f.c = h.Client()
	login(f.c)
	v := f.c.Do("GET", "/api/nodes/"+f.id+"/self-check", nil, 200)
	if v["state"] != "unknown" || v["consecutive_abnormal"] != float64(0) {
		t.Fatal("历史被墙判定仍可驱动自动动作", v)
	}
}

func TestRestoreAllowsNewNodesAndGroupsWithoutInheritedPause(t *testing.T) {
	fake := clusterharness.NewDNSPod()
	fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "223.5.5.5", TTL: 60, Enabled: true})
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = fake })
	c := h.Client()
	setup(c)
	login(c)
	snapshot := filepath.Join(h.Dir, "empty.db")
	if err := h.Snapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := h.RestoreSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	created := addNode(c, "恢复后新建")
	id := created["id"].(string)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return "223.5.5.5", nil }
	})
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "123", "token": "private"}, 201)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account["id"].(string)), 200)
	c.Do("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": id, "ipv4": "223.5.5.5", "confirm_inbound": true}, 202)
	awaitDNSStage(t, c, gid, panel.EntryWritten)
}

func TestRestoreFrozenActionOlderThanDayCanStillBeReleased(t *testing.T) {
	f := newRotationFixture(t, false)
	backup := filepath.Join(f.h.Dir, "before-old-action.db")
	if err := f.h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	for i := 1; i <= 5; i++ {
		advanceHealthyRotation(t, f, time.Minute)
		awaitRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(i) })
	}
	a := awaitRotation(t, f.c, f.path, "unconfirmed")
	f.n.Disconnect()
	f.h.Clock.Advance(25 * time.Hour)
	if err := f.h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	f.n.Restore()
	f.n.Restart()
	awaitRevision(t, f.c, f.id, 0)
	v := f.c.Do("GET", f.path, nil, 200)
	if v["action"] == nil || v["action"].(map[string]any)["id"] != a["id"] {
		t.Fatal("超过24小时的冻结动作丢失", v)
	}
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	awaitRotation(t, f.c, f.path, "released")
	assertRotationQuota(t, f, 0, 0)
}
