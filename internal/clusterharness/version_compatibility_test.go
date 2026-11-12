package clusterharness_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"nbpass/internal/clusterharness"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

func TestExplicitCapabilitiesGateUpgradeEvenWhenLegacyFlagIsSet(t *testing.T) {
	h, c, n, id := upgradeRig(t, clusterharness.UpdateEnvironment{})
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		r.ProtocolVersion = 1
		r.Compatibility = &controlpb.Compatibility{ControlMin: 1, ControlMax: 2, TunnelMin: 1, TunnelMax: 1, StateMin: 1, StateMax: 1, StateWrite: 1, Capabilities: []string{"future-unsupported"}}
	})
	// Capability changes are admitted on a new control session.
	// Keep the real updater available, so denying the command proves capability gating.
	n.Restart()
	awaitNode(t, c, id, connected)
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 409)
	if nodeRow(t, c, id)["status"] == "maintenance" {
		t.Fatal("未支持升级仍进入维护")
	}
	view := c.Do("GET", "/api/nodes/"+id+"/compatibility", nil, 200)
	if len(view["capabilities"].([]any)) != 0 {
		t.Fatal("启用了对端未支持的能力")
	}
}

func TestStateFormatIncompatibleNodeRetainsControlButCannotUpgrade(t *testing.T) {
	h, c, n, id := upgradeRig(t, clusterharness.UpdateEnvironment{})
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		r.Compatibility = compatibility.Current()
		r.Compatibility.StateMin, r.Compatibility.StateMax, r.Compatibility.StateWrite = 1, 1, 1
	})
	n.Restart()
	awaitNode(t, c, id, connected)
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 409)
}

func TestPendingUpgradeIsNotDispatchedToIncompatibleReconnectedNode(t *testing.T) {
	h, c, n, id := upgradeRig(t, clusterharness.UpdateEnvironment{})
	h.DropUpgradeCommands(true)
	c.Do("POST", "/api/nodes/"+id+"/upgrade", nil, 202)
	instance := nodeRow(t, c, id)["process_instance"]
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		r.Compatibility = compatibility.Current()
		r.Compatibility.Capabilities = []string{"dns_watch"}
	})
	n.Stop()
	h.DropUpgradeCommands(false)
	n.Restart()
	awaitNode(t, c, id, func(v map[string]any) bool { return connected(v) && v["process_instance"] != instance })
	for range 20 {
		h.Clock.Advance(time.Second)
		awaitControlIdle(t, h)
		time.Sleep(2 * time.Millisecond)
	}
	v := c.Do("GET", "/api/nodes/"+id+"/upgrade", nil, 200)["upgrade"].(map[string]any)
	if v["state"] != "queued" || nodeRow(t, c, id)["software_version"] != "0.0.9" {
		t.Fatal("已排队任务绕过新会话能力门禁", v)
	}
}

func TestUnsupportedTunnelAuthorizationDoesNotEnableNewRule(t *testing.T) {
	h, c := nodeBrowser(t)
	entry, exit := addNode(c, "无新能力入口"), addNode(c, "出口")
	entryID, exitID := entry["id"].(string), exit["id"].(string)
	var mu sync.Mutex
	var received *controlpb.ConfigSnapshot
	h.BeforeControlSend(func(_ context.Context, id string, m *controlpb.PanelMessage) error {
		if id == entryID && m.Snapshot != nil {
			mu.Lock()
			received = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
			mu.Unlock()
		}
		return nil
	})
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		if r.NodeId == entryID {
			r.Compatibility = compatibility.Current()
			r.Compatibility.Capabilities = []string{"upgrade", "future"}
		}
	})
	h.StartNode(entry["installation_ticket"].(string))
	h.StartNode(exit["installation_ticket"].(string))
	awaitNode(t, c, entryID, connected)
	awaitNode(t, c, exitID, connected)
	c.Do("PUT", "/api/nodes/"+exitID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{exitID}, "strategy": "primary_backup"}, 201)
	body := directBody(entryGroup(c, entryID)["id"].(string), h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "no:"))
	body["kind"], body["exit_group_id"] = "tunnel", g["id"]
	rule := c.Do("POST", "/api/rules", body, 201)
	for range 12 {
		h.Clock.Advance(time.Second)
		awaitControlIdle(t, h)
	}
	// 控制流空闲不代表十秒入口检查已完成；沿用真实采样等待助手。
	sampleEntryState(t, h, c, body["entry_group_id"].(string), entryID)
	row := awaitRule(t, c, rule["id"].(string), "整条阻塞（需升级服务器）")
	blockers := row["configuration_blockers"].([]any)
	if len(blockers) != 1 || blockers[0].(map[string]any)["node_id"] != entryID || blockers[0].(map[string]any)["capability"] != "tunnel_authorization" {
		t.Fatal("缺能力未定位到端口和服务器", row)
	}
	state := row["entry_states"].([]any)[0].(map[string]any)
	found := false
	for _, reason := range state["readiness_reasons"].([]any) {
		if reason.(map[string]any)["code"] == "capability_missing" {
			found = true
		}
	}
	if !found {
		t.Fatal("入口就绪没有保留缺能力原因", state)
	}
	port := h.ReserveTCPPort()
	direct := c.Do("POST", "/api/rules", directBody(entryGroup(c, entryID)["id"].(string), port, "127.0.0.1", echoTarget(t, "ordinary:")), 201)
	awaitRule(t, c, direct["id"].(string), "正常")
	tcpExchange(t, port, "ordinary:")
	mu.Lock()
	defer mu.Unlock()
	if received == nil || len(received.Rules) != 1 || received.Rules[0].Id != direct["id"] || len(received.ExitGroups) != 0 {
		t.Fatal("旧节点收到被拦规则或无归属的出口凭据", received)
	}
}

func TestCapabilitiesBackupUpgradeRequiresNewDurableConfirmation(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b, old := addNode(c, "入口"), addNode(c, "主出口"), addNode(c, "旧备用")
	aID, bID, oldID := a["id"].(string), b["id"].(string), old["id"].(string)
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		if r.NodeId == oldID {
			r.Compatibility = compatibility.Current()
			r.Compatibility.Capabilities = []string{"exit_failover"}
		}
	})
	var mu sync.Mutex
	latest := map[string]*controlpb.ConfigSnapshot{}
	var hold atomic.Bool
	release := make(chan struct{})
	defer func() {
		if hold.Swap(false) {
			close(release)
		}
	}()
	h.BeforeControlSend(func(ctx context.Context, id string, m *controlpb.PanelMessage) error {
		if m.Snapshot == nil {
			return nil
		}
		mu.Lock()
		latest[id] = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
		mu.Unlock()
		if id == oldID && hold.Load() && len(m.Snapshot.GetTunnelServer().GetGrants()) > 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	h.StartNode(a["installation_ticket"].(string))
	primary := h.StartNode(b["installation_ticket"].(string))
	oldNode := h.StartNode(old["installation_ticket"].(string))
	for _, id := range []string{aID, bID, oldID} {
		awaitNode(t, c, id, connected)
	}
	for _, id := range []string{bID, oldID} {
		c.Do("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
	}
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "主备", "members": []string{bID, oldID}, "strategy": "primary_backup"}, 201)
	port := h.ReserveTCPPort()
	body := directBody(entryGroup(c, aID)["id"].(string), port, "127.0.0.1", echoTarget(t, "backup:"))
	body["kind"], body["exit_group_id"] = "tunnel", g["id"]
	rule := c.Do("POST", "/api/rules", body, 201)
	awaitRule(t, c, rule["id"].(string), "可运行但备用成员能力不足")
	// Capability state is available before apply confirmation; wait for real traffic.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		s := latest[aID]
		ready := len(s.GetRules()) == 1
		mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("主出口未被确认")
		}
		time.Sleep(10 * time.Millisecond)
	}
	tcpExchange(t, port, "backup:")
	mu.Lock()
	oldSecrets := len(latest[oldID].GetTunnelServer().GetGrants()) + len(latest[oldID].GetTunnelServer().GetPeers())
	generation := latest[aID].GetRules()[0].AuthorizationGeneration
	mu.Unlock()
	if oldSecrets != 0 {
		t.Fatal("缺能力备用收到授权或凭据")
	}
	// 已确认主出口即使控制离线，也应保持缓存业务。备用升级不能
	// 推进整条规则代次，撤掉主出口确认或入口监听。
	primary.Disconnect()
	oldNode.Stop()
	h.TransformControlReports(nil)
	hold.Store(true)
	oldNode.Restart()
	awaitNode(t, c, oldID, connected)
	mu.Lock()
	active := proto.Clone(latest[aID]).(*controlpb.ConfigSnapshot)
	mu.Unlock()
	if len(active.Rules) != 1 || active.Rules[0].AuthorizationGeneration != generation {
		t.Fatalf("备用升级撤掉了仍可运行的主路径：规则数=%d", len(active.Rules))
	}
	for _, r := range active.GetRules() {
		for _, id := range r.AuthorizedExitIds {
			if id == oldID {
				t.Fatal("未持久应用确认就加入备用")
			}
		}
	}
	tcpExchange(t, port, "backup:")
	hold.Store(false)
	close(release)
	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		s := latest[aID]
		ready := len(s.GetRules()) == 1 && len(s.Rules[0].AuthorizedExitIds) == 2
		mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("备用升级后未重新确认加入")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitRule(t, c, rule["id"].(string), "正常")
	mu.Lock()
	defer mu.Unlock()
	if got := latest[aID].ExitGroups[0].Exits; len(got) != 2 || got[0].NodeId != bID || got[1].NodeId != oldID {
		t.Fatal("升级后丢失原组顺序", got)
	}
}

func TestCompatibilityQueryPersistsWithoutBlockingUnreferencedNodeDeletion(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "兼容信息")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/compatibility"
	h.Client().Do("GET", path, nil, 401)
	c.Do("GET", "/api/nodes/missing/compatibility", nil, 404)
	if c.Do("GET", path, nil, 200)["node"] != nil {
		t.Fatal("未接入节点伪造兼容声明")
	}
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	view := c.Do("GET", path, nil, 200)
	if view["control_protocol"] != float64(2) {
		t.Fatal("当前协议声明未保存")
	}
	n.Stop()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	if c.Do("GET", path, nil, 200)["control_protocol"] != float64(2) {
		t.Fatal("面板重启丢失兼容声明")
	}
	c.Do("DELETE", "/api/nodes/"+id, nil, 204)
	c.Do("GET", path, nil, 404)
}

func TestPreviousControlNodeReinstallsAndTunnelsWithCurrentNode(t *testing.T) {
	for _, oldEntry := range []bool{true, false} {
		t.Run(map[bool]string{true: "old_entry", false: "old_exit"}[oldEntry], func(t *testing.T) {
			h := clusterharness.New(t, func(d *panel.Dependencies) {
				d.Passwords = nodePasswords{}
				d.NodeBinary = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("embedded-node-binary")), nil }
			})
			c := h.Client()
			setup(c)
			login(c)
			entry, exit := addNode(c, "入口"), addNode(c, "出口")
			entryID, exitID := entry["id"].(string), exit["id"].(string)
			oldID := exitID
			if oldEntry {
				oldID = entryID
			}
			h.TransformControlReports(func(r *controlpb.NodeReport) {
				if r.NodeId == oldID && r.SoftwareVersion == "0.0.9" {
					r.ProtocolVersion = 1
					r.Compatibility = nil
				}
			})
			var old *clusterharness.Node
			option := func(id string) func(*node.Config, *node.Dependencies) {
				return func(_ *node.Config, d *node.Dependencies) {
					if id != oldID {
						return
					}
					d.SoftwareVersion = "0.0.9"
					d.Updater = h.UpdateService(d.Store, clusterharness.UpdateEnvironment{Switch: func() { old.RestartImage(node.SoftwareVersion, []byte("embedded-node-binary")) }})
				}
			}
			entryNode := h.StartNode(entry["installation_ticket"].(string), option(entryID), func(_ *node.Config, d *node.Dependencies) {
				d.Forwarding.DialTarget = func(context.Context, string) (net.Conn, error) { return nil, errors.New("入口不得直拨目标") }
			})
			exitNode := h.StartNode(exit["installation_ticket"].(string), option(exitID))
			old = exitNode
			if oldEntry {
				old = entryNode
			}
			awaitNode(t, c, entryID, connected)
			awaitNode(t, c, exitID, connected)
			legacy := c.Do("GET", "/api/nodes/"+oldID+"/compatibility", nil, 200)
			if legacy["control_protocol"] != float64(1) {
				t.Fatal("未识别上一版控制协议")
			}
			currentID := entryID
			if oldEntry {
				currentID = exitID
			}
			current := c.Do("GET", "/api/nodes/"+currentID+"/compatibility", nil, 200)
			if current["control_protocol"] != float64(2) {
				t.Fatal("未使用当前控制协议")
			}
			c.Do("PUT", "/api/nodes/"+exitID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
			g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "兼容出口组", "members": []string{exitID}, "strategy": "primary_backup"}, 201)
			port := h.ReserveTCPPort()
			body := directBody(entryGroup(c, entryID)["id"].(string), port, "127.0.0.1", echoTarget(t, "V:"))
			body["kind"], body["exit_group_id"] = "tunnel", g["id"]
			rule := c.Do("POST", "/api/rules", body, 201)
			awaitRule(t, c, rule["id"].(string), "正常")
			tcpExchange(t, port, "V:")
			// Protocol-1 ordinary forwarding remains valid, but its declared
			// state writer 1 cannot cross to 2 through the online updater.
			c.Do("POST", "/api/nodes/"+oldID+"/upgrade", nil, 409)
			old.RestartImage(node.SoftwareVersion, []byte("embedded-node-binary"))
			awaitNode(t, c, oldID, func(v map[string]any) bool { return connected(v) && v["software_version"] == node.SoftwareVersion })
			upgraded := c.Do("GET", "/api/nodes/"+oldID+"/compatibility", nil, 200)
			if upgraded["control_protocol"] != float64(2) {
				t.Fatal("安装后未切换当前协议")
			}
			awaitRule(t, c, rule["id"].(string), "正常")
			tcpExchange(t, port, "V:")
		})
	}
}
