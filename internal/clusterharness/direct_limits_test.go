package clusterharness_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
)

// 预先 bind 并把真实监听交给节点，消除释放临时端口到规则应用之间的竞争。
func directPorts(t *testing.T) (func() int, func(*node.Config, *node.Dependencies)) {
	t.Helper()
	var mu sync.Mutex
	listeners := map[string]net.Listener{}
	reserve := func() int {
		t.Helper()
		l, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		mu.Lock()
		listeners[l.Addr().String()] = l
		mu.Unlock()
		return l.Addr().(*net.TCPAddr).Port
	}
	option := func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.Listen = func(network, address string) (net.Listener, error) {
			mu.Lock()
			l := listeners[address]
			delete(listeners, address)
			mu.Unlock()
			if l != nil {
				return l, nil
			}
			return net.Listen(network, address)
		}
	}
	return reserve, option
}

func directClient(t *testing.T, port int) *net.TCPConn {
	t.Helper()
	c, err := net.DialTCP("tcp4", nil, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// 半关闭不能截断反向的大响应，也不能阻止反向半关闭后继续接收请求。
func TestDirectHalfCloseTransfersBothDirectionsAndReleasesResources(t *testing.T) {
	for _, targetFirst := range []bool{false, true} {
		t.Run(strconv.FormatBool(targetFirst), func(t *testing.T) {
			h, c := nodeBrowser(t)
			n := addNode(c, "半关闭入口")
			id := n["id"].(string)
			reservePort, listenOption := directPorts(t)
			h.StartNode(n["installation_ticket"].(string), listenOption)
			awaitNode(t, c, id, connected)
			awaitUptime(t, c, id, 0)
			gid := entryGroup(c, id)["id"].(string)
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
			port := reservePort()
			c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", l.Addr().(*net.TCPAddr).Port), 201)
			awaitRevision(t, c, id, 0)
			conn := directClient(t, port)
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			if !targetFirst {
				conn.Write(request)
				conn.CloseWrite()
			}
			data, err := io.ReadAll(conn)
			if err != nil || !bytes.Equal(data, reply) {
				t.Fatalf("回复截断: %d/%d %v", len(data), len(reply), err)
			}
			if targetFirst {
				conn.Write(request)
				conn.CloseWrite()
			}
			select {
			case data := <-received:
				if !bytes.Equal(data, request) {
					t.Fatalf("请求截断: %d/%d", len(data), len(request))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("半关闭后的请求未传完")
			}
			sampleBusiness(t, h, c, id, 0)
		})
	}
}

// 双向共享的空闲期限为默认两小时；仅下行有数据也必须续期。
func TestDirectIdleTimeoutRenewsOnDownstreamAndReclaimsHalfClosedConnection(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "空闲入口")
	id := n["id"].(string)
	reservePort, listenOption := directPorts(t)
	h.StartNode(n["installation_ticket"].(string), listenOption)
	awaitNode(t, c, id, connected)
	awaitUptime(t, c, id, 0)
	gid := entryGroup(c, id)["id"].(string)
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
	port := reservePort()
	c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", l.Addr().(*net.TCPAddr).Port), 201)
	awaitRevision(t, c, id, 0)
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
			t.Fatalf("单向活性未续期: %q %v", b, err)
		}
		conn.SetReadDeadline(time.Time{})
	}
	readReply()
	sampleBusiness(t, h, c, id, 1)
	h.Clock.Advance(119 * time.Minute)
	readReply()
	sampleBusiness(t, h, c, id, 1)
	h.Clock.Advance(119 * time.Minute)
	readReply()
	sampleBusiness(t, h, c, id, 1)
	h.Clock.Advance(2 * time.Hour)
	requireClosed(t, conn)
	sampleBusiness(t, h, c, id, 0)
}

func requireClosed(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(time.Second))
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("连接未关闭")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("连接未回收，只是等待超时")
	}
}

func sampleBusiness(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, want float64) {
	t.Helper()
	sampleBusinessWithin(t, h, c, id, want, 5*time.Second)
}

func sampleBusinessWithin(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, want float64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		h.Clock.Advance(5 * time.Second)
		time.Sleep(10 * time.Millisecond)
		v := c.Do("GET", probePath(id), nil, 200)
		if rt, ok := v["realtime"].(map[string]any); ok && v["sampled_at"] == h.Clock.Now().Format(time.RFC3339Nano) && rt["business_connections"] == want && rt["tunnel_connections"] == float64(0) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("连接数未收敛到 %v: %v", want, v["realtime"])
		}
	}
}

// 丢失节点/规则的原子准入、拒绝时误伤已有连接或阻塞控制流时失败。
func TestDirectResourceLimitsRejectNewConnectionsAndKeepControlResponsive(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "直连容量")
	id := n["id"].(string)
	reservePort, listenOption := directPorts(t)
	h.StartNode(n["installation_ticket"].(string), listenOption, func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.Limits = forwarding.ResourceLimits{MaxConnections: 3, MaxConnectionsPerRule: 2}
	})
	awaitNode(t, c, id, connected)
	awaitUptime(t, c, id, 0)
	gid := entryGroup(c, id)["id"].(string)
	pa, pb := reservePort(), reservePort()
	target := echoTarget(t, "A:")
	ra := c.Do("POST", "/api/rules", directBody(gid, pa, "127.0.0.1", target), 201)
	c.Do("POST", "/api/rules", directBody(gid, pb, "127.0.0.1", target), 201)
	rev := awaitRevision(t, c, id, 0)
	a1, a2 := tcpExchange(t, pa, "A:"), tcpExchange(t, pa, "A:")
	sampleBusiness(t, h, c, id, 2)
	requireClosed(t, directClient(t, pa))
	b1 := tcpExchange(t, pb, "A:")
	sampleBusiness(t, h, c, id, 3)
	requireClosed(t, directClient(t, pb))
	// 满载时仍能独立推进活性，并应用新增规则的快照。
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] == h.Clock.Now().Format(time.RFC3339Nano) })
	pc := reservePort()
	c.Do("POST", "/api/rules", directBody(gid, pc, "127.0.0.1", target), 201)
	rev = awaitRevision(t, c, id, rev)
	requireClosed(t, directClient(t, pc))
	for _, conn := range []net.Conn{a1, a2, b1} {
		conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 6)
		if _, err := io.ReadFull(conn, b); err != nil || string(b) != "A:ping" {
			t.Fatalf("超限影响已有连接: %q %v", b, err)
		}
		conn.SetDeadline(time.Time{})
	}
	c.Do("DELETE", "/api/rules/"+ra["id"].(string), nil, 204)
	awaitRevision(t, c, id, rev)
	requireClosed(t, a1)
	requireClosed(t, a2)
	sampleBusiness(t, h, c, id, 1)
	c1 := tcpExchange(t, pc, "A:")
	b1.Close()
	c1.Close()
	sampleBusiness(t, h, c, id, 0)
}

// DNS 也占待拨号槽；满载、撤销或超时不得泄漏槽或堵住配置应用。
func TestDirectPendingDialLimitCancellationAndTimeout(t *testing.T) {
	for _, cancelRule := range []bool{true, false} {
		t.Run(strconv.FormatBool(cancelRule), func(t *testing.T) {
			h, c := nodeBrowser(t)
			n := addNode(c, "待拨号容量")
			id := n["id"].(string)
			reservePort, listenOption := directPorts(t)
			started := make(chan struct{}, 4)
			h.StartNode(n["installation_ticket"].(string), listenOption, func(_ *node.Config, d *node.Dependencies) {
				d.Forwarding.Limits = forwarding.ResourceLimits{MaxPendingDials: 1, DialTimeout: 300 * time.Millisecond}
				d.Forwarding.LookupIPv4 = func(ctx context.Context, _ string) ([]netip.Addr, error) {
					started <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				}
			})
			awaitNode(t, c, id, connected)
			awaitUptime(t, c, id, 0)
			gid := entryGroup(c, id)["id"].(string)
			pa, pb := reservePort(), reservePort()
			ra := c.Do("POST", "/api/rules", directBody(gid, pa, "stall.test", 80), 201)
			c.Do("POST", "/api/rules", directBody(gid, pb, "127.0.0.1", echoTarget(t, "B:")), 201)
			rev := awaitRevision(t, c, id, 0)
			first := directClient(t, pa)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("未开始目标解析")
			}
			requireClosed(t, directClient(t, pb))
			if cancelRule {
				c.Do("DELETE", "/api/rules/"+ra["id"].(string), nil, 204)
				awaitRevision(t, c, id, rev)
			}
			requireClosed(t, first)
			sampleBusiness(t, h, c, id, 0)
			tcpExchange(t, pb, "B:").Close()
			sampleBusiness(t, h, c, id, 0)
		})
	}
}

// 客户端 RST 必须取消未完成的拨号，不能一直占槽直到拨号总期限。
func TestDirectClientResetCancelsPendingDial(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "客户端取消")
	id := n["id"].(string)
	reservePort, listenOption := directPorts(t)
	started := make(chan struct{}, 1)
	h.StartNode(n["installation_ticket"].(string), listenOption, func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.Limits = forwarding.ResourceLimits{MaxPendingDials: 1, DialTimeout: time.Minute}
		d.Forwarding.DialTarget = func(ctx context.Context, _ string) (net.Conn, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	awaitNode(t, c, id, connected)
	awaitUptime(t, c, id, 0)
	gid := entryGroup(c, id)["id"].(string)
	port := reservePort()
	c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", 80), 201)
	awaitRevision(t, c, id, 0)
	conn := directClient(t, port)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未开始拨号")
	}
	if _, err := conn.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	sampleBusiness(t, h, c, id, 1)
	conn.SetLinger(0)
	conn.Close()
	sampleBusinessWithin(t, h, c, id, 0, time.Second)
}
