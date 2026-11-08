package clusterharness_test

import (
	"context"
	"net"
	"testing"
	"time"

	"nbpass/internal/node"
)

// 直连数据面的逻辑连接数必须接入探针，控制连接和目标侧连接不能重复计算。
func TestDirectConnectionsFeedProbeWhileEventsAndIPKeepWorking(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "综合入口")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.PublicIPv4 = func(context.Context) (string, error) { return "223.5.5.5", nil }
	})
	awaitNode(t, c, id, connected)
	awaitUptime(t, c, id, 0)
	gid := entryGroup(c, id)["id"].(string)
	port := h.ReserveTCPPort()
	rule := c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", echoTarget(t, "A:")), 201)
	revision := awaitRevision(t, c, id, 0)
	conn := tcpExchange(t, port, "A:")
	h.Clock.Advance(5 * time.Second)
	view := awaitUptime(t, c, id, 5)
	rt := view["realtime"].(map[string]any)
	if rt["business_connections"] != float64(1) || rt["tunnel_connections"] != float64(0) {
		t.Fatalf("真实直连连接数未接入探针或重复计数: %v", rt)
	}
	for range 40 {
		if err := n.RecordEvent("integration_event", "批量补报与配置版本隔离", 999); err != nil {
			t.Fatal(err)
		}
	}
	h.Clock.Advance(5 * time.Second)
	awaitEvents(t, h, c, "?node_id="+id+"&type=integration_event", 40)
	row := nodeRow(t, c, id)
	if row["desired_revision"] != revision || row["applied_revision"] != revision || row["config_status"] != "正常" || row["public_ipv4"] != "223.5.5.5" {
		t.Fatalf("事件或探针污染配置/IP状态: %v", row)
	}
	c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
	awaitRevision(t, c, id, revision)
	assertRevoked(t, port, conn)
	h.Clock.Advance(5 * time.Second)
	awaitProbe(t, c, id, func(v map[string]any) bool {
		rt, ok := v["realtime"].(map[string]any)
		return ok && rt["business_connections"] == float64(0)
	})
}

// 同步执行器的真实监听失败必须经持久事件队列进入事件日志。
func TestDirectApplyFailureEntersEventLog(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "失败入口")
	id := created["id"].(string)
	h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	c.Do("POST", "/api/rules", directBody(gid, occupied.Addr().(*net.TCPAddr).Port, "127.0.0.1", echoTarget(t, "A:")), 201)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	events := awaitEvents(t, h, c, "?node_id="+id+"&type=sync_failed", 1)
	if events[0].(map[string]any)["config_revision"] != nodeRow(t, c, id)["desired_revision"] {
		t.Fatalf("同步失败未关联正确配置版本: %v", events)
	}
}
