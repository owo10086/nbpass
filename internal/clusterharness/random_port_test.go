package clusterharness_test

import (
	"crypto/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

// 只控制端口随机源的两字节读取，其余凭据仍使用真正的随机数。
type firstAvailablePortRandom struct{ portReads atomic.Int64 }

func (r *firstAvailablePortRandom) Read(b []byte) (int, error) {
	if len(b) == 2 {
		r.portReads.Add(1)
		clear(b)
		return len(b), nil
	}
	return rand.Read(b)
}

// 若只检查首个成员、漏掉活动隧道端口、按成员各选一次或重编译重新抽取，会失败。
func TestRandomPortUsesOneFreePortAcrossAllMembersAndPersists(t *testing.T) {
	random := &firstAvailablePortRandom{}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = panel.BcryptPasswords{Cost: 4}; d.Random = random })
	c := h.Client()
	setup(c)
	login(c)
	a, b := addNode(c, "随机入口 A"), addNode(c, "随机入口 B")
	aid, bid := a["id"].(string), b["id"].(string)
	c.Do("PUT", "/api/nodes/"+aid+"/listener-ports", map[string]any{"tunnel_listen_port": 1024, "check_port": 1025}, 200)
	c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 1026, "transport": "wss"}, 200)
	other := entryGroup(c, bid)["id"].(string)
	c.Do("POST", "/api/rules", directBody(other, 1027, "example.com", 443), 201)
	g := c.Do("POST", "/api/entry-groups", map[string]any{"name": "多成员随机端口", "members": []string{aid, bid}}, 201)["id"].(string)
	// 两个真实节点共享测试主机，将物理 bind 隔离到临时端口；规则配置与同步仍走真实代码。
	for _, n := range []map[string]any{a, b} {
		h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
			d.Forwarding.Listen = func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }
		})
		awaitNode(t, c, n["id"].(string), connected)
	}
	body := directBody(g, 0, "example.com", 443)
	body["random_port"] = true
	reads := random.portReads.Load()
	rule := c.Do("POST", "/api/rules", body, 201)
	if rule["listen_port"] != float64(1028) || random.portReads.Load()-reads != 1 {
		t.Fatalf("未一次选择所有成员共同空闲端口: %v", rule)
	}
	awaitRule(t, c, rule["id"].(string), "正常")
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	doc := c.Do("GET", "/api/rules/export?id="+rule["id"].(string), nil, 200)
	if doc["rules"].([]any)[0].(map[string]any)["listen_port"] != float64(1028) || random.portReads.Load()-reads != 1 {
		t.Fatal("重启或同步重新抽取了端口")
	}
}

func TestRandomPortCreationSerializesConcurrentRequestsAndRejectsAmbiguity(t *testing.T) {
	random := &firstAvailablePortRandom{}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = panel.BcryptPasswords{Cost: 4}; d.Random = random })
	c := h.Client()
	setup(c)
	login(c)
	g := entryGroup(c, addNode(c, "并发随机入口")["id"].(string))["id"].(string)
	body := directBody(g, 0, "example.com", 443)
	body["random_port"] = true
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); c.Do("POST", "/api/rules", body, 201) }()
	}
	wg.Wait()
	rules := c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)
	if len(rules) != 2 || rules[0].(map[string]any)["listen_port"] == rules[1].(map[string]any)["listen_port"] {
		t.Fatal("并发随机端口发生冲突")
	}
	body["listen_port"] = 24000
	c.Do("POST", "/api/rules", body, 400)
	body["listen_port"] = 0
	c.Do("PUT", "/api/rules/"+rules[0].(map[string]any)["id"].(string), body, 400)
	body["entry_group_id"] = "missing"
	c.Do("POST", "/api/rules", body, 400)
}
