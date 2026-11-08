package clusterharness_test

import (
	"fmt"
	"net"
	"reflect"
	"strconv"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
)

func saveMembers(c *clusterharness.Client, gid string, status int, ids ...string) map[string]any {
	return c.Do("PUT", "/api/entry-groups/"+gid, map[string]any{"name": "多成员入口组", "members": ids}, status)
}

func TestEntryMembersOrderValidationAndPersistence(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "入口 A")["id"].(string), addNode(c, "入口 B")["id"].(string)
	g := c.Do("POST", "/api/entry-groups", map[string]any{"name": "多成员", "members": []string{b, a}}, 201)
	gid := g["id"].(string)
	if !reflect.DeepEqual(groupRow(t, c, gid)["members"], []any{b, a}) {
		t.Fatal("列表重复了入口组或丢失成员顺序")
	}
	for _, members := range [][]string{nil, {}, {a, a}, {a, "missing"}, {a, ""}} {
		saveMembers(c, gid, 400, members...)
	}
	saveMembers(c, gid, 200, a, b)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(groupRow(t, c, gid)["members"], []any{a, b}) || publication(c, gid)["preferred_node_id"] != a {
		t.Fatal("重启丢失排序或第一位没有成为首选入口")
	}
}

// 本机共用网络命名空间，把两个真实节点的同一逻辑监听映射到不同物理端口。
func TestEntryMembersAllListenDirectAndTunnelAndRemovalRevokes(t *testing.T) {
	for _, tunnel := range []bool{false, true} {
		t.Run(fmt.Sprint("tunnel=", tunnel), func(t *testing.T) {
			h, c := nodeBrowser(t)
			a, b := addNode(c, "入口 A"), addNode(c, "入口 B")
			ids := []string{a["id"].(string), b["id"].(string)}
			ports := []int{freePort(t), freePort(t)}
			for i, n := range []map[string]any{a, b} {
				port := ports[i]
				h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
					d.Forwarding.Listen = func(network, _ string) (net.Listener, error) {
						return net.Listen(network, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
					}
				})
				awaitNode(t, c, ids[i], connected)
			}
			gid := c.Do("POST", "/api/entry-groups", map[string]any{"name": "多成员", "members": ids}, 201)["id"].(string)
			body := directBody(gid, 29001, "127.0.0.1", echoTarget(t, "M:"))
			if tunnel {
				exit := addNode(c, "出口")
				exitID := exit["id"].(string)
				h.StartNode(exit["installation_ticket"].(string))
				awaitNode(t, c, exitID, connected)
				c.Do("PUT", "/api/nodes/"+exitID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": freePort(t), "transport": "wss"}, 200)
				eg := c.Do("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{exitID}, "strategy": "primary_backup"}, 201)
				body["kind"], body["exit_group_id"] = "tunnel", eg["id"]
			}
			c.Do("POST", "/api/rules", body, 201)
			for i, id := range ids {
				awaitRevision(t, c, id, 0)
				tcpExchange(t, ports[i], "M:")
			}
			before := nodeRow(t, c, ids[1])["applied_revision"].(float64)
			existing := tcpExchange(t, ports[1], "M:")
			saveMembers(c, gid, 200, ids[1], ids[0])
			if nodeRow(t, c, ids[1])["desired_revision"] != before {
				t.Fatal("仅排序推进了节点配置版本")
			}
			saveMembers(c, gid, 200, ids[0])
			awaitRevision(t, c, ids[1], before)
			assertRevoked(t, ports[1], existing)
			tcpExchange(t, ports[0], "M:")
		})
	}
}

func TestEntryMembersKeepPortLoopAndDeletionChecks(t *testing.T) {
	_, c := nodeBrowser(t)
	a, b := addNode(c, "入口 A")["id"].(string), addNode(c, "入口 B")["id"].(string)
	gid := entryGroup(c, a)["id"].(string)
	other := entryGroup(c, b)["id"].(string)
	c.Do("POST", "/api/rules", directBody(gid, 29101, "example.com", 443), 201)
	r := c.Do("POST", "/api/rules", directBody(other, 29101, "example.com", 443), 201)
	before := nodeRow(t, c, b)["desired_revision"]
	requireErrorContains(t, saveMembers(c, gid, 409, a, b), "入口 B", "29101")
	if nodeRow(t, c, b)["desired_revision"] != before || !reflect.DeepEqual(groupRow(t, c, gid)["members"], []any{a}) {
		t.Fatal("失败成员修改没有原子回滚")
	}
	c.Do("DELETE", "/api/rules/"+r["id"].(string), nil, 204)
	// 加入第二位的节点时，同样检查它的检测端口和隧道端口。
	for _, field := range []string{"check_port", "tunnel_listen_port"} {
		ports := map[string]any{"check_port": 29102, "tunnel_listen_port": 29103}
		ports[field] = 29101
		c.Do("PUT", "/api/nodes/"+b+"/listener-ports", ports, 200)
		saveMembers(c, gid, 409, a, b)
	}
	c.Do("PUT", "/api/nodes/"+b+"/listener-ports", map[string]any{"check_port": 29102, "tunnel_listen_port": 29103}, 200)
	saveMembers(c, gid, 200, a, b)
	c.Do("POST", "/api/rules", directBody(other, 29104, "127.0.0.1", 29105), 201)
	c.Do("POST", "/api/rules", directBody(gid, 29105, "127.0.0.1", 29104), 409)
	refs := c.Do("GET", "/api/nodes/"+b+"/references", nil, 200)["references"].([]any)
	if len(refs) != 4 {
		t.Fatalf("第二位成员的组和规则引用丢失: %v", refs)
	}
	c.Do("DELETE", "/api/nodes/"+b, nil, 409)
	c.Do("DELETE", "/api/entry-groups/"+gid, nil, 409)
}
