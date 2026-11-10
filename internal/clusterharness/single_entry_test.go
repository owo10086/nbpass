package clusterharness_test

import (
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

func singleEntryBody(id string, port int, target string, targetPort int) map[string]any {
	b := directBody("", port, target, targetPort)
	delete(b, "entry_group_id")
	b["entry_node_id"] = id
	return b
}

// 捕获单节点配置未下发、转换入口后监听未撤销或累计量被重置。
func TestSingleEntryDirectLifecycleAndTrafficAcrossEntryChanges(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "原入口"), addNode(c, "新入口")
	aid, bid := a["id"].(string), b["id"].(string)
	h.StartNode(a["installation_ticket"].(string))
	h.StartNode(b["installation_ticket"].(string))
	awaitNode(t, c, aid, connected)
	awaitNode(t, c, bid, connected)
	port := h.ReserveTCPPort()
	body := singleEntryBody(aid, port, "127.0.0.1", echoTarget(t, "S:"))
	rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, c, rid, "正常")
	first := tcpExchange(t, port, "S:")
	awaitTraffic(t, h, c, rid, 4, 6, float64(1))
	// 换入口同时更换端口，两个真实节点可在同一测试主机运行。
	nextPort := h.ReserveTCPPort()
	delete(body, "entry_node_id")
	body["entry_group_id"] = entryGroup(c, bid)["id"]
	body["listen_port"] = nextPort
	c.Do("PUT", "/api/rules/"+rid, body, 200)
	awaitRule(t, c, rid, "正常")
	awaitRevision(t, c, aid, 0)
	assertRevoked(t, port, first)
	tcpExchange(t, nextPort, "S:").Close()
	awaitTraffic(t, h, c, rid, 8, 12, float64(0))
	delete(body, "entry_group_id")
	body["entry_node_id"] = aid
	body["listen_port"] = port
	c.Do("PUT", "/api/rules/"+rid, body, 200)
	awaitRule(t, c, rid, "正常")
	tcpExchange(t, port, "S:").Close()
	awaitTraffic(t, h, c, rid, 12, 18, float64(0))
	body["enabled"] = false
	c.Do("PUT", "/api/rules/"+rid, body, 200)
	awaitRule(t, c, rid, "正常")
	c.Do("DELETE", "/api/rules/"+rid, nil, 204)
}

func TestSingleEntryValidationConflictsLoopsAndReferences(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "共享入口"), addNode(c, "独立入口")
	aid, bid := a["id"].(string), b["id"].(string)
	h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, aid, connected)
	group := entryGroup(c, aid)["id"].(string)
	port := h.ReserveTCPPort()
	body := singleEntryBody(aid, port, "example.com", 443)
	body["entry_group_id"] = group
	c.Do("POST", "/api/rules", body, 400)
	delete(body, "entry_group_id")
	delete(body, "entry_node_id")
	c.Do("POST", "/api/rules", body, 400)
	body["entry_node_id"] = "不存在"
	c.Do("POST", "/api/rules", body, 400)
	body["entry_node_id"] = aid
	rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, c, rid, "正常")
	conflict := c.Do("POST", "/api/rules", directBody(group, port, "example.com", 443), 409)
	requireErrorContains(t, conflict, aid, fmt.Sprint(port))
	other := entryGroup(c, bid)["id"].(string)
	c.Do("POST", "/api/rules", directBody(other, port, "example.com", 443), 201)
	c.Do("PUT", "/api/entry-groups/"+other, map[string]any{"name": "不能加入", "members": []string{bid, aid}}, 409)
	if len(groupRow(t, c, other)["members"].([]any)) != 1 {
		t.Fatal("加入失败未回滚")
	}
	refs := c.Do("DELETE", "/api/nodes/"+aid, nil, 409)
	requireErrorContains(t, refs, rid)
	c.Do("DELETE", "/api/rules/"+rid, nil, 204)
	c.Do("POST", "/api/rules", singleEntryBody(aid, 24021, "127.0.0.1", 24022), 201)
	loop := c.Do("POST", "/api/rules", directBody(group, 24022, "localhost", 24021), 409)
	if loop["code"] != "forwarding_loop" {
		t.Fatalf("未识别混合入口环路: %v", loop)
	}
}

func TestSingleEntryRandomPortSharesGroupPortSpace(t *testing.T) {
	random := &firstAvailablePortRandom{}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = panel.BcryptPasswords{Cost: 4}; d.Random = random })
	c := h.Client()
	setup(c)
	login(c)
	n := addNode(c, "随机端口入口")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.Listen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
	})
	awaitNode(t, c, id, connected)
	group := entryGroup(c, id)["id"].(string)
	c.Do("POST", "/api/rules", directBody(group, 1024, "example.com", 443), 201)
	one := singleEntryBody(id, 0, "example.com", 443)
	one["random_port"] = true
	if r := c.Do("POST", "/api/rules", one, 201); r["listen_port"] != float64(1025) {
		t.Fatalf("节点随机端口漏掉组规则: %v", r)
	}
	two := directBody(group, 0, "example.com", 443)
	two["random_port"] = true
	if r := c.Do("POST", "/api/rules", two, 201); r["listen_port"] != float64(1026) {
		t.Fatalf("组随机端口漏掉节点规则: %v", r)
	}
}

func TestSingleEntryJSONFormatsRoundTripAndAtomicRejection(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "导入入口")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	group := entryGroup(c, id)["id"].(string)
	old := directBody(group, h.ReserveTCPPort(), "example.com", 443)
	old["enabled"] = false
	importRules(c, "", "create", rulesDocument(old), 200)
	one := singleEntryBody(id, h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "J:"))
	doc := map[string]any{"format_version": 2, "rules": []map[string]any{one}}
	result := importRules(c, "", "create", doc, 200)
	rid := result["results"].([]any)[0].(map[string]any)["id"].(string)
	awaitRule(t, c, rid, "正常")
	tcpExchange(t, one["listen_port"].(int), "J:")
	exported := c.Do("GET", "/api/rules/export?id="+rid, nil, 200)
	if exported["format_version"] != float64(2) {
		t.Fatalf("未导出格式2: %v", exported)
	}
	importRules(c, "", "overwrite", exported, 200)
	if again := c.Do("GET", "/api/rules/export?id="+rid, nil, 200); !reflect.DeepEqual(exported, again) {
		t.Fatal("格式2往返不一致")
	}
	for _, both := range []bool{false, true} {
		bad := singleEntryBody(id, h.ReserveTCPPort(), "example.com", 443)
		if both {
			bad["entry_group_id"] = group
		} else {
			delete(bad, "entry_node_id")
		}
		good := singleEntryBody(id, h.ReserveTCPPort(), "example.com", 443)
		before := c.Do("GET", "/api/rules", nil, 200)["rules"]
		revision := nodeRow(t, c, id)["desired_revision"]
		importRules(c, "", "create", map[string]any{"format_version": 2, "rules": []map[string]any{good, bad}}, 400)
		if !reflect.DeepEqual(before, c.Do("GET", "/api/rules", nil, 200)["rules"]) || revision != nodeRow(t, c, id)["desired_revision"] {
			t.Fatal("非法入口导入未整批回滚")
		}
	}
	// 格式1不能夹带单节点语义。
	importRules(c, "", "create", rulesDocument(one), 400)
}

func TestSingleEntryReadinessIsolatedFromGroup(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "共享就绪入口")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	group := entryGroup(c, id)["id"].(string)
	c.Do("POST", "/api/rules", directBody(group, h.ReserveTCPPort(), "example.com", 443), 201)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	body := singleEntryBody(id, occupied.Addr().(*net.TCPAddr).Port, "example.com", 443)
	rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, c, rid, "同步失败")
	if row := sampleEntryState(t, h, c, group, id); row["ready"] != true {
		t.Fatalf("单节点失败污染组就绪: %v", row)
	}
	var single map[string]any
	for _, raw := range c.Do("GET", "/api/rules", nil, 200)["rules"].([]any) {
		row := raw.(map[string]any)
		if row["id"] == rid {
			single = row
		}
	}
	states, ok := single["entry_states"].([]any)
	if !ok || len(states) != 1 || states[0].(map[string]any)["ready"] != false || !hasEntryReason(states[0].(map[string]any), "readiness_reasons", "rule_apply_failed") {
		t.Fatalf("未暴露单节点入口就绪: %v", single)
	}
}

func TestSingleEntryOfflineTrafficBackfillAcrossEntryTypes(t *testing.T) {
	for _, fromNode := range []bool{true, false} {
		t.Run(fmt.Sprintf("原入口为节点=%v", fromNode), func(t *testing.T) {
			h, c := nodeBrowser(t)
			a, b := addNode(c, "离线原入口"), addNode(c, "新组入口")
			id := a["id"].(string)
			n := h.StartNode(a["installation_ticket"].(string))
			awaitNode(t, c, id, connected)
			port := h.ReserveTCPPort()
			body := singleEntryBody(id, port, "127.0.0.1", echoTarget(t, "B:"))
			if !fromNode {
				delete(body, "entry_node_id")
				body["entry_group_id"] = entryGroup(c, id)["id"]
			}
			rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
			awaitRule(t, c, rid, "正常")
			tcpExchange(t, port, "B:").Close()
			awaitTraffic(t, h, c, rid, 4, 6, float64(0))
			n.Disconnect()
			awaitNode(t, c, id, func(v map[string]any) bool { return v["connected"] == false })
			tcpExchange(t, port, "B:").Close()
			if fromNode {
				delete(body, "entry_node_id")
				body["entry_group_id"] = entryGroup(c, b["id"].(string))["id"]
			} else {
				delete(body, "entry_group_id")
				body["entry_node_id"] = b["id"]
			}
			c.Do("PUT", "/api/rules/"+rid, body, 200)
			tcpExchange(t, port, "B:").Close()
			n.Restart()
			n.Restore()
			awaitTraffic(t, h, c, rid, 12, 18, nil)
		})
	}
}

func TestSingleEntryTunnelRotationAndOfflineRevocation(t *testing.T) {
	f := newAuthorizationFixture(t)
	group := f.body["entry_group_id"]
	delete(f.body, "entry_group_id")
	f.body["entry_node_id"] = f.entryID
	rid := f.create(t)
	conn := tcpExchange(t, f.port, "T:")
	awaitTraffic(t, f.h, f.c, rid, 4, 6, float64(1))
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	awaitTunnelRotation(t, &f, "complete")
	assertRotationConnection(t, conn)
	tcpExchange(t, f.port, "T:").Close()
	// 单节点与同节点的组互改也必须更新授权代次，旧缓存不能继续使用旧授权。
	f.entry.Disconnect()
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
	previous := nodeRow(t, f.c, f.exitID)["desired_revision"].(float64)
	delete(f.body, "entry_node_id")
	f.body["entry_group_id"] = group
	f.c.Do("PUT", "/api/rules/"+rid, f.body, 200)
	awaitRevision(t, f.c, f.exitID, previous)
	f.h.Clock.Advance(time.Second)
	conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("出口未撤销旧授权流")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("旧授权已有流未主动断开")
	}
	assertTCPFailure(t, f.port)
	f.entry.Restore()
	awaitTraffic(t, f.h, f.c, rid, 12, 18, float64(0))
	awaitRule(t, f.c, rid, "正常")
	tcpExchange(t, f.port, "T:")
}

// 单节点入口离线时仍必须出现在轮换确认名单，不能提前撤下它缓存中的旧材料。
func TestSingleEntryTunnelRotationWaitsForOfflineEntry(t *testing.T) {
	f := newAuthorizationFixture(t)
	delete(f.body, "entry_group_id")
	f.body["entry_node_id"] = f.entryID
	rid := f.create(t)
	f.entry.Disconnect()
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	v := awaitTunnelRotation(t, &f, "distributing")
	entries := v["entries"].([]any)
	if v["pending"] != float64(1) || len(entries) != 1 || entries[0].(map[string]any)["node_id"] != f.entryID {
		t.Fatalf("轮换漏掉单节点入口: %v", v)
	}
	tcpExchange(t, f.port, "T:").Close()
	f.entry.Restore()
	awaitTraffic(t, f.h, f.c, rid, 4, 6, float64(0))
	awaitTunnelRotation(t, &f, "complete")
	tcpExchange(t, f.port, "T:")
}

func singleEntryState(t *testing.T, c *clusterharness.Client, rid string) map[string]any {
	t.Helper()
	for _, raw := range c.Do("GET", "/api/rules", nil, 200)["rules"].([]any) {
		row := raw.(map[string]any)
		if row["id"] == rid {
			states, ok := row["entry_states"].([]any)
			if !ok || len(states) != 1 {
				t.Fatalf("缺少单节点就绪: %v", row)
			}
			return states[0].(map[string]any)
		}
	}
	t.Fatal("规则不存在")
	return nil
}

// 就绪必须使用本规则的获授权出口握手，不把监听成功直接当成隧道就绪。
func TestSingleEntryTunnelReadinessRequiresHandshake(t *testing.T) {
	f := newAuthorizationFixture(t)
	delete(f.body, "entry_group_id")
	f.body["entry_node_id"] = f.entryID
	rid := f.create(t)
	awaitReady := func(want bool) map[string]any {
		t.Helper()
		var state map[string]any
		for range 4 {
			// 依照真实五秒探针和十秒入口检查推进，不以读取制造样本。
			for range 10 {
				awaitControlIdle(t, f.h)
				f.h.Clock.Advance(time.Second)
				time.Sleep(10 * time.Millisecond)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				state = singleEntryState(t, f.c, rid)
				if state["checked_at"] != nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("入口检查未运行: %v", state)
				}
				time.Sleep(time.Millisecond)
			}
			if state["ready"] == want {
				return state
			}
		}
		t.Fatalf("单节点入口就绪期望 %v: %v", want, state)
		return nil
	}
	awaitReady(true)
	f.exit.Stop()
	state := awaitReady(false)
	if !hasEntryReason(state, "readiness_reasons", "tunnel_handshake_missing") {
		t.Fatalf("缺少握手失败原因: %v", state)
	}
}
