package clusterharness_test

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
)

func ruleRow(t *testing.T, c *clusterharness.Client, id string) map[string]any {
	t.Helper()
	for _, value := range c.Do("GET", "/api/rules", nil, 200)["rules"].([]any) {
		row := value.(map[string]any)
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("规则不存在: %s", id)
	return nil
}

func awaitRule(t *testing.T, c *clusterharness.Client, id, status string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := ruleRow(t, c, id)
		if row["sync_status"] == status {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待规则 %s 超时: %v", status, row)
		}
		time.Sleep(time.Millisecond)
	}
}

func groupRow(t *testing.T, c *clusterharness.Client, id string) map[string]any {
	t.Helper()
	for _, value := range c.Do("GET", "/api/entry-groups", nil, 200)["entry_groups"].([]any) {
		row := value.(map[string]any)
		if row["id"] == id {
			return row
		}
	}
	t.Fatalf("入口组不存在: %s", id)
	return nil
}

// 一条实际 bind 失败不能阻止同快照的其他规则，也不能污染同节点的其他入口组。
func TestPartialApplyReportsEachRuleAndGroupOverHTTP(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "部分失败入口")
	id := n["id"].(string)
	real := h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	goodGroup := entryGroup(c, id)["id"].(string)
	badGroup := entryGroup(c, id)["id"].(string)
	goodPort := h.ReserveTCPPort()
	good := c.Do("POST", "/api/rules", directBody(goodGroup, goodPort, "127.0.0.1", echoTarget(t, "A:")), 201)
	previous := awaitRevision(t, c, id, 0)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	bad := c.Do("POST", "/api/rules", directBody(badGroup, port, "127.0.0.1", echoTarget(t, "B:")), 201)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	failed := awaitRule(t, c, bad["id"].(string), "同步失败")
	member := failed["sync_members"].([]any)[0].(map[string]any)
	if member["node_id"] != id || member["node_name"] != "部分失败入口" || member["listen_port"] != float64(port) || !strings.Contains(member["apply_error"].(string), fmt.Sprint(port)) || member["applied_revision"] != float64(0) {
		t.Fatalf("未提供准确的逐成员监听错误: %v", member)
	}
	goodRow := awaitRule(t, c, good["id"].(string), "正常")
	goodMember := goodRow["sync_members"].([]any)[0].(map[string]any)
	if goodMember["applied_revision"] != nodeRow(t, c, id)["desired_revision"] || nodeRow(t, c, id)["applied_revision"] != previous {
		t.Fatal("部分成功错误推进整体版本，或丢失逐规则实际版本")
	}
	if groupRow(t, c, goodGroup)["sync_status"] != "正常" || groupRow(t, c, badGroup)["sync_status"] != "同步失败" {
		t.Fatal("入口组汇总未按本组规则计算")
	}
	tcpExchange(t, goodPort, "A:")
	real.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return !connected(row) })
	for _, rule := range []map[string]any{good, bad} {
		row := awaitRule(t, c, rule["id"].(string), "待确认")
		if row["sync_members"].([]any)[0].(map[string]any)["sync_status"] != "待确认" {
			t.Fatal("失联成员沿用旧确认")
		}
	}
	if groupRow(t, c, goodGroup)["sync_status"] != "待确认" || groupRow(t, c, badGroup)["sync_status"] != "待确认" {
		t.Fatal("失联入口组仍显示正常/确定失败")
	}
	tcpExchange(t, goodPort, "A:")
	occupied.Close()
	real.Restore()
	for deadline := time.Now().Add(5 * time.Second); !connected(nodeRow(t, c, id)); {
		if time.Now().After(deadline) {
			t.Fatal("控制链路未恢复")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitRevision(t, c, id, previous)
	awaitRule(t, c, bad["id"].(string), "正常")
	tcpExchange(t, port, "B:")
}

// 缓存必须记录部分应用中的撤销；离线重建核心只能恢复真正生效的监听。
func TestPartialApplyRestartNeverResurrectsDisabledOrDeletedRules(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "缓存入口")
	id := n["id"].(string)
	real := h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	keepPort, disablePort, deletePort := h.ReserveTCPPort(), h.ReserveTCPPort(), h.ReserveTCPPort()
	target := echoTarget(t, "A:")
	keep := c.Do("POST", "/api/rules", directBody(gid, keepPort, "127.0.0.1", target), 201)
	disabledBody := directBody(gid, disablePort, "127.0.0.1", target)
	disabled := c.Do("POST", "/api/rules", disabledBody, 201)
	deleted := c.Do("POST", "/api/rules", directBody(gid, deletePort, "127.0.0.1", target), 201)
	previous := awaitRevision(t, c, id, 0)
	disableConn, deleteConn := tcpExchange(t, disablePort, "A:"), tcpExchange(t, deletePort, "A:")
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	badPort := occupied.Addr().(*net.TCPAddr).Port
	bad := c.Do("POST", "/api/rules", directBody(gid, badPort, "127.0.0.1", target), 201)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	disabledBody["enabled"] = false
	c.Do("PUT", "/api/rules/"+disabled["id"].(string), disabledBody, 200)
	c.Do("DELETE", "/api/rules/"+deleted["id"].(string), nil, 204)
	awaitRule(t, c, disabled["id"].(string), "正常")
	awaitRule(t, c, bad["id"].(string), "同步失败")
	assertRevoked(t, disablePort, disableConn)
	assertRevoked(t, deletePort, deleteConn)
	if nodeRow(t, c, id)["applied_revision"] != previous {
		t.Fatal("部分失败推进已应用版本")
	}
	real.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return !connected(row) })
	// 网络保持阻断，重启不得靠面板重新下发恢复监听。
	real.Restart()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", keepPort), 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("节点未在离线重建后恢复缓存监听")
		}
		time.Sleep(time.Millisecond)
	}
	tcpExchange(t, keepPort, "A:")
	for _, port := range []int{disablePort, deletePort} {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			conn.Close()
			t.Fatalf("已撤销规则在重启后复活: %d", port)
		}
	}
	awaitRule(t, c, keep["id"].(string), "待确认")
	// 面板也保留磁盘重建，业务依旧转发，同步仍待确认。
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitRule(t, c, keep["id"].(string), "待确认")
	tcpExchange(t, keepPort, "A:")
}
