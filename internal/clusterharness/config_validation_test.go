package clusterharness_test

import (
	"context"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

func requireErrorContains(t *testing.T, result map[string]any, parts ...string) {
	t.Helper()
	message, _ := result["error"].(string)
	for _, part := range parts {
		if !strings.Contains(message, part) {
			t.Fatalf("错误未说明 %q: %v", part, result)
		}
	}
}

func TestConfigValidationRejectsSharedNodePortsAtomically(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "共享入口")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	g1 := entryGroup(c, id)["id"].(string)
	g2 := entryGroup(c, id)["id"].(string)
	port := h.ReserveTCPPort()
	body := directBody(g1, port, "127.0.0.1", echoTarget(t, "A:"))
	first := c.Do("POST", "/api/rules", body, 201)
	rev := awaitRevision(t, c, id, 0)
	body["entry_group_id"] = g2
	conflict := c.Do("POST", "/api/rules", body, 409)
	requireErrorContains(t, conflict, "共享入口", id, fmt.Sprint(port), "直连 A")
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 1 || nodeRow(t, c, id)["desired_revision"] != rev {
		t.Fatal("冲突保存改变了规则或配置版本")
	}
	// 停用规则不监听；重新启用时必须再次校验，失败不能覆盖停用状态。
	body["enabled"] = false
	second := c.Do("POST", "/api/rules", body, 201)
	body["enabled"] = true
	c.Do("PUT", "/api/rules/"+second["id"].(string), body, 409)
	body["entry_group_id"] = g1
	c.Do("PUT", "/api/rules/"+first["id"].(string), body, 200) // 自身更新不算冲突。
	if nodeRow(t, c, id)["desired_revision"] != rev {
		t.Fatal("失败启用或无变化更新推进了版本")
	}
	// 相同端口在不同节点可用；修改成员不能绕过冲突校验。
	other := addNode(c, "独立入口")["id"].(string)
	g3 := entryGroup(c, other)["id"].(string)
	body["entry_group_id"] = g3
	c.Do("POST", "/api/rules", body, 201)
	c.Do("PUT", "/api/entry-groups/"+g3, map[string]any{"name": "独立组", "members": []string{id}}, 409)
	groups := c.Do("GET", "/api/entry-groups", nil, 200)["entry_groups"].([]any)
	for _, value := range groups {
		g := value.(map[string]any)
		if g["id"] == g3 && g["members"].([]any)[0] != other {
			t.Fatal("被拒的成员修改没有回滚")
		}
	}
}

func TestConfigValidationReservesNodeServicePorts(t *testing.T) {
	h, c := nodeBrowser(t)
	id := addNode(c, "保留端口入口")["id"].(string)
	path := "/api/nodes/" + id + "/listener-ports"
	ports := c.Do("GET", path, nil, 200)
	if ports["tunnel_listen_port"] == ports["check_port"] {
		t.Fatal("两种服务端口不能相同")
	}
	g := entryGroup(c, id)["id"].(string)
	for _, kind := range []struct{ field, label string }{{"tunnel_listen_port", "隧道监听"}, {"check_port", "检测端口"}} {
		port := int(ports[kind.field].(float64))
		result := c.Do("POST", "/api/rules", directBody(g, port, "example.com", 443), 409)
		requireErrorContains(t, result, "保留端口入口", fmt.Sprint(port), kind.label)
	}
	rule := c.Do("POST", "/api/rules", directBody(g, 23001, "example.com", 443), 201)
	for _, body := range []map[string]any{
		{"tunnel_listen_port": 23001, "check_port": 23002},
		{"tunnel_listen_port": 23002, "check_port": 23001},
		{"tunnel_listen_port": 23002, "check_port": 23002},
	} {
		c.Do("PUT", path, body, 409)
	}
	for _, body := range []map[string]any{
		{"tunnel_listen_port": 0, "check_port": 23002},
		{"tunnel_listen_port": 23002, "check_port": 65536},
		{"tunnel_listen_port": 23002},
	} {
		c.Do("PUT", path, body, 400)
	}
	if got := c.Do("GET", path, nil, 200); got["tunnel_listen_port"] != ports["tunnel_listen_port"] || got["check_port"] != ports["check_port"] {
		t.Fatal("端口设置失败却改变了原值")
	}
	c.Do("PUT", path, map[string]any{"tunnel_listen_port": 23002, "check_port": 23003}, 200)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	got := c.Do("GET", path, nil, 200)
	if got["tunnel_listen_port"] != float64(23002) || got["check_port"] != float64(23003) {
		t.Fatal("保留端口在重启后丢失")
	}
	c.Do("POST", "/api/rules", directBody(g, 23003, "example.com", 443), 409)
	c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
}

func TestConfigValidationRejectsSelfAndIndirectLoops(t *testing.T) {
	_, c := nodeBrowser(t)
	id := addNode(c, "循环入口")["id"].(string)
	g := entryGroup(c, id)["id"].(string)
	for _, address := range []string{"127.0.0.1", "127.0.0.2", "LOCALHOST."} {
		result := c.Do("POST", "/api/rules", directBody(g, 24001, address, 24001), 409)
		requireErrorContains(t, result, "转发循环", "循环入口", "24001")
	}
	first := c.Do("POST", "/api/rules", directBody(g, 24001, "127.0.0.1", 24002), 201)
	c.Do("POST", "/api/rules", directBody(g, 24002, "localhost", 24003), 201)
	last := directBody(g, 24003, "127.0.0.1", 24001)
	c.Do("POST", "/api/rules", last, 409)
	last["enabled"] = false
	third := c.Do("POST", "/api/rules", last, 201)
	last["enabled"] = true
	c.Do("PUT", "/api/rules/"+third["id"].(string), last, 409)
	// 打断链路后可以启用，证明没有把所有本机目标都当成循环。
	c.Do("DELETE", "/api/rules/"+first["id"].(string), nil, 204)
	c.Do("PUT", "/api/rules/"+third["id"].(string), last, 200)
}

func TestConfigValidationRejectsCrossNodeLoops(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "入口 A")
	b := addNode(c, "入口 B")
	start := func(installation map[string]any, ip string) {
		h.StartNode(installation["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
			deps.PublicIPv4 = func(context.Context) (string, error) { return ip, nil }
		})
		awaitNode(t, c, installation["id"].(string), func(row map[string]any) bool { return row["public_ipv4"] == ip })
	}
	start(a, "8.8.8.8")
	start(b, "1.1.1.1")
	ga := entryGroup(c, a["id"].(string))["id"].(string)
	gb := entryGroup(c, b["id"].(string))["id"].(string)
	c.Do("POST", "/api/rules", directBody(ga, 25001, "1.1.1.1", 25002), 201)
	result := c.Do("POST", "/api/rules", directBody(gb, 25002, "8.8.8.8", 25001), 409)
	requireErrorContains(t, result, "转发循环", "入口 A", "入口 B", "25001", "25002")
}

func TestNodeDeletionListsReferencesAndInvalidatesCredentials(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "被引用节点")
	id := n["id"].(string)
	real := h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	g := entryGroup(c, id)["id"].(string)
	body := directBody(g, h.ReserveTCPPort(), "example.com", 443)
	body["enabled"] = false // 停用规则仍是删除引用。
	rule := c.Do("POST", "/api/rules", body, 201)
	path := "/api/nodes/" + id
	refs := c.Do("GET", path+"/references", nil, 200)["references"].([]any)
	if len(refs) != 2 {
		t.Fatalf("引用清单不完整: %v", refs)
	}
	result := c.Do("DELETE", path, nil, 409)
	requireErrorContains(t, result, "入口组 A", "直连 A")
	if len(result["references"].([]any)) != 2 || !connected(nodeRow(t, c, id)) {
		t.Fatal("拒绝删除没有保留节点或返回引用清单")
	}
	c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
	c.Do("DELETE", "/api/entry-groups/"+g, nil, 204)
	c.Do("PUT", path+"/listener-ports", map[string]any{"tunnel_listen_port": 26001, "check_port": 26002}, 200)
	c.Do("DELETE", path, nil, 204)
	if len(c.Do("GET", "/api/nodes", nil, 200)["nodes"].([]any)) != 0 {
		t.Fatal("已删除节点仍在列表")
	}
	real.Restart() // 旧 token 的真实节点不能恢复已删除身份。
	deadline := time.After(5 * time.Second)
replay:
	for {
		select {
		case <-real.Done:
			break replay
		case <-deadline:
			t.Fatal("旧 token 的重连尝试未结束")
		case <-time.After(10 * time.Millisecond):
			// 流首个 Send 可能先看到 EOF，推进注入时钟让后续鉴权重试完成。
			h.Clock.Advance(time.Second)
		}
	}
	requireErrorContains(t, c.Do("POST", path+"/token/reset", nil, 404), "节点不存在")
	c.Do("GET", path+"/references", nil, 404)
	c.Do("DELETE", path, nil, 404)
	unused := addNode(c, "未兑换票据")
	c.Do("DELETE", "/api/nodes/"+unused["id"].(string), nil, 204)
	unregistered := h.StartNode(unused["installation_ticket"].(string))
	select {
	case <-unregistered.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("已删除节点的未兑换票据仍能接入")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	if len(c.Do("GET", "/api/nodes", nil, 200)["nodes"].([]any)) != 0 {
		t.Fatal("重连或重启复活了已删除节点")
	}
}

func TestNodeDeletionRecognizesFutureExitGroupRelations(t *testing.T) {
	h, c := nodeBrowser(t)
	id := addNode(c, "出口引用节点")["id"].(string)
	source := addNode(c, "隧道入口夹具")["id"].(string)
	g := entryGroup(c, source)["id"].(string)
	body := directBody(g, 27002, "example.com", 443)
	body["enabled"] = false
	rule := c.Do("POST", "/api/rules", body, 201)
	db, err := (platform.SQLite{Path: filepath.Join(h.Dir, "panel.db")}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO exit_groups(id, name, strategy) VALUES('exit-fixture', '出口组夹具', 'single')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext) VALUES(?,?,?,?,?,?,?)", id, "203.0.113.5", 18443, "wss", "exit.fixture", []byte("x"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO exit_group_members(group_id, node_id, position) VALUES('exit-fixture', ?, 0)", id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE forward_rules SET exit_group_id='exit-fixture' WHERE id=?", rule["id"]); err != nil {
		t.Fatal(err)
	}
	result := c.Do("DELETE", "/api/nodes/"+id, nil, 409)
	requireErrorContains(t, result, "出口组夹具")
	ref := result["references"].([]any)[0].(map[string]any)
	if ref["kind"] != "exit_group" || ref["id"] != "exit-fixture" {
		t.Fatalf("出口组引用不明确: %v", ref)
	}
	if len(result["references"].([]any)) != 2 {
		t.Fatalf("未列出经出口组引用节点的规则: %v", result)
	}
}

func TestConfigValidationRejectsDomainAliasLoop(t *testing.T) {
	h := clusterharness.New(t, func(deps *panel.Dependencies) {
		deps.Passwords = panel.BcryptPasswords{Cost: 4}
		deps.LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "alias.example" {
				return nil, fmt.Errorf("测试不解析其他域名")
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("127.0.0.2")}, nil
		}
	})
	c := h.Client()
	setup(c)
	login(c)
	id := addNode(c, "域名入口")["id"].(string)
	g := entryGroup(c, id)["id"].(string)
	result := c.Do("POST", "/api/rules", directBody(g, 28001, "alias.example", 28001), 409)
	requireErrorContains(t, result, "转发循环", "域名入口", "28001")
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 {
		t.Fatal("域名别名环路没有回滚")
	}
}

func TestNodeDeletionStorageFailureRollsBackCredentials(t *testing.T) {
	persistence := &interruptedPersistence{}
	h := clusterharness.New(t, func(deps *panel.Dependencies) {
		deps.Passwords = panel.BcryptPasswords{Cost: 4}
		persistence.base = deps.Persistence
		deps.Persistence = persistence
	})
	c := h.Client()
	setup(c)
	login(c)
	n := addNode(c, "回滚节点")
	id := n["id"].(string)
	if _, err := persistence.db.Exec(`CREATE TRIGGER fail_node_deletion BEFORE DELETE ON nodes BEGIN SELECT RAISE(ABORT, '测试台注入删除失败'); END;`); err != nil {
		t.Fatal(err)
	}
	c.Do("DELETE", "/api/nodes/"+id, nil, 503)
	// 安装票据仍可由真实节点兑换，证明先前的清理被事务完整回滚。
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
}

func TestConfigValidationSerializesConcurrentConflictingSaves(t *testing.T) {
	_, c := nodeBrowser(t)
	id := addNode(c, "并发入口")["id"].(string)
	g := entryGroup(c, id)["id"].(string)
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := c.Request("POST", "/api/rules", directBody(g, 27001, "example.com", 443), nil)
			defer r.Body.Close()
			statuses <- r.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[201] != 1 || counts[409] != 1 || len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 1 {
		t.Fatalf("并发保存绕过冲突预检: %v", counts)
	}
}

func TestValidationManagementRequiresAuthenticationAndOrigin(t *testing.T) {
	h, c := nodeBrowser(t)
	id := addNode(c, "受保护节点")["id"].(string)
	unauthenticated := h.Client()
	for _, path := range []string{"/api/nodes/" + id + "/listener-ports", "/api/nodes/" + id + "/references"} {
		unauthenticated.Do("GET", path, nil, 401)
	}
	unauthenticated.Do("DELETE", "/api/nodes/"+id, nil, 401)
	response := c.Request("DELETE", "/api/nodes/"+id, nil, map[string]string{"Origin": "https://other.example"})
	defer response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("跨源删除被放行")
	}
}
