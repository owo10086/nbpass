package clusterharness_test

import (
	"nbpass/internal/clusterharness"
	"net"
	"testing"
	"time"
)

func operations(t *testing.T, c *clusterharness.Client) map[string]any {
	t.Helper()
	return c.Do("GET", "/api/overview", nil, 200)
}

func TestOperationsEmptyAndPendingRulesAreNotHealthy(t *testing.T) {
	_, c := nodeBrowser(t)
	view := operations(t, c)
	if view["generated_at"] == nil || view["rules"] == nil {
		t.Fatalf("缺少概览契约: %v", view)
	}
	n := addNode(c, "待接入入口")
	body := directBody("", 12001, "8.8.8.8", 443)
	delete(body, "entry_group_id")
	body["entry_node_id"] = n["id"]
	c.Do("POST", "/api/rules", body, 201)
	view = operations(t, c)
	counts := view["counts"].(map[string]any)
	if counts["unknown_rules"] != float64(1) || counts["abnormal_rules"] != float64(0) {
		t.Fatalf("待接入错误算作正常/异常: %v", view)
	}
	body["enabled"] = false
	c.Do("POST", "/api/rules", func() map[string]any {
		b := directBody("", 12002, "8.8.8.8", 443)
		delete(b, "entry_group_id")
		b["entry_node_id"] = n["id"]
		b["enabled"] = false
		return b
	}(), 201)
	if operations(t, c)["counts"].(map[string]any)["unknown_rules"] != float64(1) {
		t.Fatal("停用规则计入异常统计")
	}
}

func TestOperationsKeepsRuleFailureSeparateFromMissingEvidence(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "逐规则入口")
	real := h.StartNode(n["installation_ticket"].(string))
	id := n["id"].(string)
	awaitNode(t, c, id, connected)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	add := func(port int) string {
		b := directBody("", port, "127.0.0.1", echoTarget(t, "O:"))
		delete(b, "entry_group_id")
		b["entry_node_id"] = id
		return c.Do("POST", "/api/rules", b, 201)["id"].(string)
	}
	good := add(h.ReserveTCPPort())
	bad := add(occupied.Addr().(*net.TCPAddr).Port)
	awaitRule(t, c, bad, "同步失败")
	var view map[string]any
	for i := 0; i < 20; i++ {
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
		view = operations(t, c)
		if view["counts"].(map[string]any)["abnormal_rules"] == float64(1) {
			break
		}
	}
	if view["counts"].(map[string]any)["abnormal_rules"] != float64(1) {
		t.Fatalf("监听失败没有准确归因: %v", view)
	}
	for _, v := range view["rules"].([]any) {
		r := v.(map[string]any)
		if r["id"] == good && r["status"] != "no_known_issue" {
			t.Fatalf("其他规则污染: %v", r)
		}
	}
	real.Disconnect()
	awaitNode(t, c, id, func(r map[string]any) bool { return !connected(r) })
	view = operations(t, c)
	if view["counts"].(map[string]any)["unknown_rules"] != float64(1) {
		t.Fatalf("控制失联抹掉已知失败或假报正常: %v", view)
	}
}

func TestOperationsPublicationSeparatesConfirmedAddressAndRedundancy(t *testing.T) {
	f := offlineEntry(t, true)
	find := func() map[string]any {
		for _, value := range operations(t, f.c)["groups"].([]any) {
			g := value.(map[string]any)
			if g["id"] == f.group {
				return g
			}
		}
		t.Fatal("缺少入口组")
		return nil
	}
	g := find()
	if g["confirmed_ip"] != "8.8.8.8" || g["desired_node"] != f.a {
		t.Fatalf("服务商确认与期望未展示: %v", g)
	}
	f.c.Do("PUT", "/api/nodes/"+f.b+"/maintenance", map[string]bool{"enabled": true}, 204)
	f.advance(t, 11)
	g = find()
	if g["redundancy"] != "冗余下降" {
		t.Fatalf("备用维护未标记冗余下降: %v", g)
	}
	if g["confirmed_node"] != f.a {
		t.Fatalf("备用失败污染活动入口: %v", g)
	}
}
