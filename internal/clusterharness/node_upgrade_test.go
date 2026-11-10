package clusterharness_test

import (
	"context"
	"fmt"
	"io"
	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpgradeRequiresOnlineUpdaterAndPersistsBoundManifest(t *testing.T) {
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("node-binary")), nil }
	})
	c := h.Client()
	setup(c)
	login(c)
	n := addNode(c, "升级节点")
	id := n["id"].(string)
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 409)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 409)
	// 没有 helper 的节点不会被错误地置为维护。
	if nodeRow(t, c, id)["status"] == "maintenance" {
		t.Fatal("无更新服务仍进入维护")
	}
}

func upgradeRig(t *testing.T, env clusterharness.UpdateEnvironment) (*clusterharness.Harness, *clusterharness.Client, *clusterharness.Node, string) {
	t.Helper()
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("embedded-node-binary")), nil }
	})
	c := h.Client()
	setup(c)
	login(c)
	created := addNode(c, "旧版本节点")
	id := created["id"].(string)
	var n *clusterharness.Node
	var updater *clusterharness.UpdateService
	env.Switch = func() {
		version := node.SoftwareVersion
		if env.FailStartup || env.FailBusiness {
			version = "0.0.9"
		}
		n.RestartImage(version, []byte("embedded-node-binary"))
	}
	n = h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.SoftwareVersion = "0.0.9"
		updater = h.UpdateService(d.Store, env)
		d.Updater = updater
	})
	t.Cleanup(updater.Close)
	awaitNode(t, c, id, connected)
	return h, c, n, id
}
func awaitUpgrade(t *testing.T, c *clusterharness.Client, id, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v := c.Do("GET", "/api/nodes/"+id+"/upgrade", nil, 200)
		if p, ok := v["upgrade"].(map[string]any); ok && p["state"] == state {
			return p
		}
		cTime := time.NewTimer(5 * time.Millisecond)
		<-cTime.C
	}
	t.Fatalf("升级未达到 %s: %v", state, c.Do("GET", "/api/nodes/"+id+"/upgrade", nil, 200))
	return nil
}
func driveUpgrade(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id, state string) map[string]any {
	t.Helper()
	for range 35 {
		h.Clock.Advance(time.Second)
		awaitControlIdle(t, h)
		v := c.Do("GET", "/api/nodes/"+id+"/upgrade", nil, 200)
		if p, ok := v["upgrade"].(map[string]any); ok && p["state"] == state {
			return p
		}
		time.Sleep(3 * time.Millisecond)
	}
	return awaitUpgrade(t, c, id, state)
}
func TestUpgradeRejectsEveryManifestBindingAndAlteredArtifactThroughHTTP(t *testing.T) {
	for _, field := range []string{"task", "version", "platform", "size", "hash", "body"} {
		t.Run(field, func(t *testing.T) {
			h, c, _, id := upgradeRig(t, clusterharness.UpdateEnvironment{Corrupt: field})
			if nodeRow(t, c, id)["version_mismatch"] != true {
				t.Fatal("未标出版本不一致")
			}
			task := c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)["task"].(map[string]any)
			m := task["manifest"].(map[string]any)
			if m["version"] != node.SoftwareVersion || m["platform"] != "linux/amd64" || m["size"] != float64(len("embedded-node-binary")) || len(m["sha256"].(string)) != 64 {
				t.Fatalf("清单未完整绑定: %v", m)
			}
			driveUpgrade(t, h, c, id, "failed")
			if nodeRow(t, c, id)["software_version"] != "0.0.9" {
				t.Fatal("不符的升级包仍切换程序")
			}
		})
	}
}
func TestUpgradeReadinessAndRollbackThroughHTTP(t *testing.T) {
	for _, failure := range []string{"none", "start", "business"} {
		t.Run(failure, func(t *testing.T) {
			gate := make(chan struct{})
			h, c, _, id := upgradeRig(t, clusterharness.UpdateEnvironment{Gate: gate, FailStartup: failure == "start", FailBusiness: failure == "business"})
			c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)
			c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 409)
			if nodeRow(t, c, id)["status"] != "maintenance" {
				t.Fatal("升级未进入维护")
			}
			close(gate)
			want := "ready"
			if failure != "none" {
				want = "rolled_back"
			}
			p := driveUpgrade(t, h, c, id, want)
			steps := fmt.Sprint(p["steps"])
			for _, step := range []string{"downloading", "verifying", "switching"} {
				if !strings.Contains(steps, step) {
					t.Fatalf("进度缺少 %s: %v", step, p)
				}
			}
			if want == "ready" {
				if nodeRow(t, c, id)["version_mismatch"] != false {
					t.Fatal("升级后仍显示版本不一致")
				}
			} else if !strings.Contains(steps, "rolling_back") {
				t.Fatal("未展示回滚")
			}
		})
	}
}

func TestUpgradeMaintenanceSuppressesFailoverAndRestartsOfflineEvidence(t *testing.T) {
	// Reuse ticket 24's real active entry, candidate and domestic checker environment.
	var service *clusterharness.UpdateService
	f := offlineEntry(t, true, func(d *panel.Dependencies) {
		d.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("node")), nil }
	})
	// Install an updater on the real active node by restarting its runtime environment.
	gate := make(chan struct{})
	defer close(gate)
	service = f.h.AttachUpdater(f.active, clusterharness.UpdateEnvironment{Gate: gate})
	_ = service
	// 重启后的连接须已上报升级能力，旧连接的在线状态不能作为升级前置条件。
	awaitNode(t, f.c, f.a, func(v map[string]any) bool {
		return connected(v) && v["upgrade_capable"] == true
	})
	f.c.Do("POST", "/api/nodes/"+f.a+"/upgrade", nil, 202)
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.active.Disconnect()
	awaitNode(t, f.c, f.a, func(v map[string]any) bool { return v["connected"] == false })
	f.advance(t, 590)
	if nodeRow(t, f.c, f.a)["status"] != "maintenance" || publication(f.c, f.group)["active_node_id"] != f.a {
		t.Fatal("维护期内发生切换")
	}
	if checks := f.c.Do("GET", "/api/entry-groups/"+f.group+"/offline-checks", nil, 200)["checks"].([]any); len(checks) != 0 {
		t.Fatal("维护离线积累了故障确认")
	}
	f.advance(t, 11)
	if nodeRow(t, f.c, f.a)["status"] == "offline" {
		t.Fatal("维护到期直接沿用累计离线时间")
	}
	f.advance(t, 89)
	if publication(f.c, f.group)["active_node_id"] != f.a {
		t.Fatal("新观察窗口未满就切走")
	}
	f.advance(t, 2)
	awaitNode(t, f.c, f.a, func(v map[string]any) bool { return v["status"] == "offline" })
	// A fresh checker round is required, then the existing coordinator can switch.
	for range 6 {
		f.advance(t, 1)
		time.Sleep(5 * time.Millisecond)
	}
	awaitOfflineCheck(t, f, "submitted")
	awaitDNSStage(t, f.c, f.group, "服务商已写入")
	if publication(f.c, f.group)["active_node_id"] != f.b {
		t.Fatal("期满后未按当前证据重评")
	}
}

func TestUpgradeRollbackPreservesRevocationAndRotationDedupThroughHTTP(t *testing.T) {
	f := newRotationFixture(t, false)
	f.h.Dependencies.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("node-binary")), nil }
	// Reopen panel so the embedded artifact dependency takes effect.
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	f.n.Restart()
	awaitNode(t, f.c, f.id, connected)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	a := f.finishObservation(t)
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	awaitRotation(t, f.c, f.path, "released")
	group := entryGroup(f.c, f.id)["id"].(string)
	body := directBody(group, f.h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "target:"))
	rule := f.c.Do("POST", "/api/rules", body, 201)
	awaitRule(t, f.c, rule["id"].(string), "正常")
	gate := make(chan struct{})
	var gateOnce sync.Once
	defer gateOnce.Do(func() { close(gate) })
	f.h.AttachUpdater(f.n, clusterharness.UpdateEnvironment{Gate: gate, FailBusiness: true, Switch: func() { f.n.Restart() }})
	awaitNode(t, f.c, f.id, connected)
	f.c.Do("POST", "/api/nodes/"+f.id+"/upgrade", nil, 202)
	// Revoke after upgrade preparation. A configuration backup from upgrade start would resurrect it.
	body["enabled"] = false
	f.c.Do("PUT", "/api/rules/"+rule["id"].(string), body, 200)
	awaitRule(t, f.c, rule["id"].(string), "正常")
	gateOnce.Do(func() { close(gate) })
	driveUpgrade(t, f.h, f.c, f.id, "rolled_back")
	row := awaitRule(t, f.c, rule["id"].(string), "正常")
	if row["enabled"] != false {
		t.Fatal("已撤销规则被回滚恢复")
	}
	latest := f.c.Do("GET", f.path, nil, 200)["action"].(map[string]any)
	if latest["id"] != a["id"] || latest["state"] != "released" {
		t.Fatal("换 IP 去重动作丢失")
	}
	assertRotationQuota(t, f, 1, 1)
	if f.calls.Load() != 1 {
		t.Fatal("回滚重复调用换 IP 接口")
	}
}

func TestUpgradeResourcesAreBoundToNodeAndExpireWithMaintenance(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	h, c, n, id := upgradeRig(t, clusterharness.UpdateEnvironment{Gate: gate})
	task := c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)["task"].(map[string]any)
	taskID := task["manifest"].(map[string]any)["task_id"].(string)
	url := "/node-upgrades/" + taskID + "/manifest"
	response := c.Request("GET", url, nil, nil)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("资源允许匿名获取")
	}
	store, err := n.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	response = c.Request("GET", url, nil, map[string]string{"Authorization": "Bearer " + credential.Token})
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("当前执行节点无法读取清单")
	}
	other := addNode(c, "其他节点")
	otherNode := h.StartNode(other["installation_ticket"].(string))
	awaitNode(t, c, other["id"].(string), connected)
	otherStore, _ := otherNode.Credentials()
	otherCredential, _ := otherStore.Load(context.Background())
	response = c.Request("GET", url, nil, map[string]string{"Authorization": "Bearer " + otherCredential.Token})
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("其他节点可读取升级任务")
	}
	// A server restart must retain the same immutable task and maintenance deadline.
	n.Disconnect()
	otherNode.Disconnect()
	if err = h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	saved := c.Do("GET", "/api/nodes/"+id+"/upgrade", nil, 200)["upgrade"].(map[string]any)
	if saved["manifest"].(map[string]any)["task_id"] != taskID {
		t.Fatal("面板重启丢失任务")
	}
	h.Clock.Advance(10 * time.Minute)
	response = c.Request("GET", url, nil, map[string]string{"Authorization": "Bearer " + credential.Token})
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("维护期限后仍可开始安装任务")
	}
}

func TestUpgradeMaintenanceBlocksAutomaticIPRotation(t *testing.T) {
	h, c := selfCheckBrowser(t)
	h.Dependencies.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("node")), nil }
	// Dependency changes enter the production panel only through reopening it.
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	sites := &domesticSites{}
	f := rotationFixtureAt(t, h, c, false, sites.dependencies)
	group := entryGroup(c, f.id)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+group+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	gate := make(chan struct{})
	defer close(gate)
	h.AttachUpdater(f.n, clusterharness.UpdateEnvironment{Gate: gate})
	awaitNode(t, c, f.id, connected)
	c.Do("POST", "/api/nodes/"+f.id+"/upgrade", nil, 202)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, h, c, f.id, 3, "blocked", 3)
	for range 3 {
		tickPolicy(t, f)
		time.Sleep(5 * time.Millisecond)
	}
	v := c.Do("GET", f.path, nil, 200)
	if v["action"] != nil || f.calls.Load() != 0 {
		t.Fatal("维护期内自动换 IP")
	}
	if nodeRow(t, c, f.id)["status"] != "maintenance" {
		t.Fatal("维护期限未保持")
	}
}

func TestUpgradeCapabilityRecoversWhenUpdaterStartsAfterNode(t *testing.T) {
	var unavailable atomic.Bool
	unavailable.Store(true)
	h, c, _, id := upgradeRig(t, clusterharness.UpdateEnvironment{Unavailable: &unavailable})
	if nodeRow(t, c, id)["upgrade_capable"] != false {
		t.Fatal("尚未启动的更新器被标为可升级")
	}
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 409)
	unavailable.Store(false)
	for range 15 {
		h.Clock.Advance(time.Second)
		awaitControlIdle(t, h)
		time.Sleep(3 * time.Millisecond)
	}
	awaitNode(t, c, id, func(v map[string]any) bool { return v["upgrade_capable"] == true })
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)
	driveUpgrade(t, h, c, id, "ready")
}

func TestUpgradeLostUpdaterResponseDoesNotEndMaintenanceOrDeclareFailure(t *testing.T) {
	gate := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(gate) })
	h, c, _, id := upgradeRig(t, clusterharness.UpdateEnvironment{LoseSubmitResponse: true, Gate: gate})
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)
	driveUpgrade(t, h, c, id, "verifying")
	if nodeRow(t, c, id)["status"] != "maintenance" {
		t.Fatal("响应丢失提前解除维护")
	}
	once.Do(func() { close(gate) })
	driveUpgrade(t, h, c, id, "ready")
}
