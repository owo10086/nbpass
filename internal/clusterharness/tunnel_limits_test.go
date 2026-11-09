package clusterharness_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
)

type tunnelLimitsFixture struct {
	h                                              *clusterharness.Harness
	c                                              *clusterharness.Client
	entry, exit, group, entryGroup, exitEntryGroup string
	port                                           func() int
}

func tunnelLimitsSetup(t *testing.T, combined bool, entryLimits, exitLimits forwarding.ResourceLimits, options ...func(*node.Config, *node.Dependencies)) *tunnelLimitsFixture {
	t.Helper()
	h, c := nodeBrowser(t)
	entry := addNode(c, "资源入口")
	exit := entry
	if !combined {
		exit = addNode(c, "资源出口")
	}
	port, listen := directPorts(t)
	entryOptions := []func(*node.Config, *node.Dependencies){listen, func(_ *node.Config, d *node.Dependencies) { d.Forwarding.Limits = entryLimits }}
	if combined {
		entryOptions = append(entryOptions, options...)
	}
	h.StartNode(entry["installation_ticket"].(string), entryOptions...)
	if !combined {
		exitOptions := []func(*node.Config, *node.Dependencies){listen, func(_ *node.Config, d *node.Dependencies) { d.Forwarding.Limits = exitLimits }}
		h.StartNode(exit["installation_ticket"].(string), append(exitOptions, options...)...)
	}
	eid, xid := entry["id"].(string), exit["id"].(string)
	awaitNode(t, c, eid, connected)
	awaitNode(t, c, xid, connected)
	awaitUptime(t, c, eid, 0)
	awaitUptime(t, c, xid, 0)
	c.Do("PUT", "/api/nodes/"+xid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": port(), "transport": "wss"}, 200)
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "容量出口组", "members": []string{xid}, "strategy": "primary_backup"}, 201)
	egroup := entryGroup(c, eid)["id"].(string)
	xgroup := egroup
	if !combined {
		xgroup = entryGroup(c, xid)["id"].(string)
	}
	return &tunnelLimitsFixture{h: h, c: c, entry: eid, exit: xid, group: g["id"].(string), entryGroup: egroup, exitEntryGroup: xgroup, port: port}
}

func (f *tunnelLimitsFixture) rule(t *testing.T, target int) (int, string) {
	t.Helper()
	p := f.port()
	body := directBody(f.entryGroup, p, "127.0.0.1", target)
	body["kind"], body["exit_group_id"] = "tunnel", f.group
	r := f.c.Do("POST", "/api/rules", body, 201)
	awaitRevision(t, f.c, f.exit, 0)
	awaitRevision(t, f.c, f.entry, 0)
	return p, r["id"].(string)
}

func sampleTunnelBusiness(t *testing.T, f *tunnelLimitsFixture, id string, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.h.Clock.Advance(5 * time.Second)
		time.Sleep(10 * time.Millisecond)
		v := f.c.Do("GET", probePath(id), nil, 200)
		if rt, ok := v["realtime"].(map[string]any); ok && v["sampled_at"] == f.h.Clock.Now().Format(time.RFC3339Nano) && rt["business_connections"] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("隧道业务连接数未收敛到 %v: %v", want, v)
		}
	}
}

// 任意一侧漏接统一预算、按双重身份扣槽或超限破坏旧连接，都会失败。
func TestTunnelNodeAndRuleBudgetsShareDirectCapacityAndKeepControlResponsive(t *testing.T) {
	for _, side := range []string{"entry", "exit", "combined"} {
		t.Run(side, func(t *testing.T) {
			limits := forwarding.ResourceLimits{MaxConnections: 3, MaxConnectionsPerRule: 2}
			var el, xl forwarding.ResourceLimits
			if side == "exit" {
				xl = limits
			} else {
				el = limits
			}
			f := tunnelLimitsSetup(t, side == "combined", el, xl)
			target := echoTarget(t, "L:")
			pa, ra := f.rule(t, target)
			pb, _ := f.rule(t, target)
			a1, a2 := tcpExchange(t, pa, "L:"), tcpExchange(t, pa, "L:")
			requireClosed(t, directClient(t, pa))
			id, gid := f.entry, f.entryGroup
			if side == "exit" {
				id, gid = f.exit, f.exitEntryGroup
			}
			pd := f.port()
			f.c.Do("POST", "/api/rules", directBody(gid, pd, "127.0.0.1", target), 201)
			awaitRevision(t, f.c, id, 0)
			direct := tcpExchange(t, pd, "L:")
			sampleTunnelBusiness(t, f, id, 3)
			requireClosed(t, directClient(t, pb))
			f.h.Clock.Advance(10 * time.Second)
			awaitNode(t, f.c, id, func(row map[string]any) bool { return row["last_seen_at"] == f.h.Clock.Now().Format(time.RFC3339Nano) })
			rev := nodeRow(t, f.c, id)["applied_revision"].(float64)
			pc := f.port()
			f.c.Do("POST", "/api/rules", directBody(gid, pc, "127.0.0.1", target), 201)
			awaitRevision(t, f.c, id, rev)
			requireClosed(t, directClient(t, pc))
			for _, conn := range []net.Conn{a1, a2, direct} {
				conn.SetDeadline(time.Now().Add(time.Second))
				conn.Write([]byte("ping"))
				b := make([]byte, 6)
				if _, err := io.ReadFull(conn, b); err != nil || string(b) != "L:ping" {
					t.Fatalf("满载影响已有连接: %q %v", b, err)
				}
				conn.SetDeadline(time.Time{})
			}
			f.c.Do("DELETE", "/api/rules/"+ra, nil, 204)
			awaitRevision(t, f.c, f.entry, 0)
			requireClosed(t, a1)
			requireClosed(t, a2)
			sampleTunnelBusiness(t, f, id, 1)
			b := tcpExchange(t, pb, "L:")
			b.(*net.TCPConn).SetLinger(0)
			b.Close()
			direct.Close()
			sampleTunnelBusiness(t, f, f.entry, 0)
			sampleTunnelBusiness(t, f, f.exit, 0)
		})
	}
}

// 入口不能通过新建会话绕过容量；出口也必须独立拒绝超额流。
func TestTunnelSessionStreamLimitRejectsAndReusesReleasedSlots(t *testing.T) {
	for _, side := range []string{"entry", "exit"} {
		t.Run(side, func(t *testing.T) {
			el, xl := forwarding.ResourceLimits{MaxTunnelStreams: 4}, forwarding.ResourceLimits{MaxTunnelStreams: 4}
			if side == "entry" {
				el.MaxTunnelStreams = 2
			} else {
				xl.MaxTunnelStreams = 2
			}
			f := tunnelLimitsSetup(t, false, el, xl)
			target := echoTarget(t, "S:")
			pa, _ := f.rule(t, target)
			pb, _ := f.rule(t, target)
			first, second := tcpExchange(t, pa, "S:"), tcpExchange(t, pb, "S:")
			for i := 0; i < 5; i++ {
				requireClosed(t, directClient(t, pb))
			}
			for _, id := range []string{f.entry, f.exit} {
				sampleTunnelBusiness(t, f, id, 2)
				rt := f.c.Do("GET", probePath(id), nil, 200)["realtime"].(map[string]any)
				if rt["tunnel_connections"] != float64(1) {
					t.Fatalf("容量不足时创建了额外会话: %v", rt)
				}
			}
			first.(*net.TCPConn).SetLinger(0)
			first.Close()
			sampleTunnelBusiness(t, f, f.entry, 1)
			sampleTunnelBusiness(t, f, f.exit, 1)
			replacement := tcpExchange(t, pa, "S:")
			second.Close()
			replacement.Close()
			sampleTunnelBusiness(t, f, f.entry, 0)
			sampleTunnelBusiness(t, f, f.exit, 0)
		})
	}
}

func TestTunnelHalfCloseTransfersBothDirectionsAndReleasesResources(t *testing.T) {
	for _, targetFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(targetFirst), func(t *testing.T) {
			f := tunnelLimitsSetup(t, false, forwarding.ResourceLimits{MaxConnections: 1}, forwarding.ResourceLimits{MaxConnections: 1})
			request, reply := bytes.Repeat([]byte("请求"), 50000), bytes.Repeat([]byte("回复"), 70000)
			l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { l.Close() })
			received := make(chan []byte, 1)
			go func() {
				conn, err := l.AcceptTCP()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				if targetFirst {
					conn.Write(reply)
					conn.CloseWrite()
				}
				data, _ := io.ReadAll(conn)
				received <- data
				if !targetFirst {
					conn.Write(reply)
					conn.CloseWrite()
				}
			}()
			port, _ := f.rule(t, l.Addr().(*net.TCPAddr).Port)
			conn := directClient(t, port)
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			if !targetFirst {
				conn.Write(request)
				conn.CloseWrite()
			}
			data, err := io.ReadAll(conn)
			if err != nil || !bytes.Equal(data, reply) {
				t.Fatalf("隧道回复截断: %d/%d %v", len(data), len(reply), err)
			}
			if targetFirst {
				conn.Write(request)
				conn.CloseWrite()
			}
			select {
			case data := <-received:
				if !bytes.Equal(data, request) {
					t.Fatalf("隧道请求截断: %d/%d", len(data), len(request))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("半关闭后的请求未传完")
			}
			sampleTunnelBusiness(t, f, f.entry, 0)
			sampleTunnelBusiness(t, f, f.exit, 0)
		})
	}
}

// 出口 DNS/拨号必须占待拨号槽；客户端 RST 与规则撤销要取消正在拨号的上下文。
func TestTunnelPendingDialBudgetTimeoutAndCancellation(t *testing.T) {
	for _, action := range []string{"timeout", "reset", "delete"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{}, 8)
			cancelled := make(chan struct{}, 1)
			exitLimits := forwarding.ResourceLimits{MaxPendingDials: 1, DialTimeout: 300 * time.Millisecond}
			if action != "timeout" {
				exitLimits.DialTimeout = time.Hour
			}
			f := tunnelLimitsSetup(t, false, forwarding.ResourceLimits{DialTimeout: time.Hour}, exitLimits, func(_ *node.Config, d *node.Dependencies) {
				d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
					_, p, _ := net.SplitHostPort(address)
					if p == "1" {
						started <- struct{}{}
						<-ctx.Done()
						cancelled <- struct{}{}
						return nil, ctx.Err()
					}
					return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
				}
			})
			pa, ra := f.rule(t, 1)
			pb, _ := f.rule(t, echoTarget(t, "P:"))
			first := directClient(t, pa)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("出口未开始拨号")
			}
			requireClosed(t, directClient(t, pb))
			if action == "reset" {
				first.Write([]byte("request"))
				first.SetLinger(0)
				first.Close()
			}
			if action == "delete" {
				f.c.Do("DELETE", "/api/rules/"+ra, nil, 204)
				awaitRevision(t, f.c, f.entry, 0)
			}
			if action != "reset" {
				requireClosed(t, first)
			}
			// 先等真实取消，不推进时钟，防止拨号超时替代取消而误通过。
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("出口拨号没有及时取消")
			}
			sampleTunnelBusiness(t, f, f.entry, 0)
			sampleTunnelBusiness(t, f, f.exit, 0)
			tcpExchange(t, pb, "P:").Close()
			sampleTunnelBusiness(t, f, f.exit, 0)
		})
	}
}

func TestTunnelIdleTimeoutRenewsOnDownstreamAndReclaimsHalfClosedFlow(t *testing.T) {
	f := tunnelLimitsSetup(t, false, forwarding.ResourceLimits{}, forwarding.ResourceLimits{})
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	commands, stop := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		conn, err := l.AcceptTCP()
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			select {
			case <-commands:
				if _, err := conn.Write([]byte("alive")); err != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	port, _ := f.rule(t, l.Addr().(*net.TCPAddr).Port)
	conn := directClient(t, port)
	conn.CloseWrite()
	readReply := func() {
		select {
		case commands <- struct{}{}:
		case <-time.After(time.Second):
			t.Fatal("目标未收到发送命令")
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		b := make([]byte, 5)
		if _, err := io.ReadFull(conn, b); err != nil || string(b) != "alive" {
			t.Fatalf("单向传输未续期: %q %v", b, err)
		}
		conn.SetReadDeadline(time.Time{})
	}
	readReply()
	for i := 0; i < 2; i++ {
		f.h.Clock.Advance(90 * time.Minute) // 留足余量:sampleTunnelBusiness 每轮快进 5 秒,负载下轮数不定
		readReply()
		sampleTunnelBusiness(t, f, f.entry, 1)
		sampleTunnelBusiness(t, f, f.exit, 1)
	}
	f.h.Clock.Advance(2 * time.Hour)
	requireClosed(t, conn)
	sampleTunnelBusiness(t, f, f.entry, 0)
	sampleTunnelBusiness(t, f, f.exit, 0)
}

// TLS、HTTP Upgrade、TLS 后认证三种半开握手均须按配置期限回收。
func TestTunnelHalfOpenHandshakesTimeoutAndLeaveControlResponsive(t *testing.T) {
	f := tunnelLimitsSetup(t, false, forwarding.ResourceLimits{}, forwarding.ResourceLimits{TunnelHandshakeTimeout: 150 * time.Millisecond})
	port, _ := f.rule(t, echoTarget(t, "H:"))
	exits := f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)
	var identity map[string]any
	for _, value := range exits {
		row := value.(map[string]any)
		if row["node_id"] == f.exit {
			identity = row
		}
	}
	if identity == nil {
		t.Fatal("出口未返回公开身份")
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(identity["listen_port"].(float64))))
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(identity["certificate_pem"].(string)))
	config := &tls.Config{RootCAs: roots, ServerName: identity["server_name"].(string), MinVersion: tls.VersionTLS12, Time: f.h.Clock.Now}
	t.Run("tls", func(t *testing.T) {
		conn, err := net.Dial("tcp4", address)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.Write([]byte{0x16})
		requireClosed(t, conn)
	})
	t.Run("http", func(t *testing.T) {
		conn, err := tls.Dial("tcp4", address, config)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.Write([]byte("GET /nbpass/tunnel/v1 HTTP/1.1\r\n"))
		requireClosed(t, conn)
	})
	t.Run("authentication", func(t *testing.T) {
		transport := &http.Transport{TLSClientConfig: config}
		defer transport.CloseIdleConnections()
		ws, _, err := websocket.Dial(context.Background(), "wss://"+address+"/nbpass/tunnel/v1", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, Subprotocols: []string{"nbpass.tunnel.v1"}})
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		requireHandshakeClosed(t, websocket.NetConn(context.Background(), ws, websocket.MessageBinary))
	})
	f.h.Clock.Advance(10 * time.Second)
	awaitNode(t, f.c, f.exit, func(row map[string]any) bool { return row["last_seen_at"] == f.h.Clock.Now().Format(time.RFC3339Nano) })
	tcpExchange(t, port, "H:")
}

func TestTunnelAuthenticationDeadlineUsesInjectedClock(t *testing.T) {
	f := tunnelLimitsSetup(t, false, forwarding.ResourceLimits{}, forwarding.ResourceLimits{TunnelHandshakeTimeout: time.Minute})
	f.rule(t, echoTarget(t, "C:"))
	exits := f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)
	identity := exits[0].(map[string]any)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(identity["certificate_pem"].(string)))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: identity["server_name"].(string), MinVersion: tls.VersionTLS12, Time: f.h.Clock.Now}}
	defer transport.CloseIdleConnections()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(identity["listen_port"].(float64))))
	ws, _, err := websocket.Dial(context.Background(), "wss://"+address+"/nbpass/tunnel/v1", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}, Subprotocols: []string{"nbpass.tunnel.v1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	// 写一个不完整帧，确保服务端已进入认证读；随后推进的是注入时钟。
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	if _, err := conn.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	f.h.Clock.Advance(time.Minute)
	requireHandshakeClosed(t, conn)
}

func requireHandshakeClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("半开握手返回了意外业务数据")
		}
	case <-time.After(time.Second):
		t.Fatal("半开握手未被服务端回收")
	}
}
