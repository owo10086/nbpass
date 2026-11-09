package clusterharness_test

import (
	"net"
	"strconv"
	"testing"
	"time"
)

// 重启的真实 node.Run 只能使用加密本地缓存；控制连接阻断期间也要恢复两端隧道。
func TestTunnelCacheSurvivesOfflineNodeRestart(t *testing.T) {
	h, c := nodeBrowser(t)
	entry, exit := addNode(c, "缓存入口"), addNode(c, "缓存出口")
	entryID, exitID := entry["id"].(string), exit["id"].(string)
	entryNode := h.StartNode(entry["installation_ticket"].(string))
	exitNode := h.StartNode(exit["installation_ticket"].(string))
	awaitNode(t, c, entryID, connected)
	awaitNode(t, c, exitID, connected)
	tunnelPort := h.ReserveTCPPort()
	c.Do("PUT", "/api/nodes/"+exitID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": tunnelPort, "transport": "wss"}, 200)
	xg := c.Do("POST", "/api/exit-groups", map[string]any{"name": "缓存出口组", "members": []string{exitID}, "strategy": "primary_backup"}, 201)
	gid := entryGroup(c, entryID)["id"].(string)
	port := h.ReserveTCPPort()
	body := directBody(gid, port, "127.0.0.1", echoTarget(t, "C:"))
	body["kind"], body["exit_group_id"] = "tunnel", xg["id"]
	rule := c.Do("POST", "/api/rules", body, 201)
	awaitRevision(t, c, entryID, 0)
	awaitRevision(t, c, exitID, 0)
	awaitRule(t, c, rule["id"].(string), "正常")
	// 保留端口和成员冲突请求都须回滚，不能进入随后恢复的缓存。
	c.Do("POST", "/api/rules", directBody(gid, 18080, "8.8.8.8", 443), 409)
	c.Do("POST", "/api/rules", directBody(entryGroup(c, exitID)["id"].(string), tunnelPort, "8.8.8.8", 443), 409)
	tcpExchange(t, port, "C:")
	entryNode.Disconnect()
	exitNode.Disconnect()
	awaitNode(t, c, entryID, func(row map[string]any) bool { return !connected(row) })
	awaitNode(t, c, exitID, func(row map[string]any) bool { return !connected(row) })
	entryNode.Restart()
	exitNode.Restart()
	// 等待缓存 bind；不依赖面板已应用确认。
	deadline := time.Now().Add(5 * time.Second)
	// 先等出口监听恢复，避免入口端口轮询本身触发一次真实拨号失败（票12会立即标故障）。
	for _, p := range []int{tunnelPort, port} {
		for {
			conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), 50*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("离线缓存未恢复端口 %d: %v", p, err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	tcpExchange(t, port, "C:")
	awaitRule(t, c, rule["id"].(string), "待确认")
}

// 配置失败/确认不能改变 DNS 发布动作；DNS 写入也不能把失败规则标为正常。
func TestConfigSyncAndManagedDNSPublicationRemainIndependent(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	id := memberOf(c, gid)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	rule := c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", echoTarget(t, "D:")), 201)
	awaitRule(t, c, rule["id"].(string), "同步失败")
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, id, 202)
	action := awaitDNSStage(t, c, gid, "服务商已写入")
	g := groupRow(t, c, gid)
	if g["publication_status"] != "服务商已写入" || g["sync_status"] != "同步失败" {
		t.Fatalf("发布状态和同步失败互相覆盖: %v", g)
	}
	occupied.Close()
	h.Clock.Advance(10 * time.Second)
	awaitRule(t, c, rule["id"].(string), "正常")
	g = groupRow(t, c, gid)
	pub := publication(c, gid)
	if g["publication_status"] != "服务商已写入" || g["sync_status"] != "正常" || pub["action"].(map[string]any)["id"] != action["id"] || fake.Writes() != 1 {
		t.Fatalf("配置收敛串改发布动作: group=%v pub=%v writes=%d", g, pub, fake.Writes())
	}
	tcpExchange(t, port, "D:")
}
