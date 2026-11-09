package clusterharness_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"nbpass/internal/clusterharness"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"nbpass/internal/node"
)

// 若入口直拨目标或在入口解析域名，本测试会在真实 TCP 交换处失败。
func TestSingleExitTunnelOverWSSYamux(t *testing.T) {
	h, c := nodeBrowser(t)
	entry, exit := addNode(c, "入口"), addNode(c, "出口")
	entryID, exitID := entry["id"].(string), exit["id"].(string)
	entryNode := h.StartNode(entry["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.LookupIPv4 = func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("入口禁止解析目标")
		}
		d.Forwarding.DialTarget = func(context.Context, string) (net.Conn, error) { return nil, errors.New("入口禁止直拨目标") }
	})
	exitNode := h.StartNode(exit["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "exit-only.invalid" {
				return nil, errors.New("非授权域名")
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
	})
	awaitNode(t, c, entryID, connected)
	awaitNode(t, c, exitID, connected)
	c.Do("PUT", "/api/nodes/"+exitID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
	group := c.Do("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{exitID}, "strategy": "primary_backup"}, 201)
	gid := entryGroup(c, entryID)["id"].(string)
	port := h.ReserveTCPPort()
	body := directBody(gid, port, "exit-only.invalid", echoTarget(t, "T:"))
	body["kind"], body["exit_group_id"] = "tunnel", group["id"]
	c.Do("POST", "/api/rules", body, 201)
	awaitRevision(t, c, exitID, 0)
	awaitRevision(t, c, entryID, 0)
	tcpExchange(t, port, "T:")
	tcpExchange(t, port, "T:")
	awaitUptime(t, c, entryID, 0)
	awaitUptime(t, c, exitID, 0)
	h.Clock.Advance(5 * time.Second)
	awaitProbe(t, c, entryID, func(v map[string]any) bool {
		rt, ok := v["realtime"].(map[string]any)
		return ok && rt["business_connections"] == float64(2) && rt["tunnel_connections"] == float64(1)
	})
	awaitProbe(t, c, exitID, func(v map[string]any) bool {
		rt, ok := v["realtime"].(map[string]any)
		return ok && rt["business_connections"] == float64(2) && rt["tunnel_connections"] == float64(1)
	})
	entryNode.Disconnect()
	exitNode.Disconnect()
	awaitNode(t, c, entryID, func(row map[string]any) bool { return row["connected"] == false })
	awaitNode(t, c, exitID, func(row map[string]any) bool { return row["connected"] == false })
	tcpExchange(t, port, "T:")
}

func tunnelSetup(t *testing.T) (*clusterharness.Harness, *clusterharness.Client, string, string, *clusterharness.Node, int, map[string]any) {
	t.Helper()
	h, c := nodeBrowser(t)
	entry, exit := addNode(c, "入口"), addNode(c, "出口")
	entryID, exitID := entry["id"].(string), exit["id"].(string)
	h.StartNode(entry["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", port))
		}
	})
	n := h.StartNode(exit["installation_ticket"].(string))
	awaitNode(t, c, entryID, connected)
	awaitNode(t, c, exitID, connected)
	exitPort := h.ReserveTCPPort()
	c.Do("PUT", "/api/nodes/"+exitID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": exitPort, "transport": "wss"}, 200)
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{exitID}, "strategy": "primary_backup"}, 201)
	port := h.ReserveTCPPort()
	body := directBody(entryGroup(c, entryID)["id"].(string), port, "127.0.0.1", echoTarget(t, "T:"))
	body["kind"], body["exit_group_id"] = "tunnel", g["id"]
	r := c.Do("POST", "/api/rules", body, 201)
	body["id"] = r["id"]
	awaitRevision(t, c, exitID, 0)
	awaitRevision(t, c, entryID, 0)
	return h, c, entryID, exitID, n, port, body
}
func assertTCPFailure(t *testing.T, port int) {
	t.Helper()
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	c.Write([]byte("ping"))
	b := make([]byte, 6)
	_, err = io.ReadFull(c, b)
	if err == nil {
		t.Fatalf("出口失效后仍到达目标: %q", b)
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("失败连接未及时结束")
	}
}
func TestSingleExitUnavailableNeverFallsBackToDirect(t *testing.T) {
	_, _, _, _, exit, port, _ := tunnelSetup(t)
	tcpExchange(t, port, "T:")
	exit.Stop()
	// 目标仍可由入口直接访问，退回直连会使此断言失败。
	assertTCPFailure(t, port)
}
func TestTunnelChangesAndEndpointUpdatesKeepIdentity(t *testing.T) {
	h, c, entryID, exitID, _, port, body := tunnelSetup(t)
	first := tcpExchange(t, port, "T:")
	exits := c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)
	identity := exits[0].(map[string]any)
	certificate := identity["certificate_pem"]
	block, _ := pem.Decode([]byte(certificate.(string)))
	if block == nil {
		t.Fatal("缺少公开证书")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.NotAfter != cert.NotBefore.AddDate(5, 0, 0) || cert.VerifyHostname(identity["server_name"].(string)) != nil || len(cert.IPAddresses) != 0 {
		t.Fatal("证书有效期或稳定身份不符合要求")
	}
	entryRev := nodeRow(t, c, entryID)["applied_revision"].(float64)
	exitRev := nodeRow(t, c, exitID)["applied_revision"].(float64)
	body["target_port"] = echoTarget(t, "B:")
	c.Do("PUT", "/api/rules/"+body["id"].(string), body, 200)
	exitRev = awaitRevision(t, c, exitID, exitRev)
	entryRev = awaitRevision(t, c, entryID, entryRev)
	first.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("修改规则后旧入口连接未断开")
	}
	tcpExchange(t, port, "B:")
	c.Do("PUT", "/api/nodes/"+exitID+"/tunnel", map[string]any{"address": "127.0.0.2", "listen_port": identity["listen_port"], "transport": "wss"}, 200)
	entryRev = awaitRevision(t, c, entryID, entryRev)
	updated := c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)[0].(map[string]any)
	if updated["certificate_pem"] != certificate || updated["server_name"] != identity["server_name"] {
		t.Fatal("出口更换地址重新生成了身份")
	}
	tcpExchange(t, port, "B:")
	body["enabled"] = false
	c.Do("PUT", "/api/rules/"+body["id"].(string), body, 200)
	awaitRevision(t, c, entryID, entryRev)
	awaitRevision(t, c, exitID, exitRev)
	assertTCPFailure(t, port)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	if c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)[0].(map[string]any)["certificate_pem"] != certificate {
		t.Fatal("面板重启丢失出口身份")
	}
	if len(c.Do("GET", "/api/exit-groups", nil, 200)["exit_groups"].([]any)) != 1 {
		t.Fatal("出口组未持久化")
	}
}
func TestTunnelManagementValidationAndGates(t *testing.T) {
	h, c := nodeBrowser(t)
	id := addNode(c, "出口")["id"].(string)
	path := "/api/nodes/" + id + "/tunnel"
	for _, b := range []map[string]any{{"address": "::1", "listen_port": 12345, "transport": "wss"}, {"address": "0.0.0.0", "listen_port": 12345, "transport": "wss"}, {"address": "127.0.0.1", "listen_port": 0, "transport": "wss"}, {"address": "127.0.0.1", "listen_port": 12345, "transport": "ws"}} {
		c.Do("PUT", path, b, 400)
	}
	c.Do("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{id}, "strategy": "primary_backup"}, 400)
	c.Do("PUT", path, map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
	for _, b := range []map[string]any{{"name": "", "members": []string{id}, "strategy": "primary_backup"}, {"name": "组", "members": []string{id, id}, "strategy": "primary_backup"}, {"name": "组", "members": []string{id}, "strategy": "least_connections"}, {"name": "组", "members": []string{"missing"}, "strategy": "primary_backup"}} {
		c.Do("POST", "/api/exit-groups", b, 400)
	}
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "组", "members": []string{id}, "strategy": "primary_backup"}, 201)
	c.Do("PUT", "/api/exit-groups/"+g["id"].(string), map[string]any{"name": "重命名", "members": []string{id}, "strategy": "primary_backup"}, 200)
	gid := entryGroup(c, id)["id"].(string)
	body := directBody(gid, h.ReserveTCPPort(), "127.0.0.1", 443)
	body["kind"], body["exit_group_id"] = "tunnel", g["id"]
	rule := c.Do("POST", "/api/rules", body, 201)
	c.Do("DELETE", "/api/exit-groups/"+g["id"].(string), nil, 409)
	c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
	c.Do("DELETE", "/api/exit-groups/"+g["id"].(string), nil, 204)
	h.Client().Do("GET", "/api/exit-groups", nil, 401)
	response := c.Request("PUT", path, map[string]any{"address": "127.0.0.1", "listen_port": 12345, "transport": "wss"}, map[string]string{"Origin": "https://other.invalid"})
	defer response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("跨源配置出口未拒绝")
	}
}

func TestCombinedEntryExitCountsBusinessOnce(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "兼任入口和出口")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	awaitUptime(t, c, id, 0)
	c.Do("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{id}, "strategy": "primary_backup"}, 201)
	port := h.ReserveTCPPort()
	body := directBody(entryGroup(c, id)["id"].(string), port, "127.0.0.1", echoTarget(t, "T:"))
	body["kind"], body["exit_group_id"] = "tunnel", g["id"]
	c.Do("POST", "/api/rules", body, 201)
	awaitRevision(t, c, id, 0)
	tcpExchange(t, port, "T:")
	h.Clock.Advance(5 * time.Second)
	awaitProbe(t, c, id, func(v map[string]any) bool {
		rt, ok := v["realtime"].(map[string]any)
		return ok && rt["business_connections"] == float64(1)
	})
}
