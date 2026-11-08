package clusterharness_test

import (
	"errors"
	"fmt"
	"io"
	"nbpass/internal/node"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
)

func entryGroup(c *clusterharness.Client, id string) map[string]any {
	return c.Do("POST", "/api/entry-groups", map[string]any{"name": "入口组 A", "members": []string{id}}, 201)
}
func directBody(group string, port int, target string, targetPort int) map[string]any {
	return map[string]any{"name": "直连 A", "entry_group_id": group, "kind": "direct", "listen_port": port, "target_address": target, "target_family": "ipv4", "target_port": targetPort, "allow_private": true, "enabled": true}
}
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}
func echoTarget(t *testing.T, prefix string) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				data := make([]byte, 4)
				for {
					if _, err := io.ReadFull(c, data); err != nil {
						return
					}
					if _, err := fmt.Fprintf(c, "%s%s", prefix, data); err != nil {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}
func tcpExchange(t *testing.T, port int, prefix string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, len(prefix)+4)
	if _, err = io.ReadFull(c, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != prefix+"ping" {
		t.Fatalf("真实 TCP 目标: %q", b)
	}
	c.SetDeadline(time.Time{})
	return c
}
func assertRevoked(t *testing.T, port int, existing net.Conn) {
	t.Helper()
	existing.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 1)
	_, err := existing.Read(b)
	if err == nil {
		t.Fatal("撤销后已有连接未关闭")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("已有连接仍未断开")
	}
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err == nil {
		c.Close()
		t.Fatal("撤销后仍在监听")
	}
}
func awaitRevision(t *testing.T, c *clusterharness.Client, id string, greater float64) float64 {
	t.Helper()
	awaitNode(t, c, id, func(row map[string]any) bool {
		return row["desired_revision"].(float64) > greater && row["desired_revision"] == row["applied_revision"] && row["config_status"] == "正常"
	})
	return nodeRow(t, c, id)["applied_revision"].(float64)
}

// 业务管理与同步断言全部经面板 HTTP；TCP 监听与字节流属于真实网络物理验证。
func TestDirectRuleLifecycleOverRealTCP(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "入口 A")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	g := entryGroup(c, id)
	gid := g["id"].(string)
	if g["publication_status"] != "未发布" {
		t.Fatalf("未绑定 DNS 的组: %v", g)
	}
	port := freePort(t)
	target := echoTarget(t, "A:")
	body := directBody(gid, port, "127.0.0.1", target)
	rule := c.Do("POST", "/api/rules", body, 201)
	path := "/api/rules/" + rule["id"].(string)
	rev := awaitRevision(t, c, id, 0)
	first := tcpExchange(t, port, "A:")
	// 心跳与全量同步不能重建未变化的规则，既有连接必须保留。
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, connected)
	first.SetDeadline(time.Now().Add(time.Second))
	first.Write([]byte("ping"))
	b := make([]byte, 6)
	if _, err := io.ReadFull(first, b); err != nil {
		t.Fatal(err)
	}
	body["enabled"] = false
	c.Do("PUT", path, body, 200)
	rev = awaitRevision(t, c, id, rev)
	assertRevoked(t, port, first)
	body["enabled"] = true
	c.Do("PUT", path, body, 200)
	rev = awaitRevision(t, c, id, rev)
	second := tcpExchange(t, port, "A:")
	body["target_port"] = echoTarget(t, "B:")
	body["target_address"] = "localhost"
	c.Do("PUT", path, body, 200)
	rev = awaitRevision(t, c, id, rev)
	third := tcpExchange(t, port, "B:")
	second.Close()
	c.Do("DELETE", path, nil, 204)
	awaitRevision(t, c, id, rev)
	assertRevoked(t, port, third)
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 {
		t.Fatal("删除未反映到列表")
	}
	c.Do("DELETE", "/api/entry-groups/"+gid, nil, 204)
}

func TestDirectManagementValidationAndPersistence(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "入口 A")
	gid := entryGroup(c, n["id"].(string))["id"].(string)
	for _, change := range []map[string]any{{"target_address": "192.168.1.1", "allow_private": false}, {"target_address": "::1"}, {"target_family": "ipv6"}, {"target_address": "https://example.com"}, {"target_port": 0}, {"listen_port": 65536}, {"kind": "tunnel"}, {"target_address": "invalid host"}} {
		body := directBody(gid, freePort(t), "example.com", 443)
		for k, v := range change {
			body[k] = v
		}
		c.Do("POST", "/api/rules", body, 400)
	}
	c.Do("POST", "/api/entry-groups", map[string]any{"name": "坏组", "members": []string{"missing"}}, 400)
	body := directBody(gid, freePort(t), "example.com", 443)
	body["allow_private"] = false
	rule := c.Do("POST", "/api/rules", body, 201)
	c.Do("DELETE", "/api/entry-groups/"+gid, nil, 409)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	rules := c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["id"] != rule["id"] {
		t.Fatal("规则重启丢失")
	}
	groups := c.Do("GET", "/api/entry-groups", nil, 200)["entry_groups"].([]any)
	if len(groups) != 1 {
		t.Fatal("入口组重启丢失")
	}
}

func TestDirectListenFailureIsVisibleAndCanRetry(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "监听失败")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	body := directBody(gid, occupied.Addr().(*net.TCPAddr).Port, "127.0.0.1", echoTarget(t, "A:"))
	c.Do("POST", "/api/rules", body, 201)
	awaitNode(t, c, id, func(row map[string]any) bool {
		return row["config_status"] == "同步失败" && row["apply_error"] != "" && row["applied_revision"] == float64(0)
	})
	occupied.Close()
	h.Clock.Advance(60 * time.Second)
	awaitRevision(t, c, id, 0)
	tcpExchange(t, body["listen_port"].(int), "A:")
}

func TestDirectDeleteWhileDisconnectedConvergesWithFullSnapshot(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "入口 A")
	id := n["id"].(string)
	real := h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	port := freePort(t)
	body := directBody(gid, port, "127.0.0.1", echoTarget(t, "A:"))
	rule := c.Do("POST", "/api/rules", body, 201)
	rev := awaitRevision(t, c, id, 0)
	conn := tcpExchange(t, port, "A:")
	real.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return !connected(row) })
	c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
	if nodeRow(t, c, id)["config_status"] != "待确认" {
		t.Fatal("离线撤销伪装成正常")
	}
	// 面板重启保留递增版本及完整撤销快照；失联节点继续现有转发。
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	tcpExchange(t, port, "A:")
	real.Restore()
	deadline := time.Now().Add(5 * time.Second)
	for !connected(nodeRow(t, c, id)) {
		if time.Now().After(deadline) {
			t.Fatal("控制未恢复")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitRevision(t, c, id, rev)
	assertRevoked(t, port, conn)
}

func TestNewNodeProcessMustConfirmConfigurationAgain(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "重启入口")
	id := n["id"].(string)
	var starts atomic.Int32
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	real := h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.Forwarding.Listen = func(network, address string) (net.Listener, error) {
			if starts.Add(1) == 2 {
				close(blocked)
				<-release
			}
			return net.Listen(network, address)
		}
	})
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	body := directBody(gid, freePort(t), "127.0.0.1", echoTarget(t, "A:"))
	c.Do("POST", "/api/rules", body, 201)
	awaitRevision(t, c, id, 0)
	real.Restart()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("新进程未恢复缓存监听")
	}
	row := nodeRow(t, c, id)
	// 缓存监听恢复完成前尚未连接面板，旧确认只作为历史保留，不能显示正常。
	if row["config_status"] != "待确认" || connected(row) {
		t.Fatalf("新进程恢复完成前错误确认配置: %v", row)
	}
	unblock()
	awaitRevision(t, c, id, 0)
}

func TestAppliedAcknowledgementLostDuringDisconnectConvergesOnReconnect(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "确认丢失")
	id := n["id"].(string)
	var realRef atomic.Pointer[clusterharness.Node]
	var disconnected atomic.Bool
	real := h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.Forwarding.Listen = func(network, address string) (net.Listener, error) {
			listener, err := net.Listen(network, address)
			if err == nil && disconnected.CompareAndSwap(false, true) {
				realRef.Load().Disconnect()
			}
			return listener, err
		}
	})
	realRef.Store(real)
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	body := directBody(gid, freePort(t), "127.0.0.1", echoTarget(t, "A:"))
	c.Do("POST", "/api/rules", body, 201)
	awaitNode(t, c, id, func(row map[string]any) bool { return disconnected.Load() && !connected(row) })
	// 规则已实际监听，但应用确认因链路断开未到达面板。
	tcpExchange(t, body["listen_port"].(int), "A:")
	real.Restore()
	deadline := time.Now().Add(5 * time.Second)
	for !connected(nodeRow(t, c, id)) {
		if time.Now().After(deadline) {
			t.Fatal("控制未恢复")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitRevision(t, c, id, 0)
}

func TestMultipleListenFailuresFitControlReportLimit(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "多规则失败")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.Forwarding.Listen = func(string, string) (net.Listener, error) {
			return nil, errors.New(strings.Repeat("监听失败😀", 6000))
		}
	})
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	for i := 0; i < 3; i++ {
		c.Do("POST", "/api/rules", directBody(gid, 20000+i, "example.com", 443), 201)
	}
	awaitNode(t, c, id, func(row map[string]any) bool {
		return row["desired_revision"] == float64(3) && row["config_status"] == "同步失败"
	})
	message := nodeRow(t, c, id)["apply_error"].(string)
	if len(message) == 0 || len(message) > 1024 || strings.ContainsRune(message, '\ufffd') {
		t.Fatalf("错误报告未限制为完整UTF-8: 长度=%d", len(message))
	}
}
