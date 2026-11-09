package clusterharness_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

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
		r.Compatibility.StateMax = 2
		r.Compatibility.StateWrite = 2
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
	row := awaitRule(t, c, rule["id"].(string), "未同步")
	if row["sync_status"] == "正常" {
		t.Fatal("不支持逐流授权的入口启用了新规则")
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

func TestPreviousControlNodeUpgradesAndTunnelsWithCurrentNode(t *testing.T) {
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
			c.Do("POST", "/api/nodes/"+oldID+"/upgrade", nil, 202)
			driveUpgrade(t, h, c, oldID, "ready")
			upgraded := c.Do("GET", "/api/nodes/"+oldID+"/compatibility", nil, 200)
			if upgraded["control_protocol"] != float64(2) {
				t.Fatal("安装后未切换当前协议")
			}
			awaitRule(t, c, rule["id"].(string), "正常")
			tcpExchange(t, port, "V:")
		})
	}
}
