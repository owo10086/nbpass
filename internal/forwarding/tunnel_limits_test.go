package forwarding

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	legacyyamux "github.com/hashicorp/yamux"
	yamux "github.com/libp2p/go-yamux/v5"
	"nbpass/internal/controlpb"
)

func limitedTunnelPair(t *testing.T, limits ResourceLimits) (*Engine, *tunnelClient, *controlpb.ForwardRule) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	engine := NewEngine(Dependencies{Limits: limits})
	rule := &controlpb.ForwardRule{Id: "r", Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, TargetAddress: "127.0.0.1", TargetPort: uint32(listener.Addr().(*net.TCPAddr).Port), AllowPrivate: true}
	server, err := engine.startTunnelServer(&controlpb.TunnelServer{CertificatePem: cert, PrivateKeyPem: key, Peers: []*controlpb.TunnelPeer{{EntryId: "entry", Credential: strings.Repeat("a", 43)}}, Grants: []*controlpb.TunnelGrant{{EntryId: "entry", Rule: rule}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.close)
	client := &tunnelClient{engine: NewEngine(Dependencies{}), endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: uint32(server.listener.Addr().(*net.TCPAddr).Port), ServerName: "exit.test", CertificatePem: cert, EntryId: "entry", Credential: strings.Repeat("a", 43)}}
	t.Cleanup(client.close)
	return engine, client, rule
}

// 默认第 1025 条流必须失败；取消一条后同一会话恢复准入，不能只检查配置值。
func TestTunnelDefaultSessionStreamBudget(t *testing.T) {
	a, b := net.Pipe()
	engine := NewEngine(Dependencies{})
	session, err := yamux.Client(a, engine.yamuxConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	server, err := yamux.Server(b, engine.yamuxConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	accepted := make(chan *yamux.Stream, 1024)
	go func() {
		for {
			stream, err := server.AcceptStream()
			if err != nil {
				return
			}
			accepted <- stream
		}
	}()
	client := &tunnelClient{engine: engine}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var streams []*yamux.Stream
	for i := 0; i < 1024; i++ {
		stream, err := client.openStream(ctx, session)
		if err != nil {
			t.Fatalf("第 %d 条流被提前拒绝: %v", i+1, err)
		}
		streams = append(streams, stream)
		select {
		case <-accepted:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if stream, err := client.openStream(ctx, session); !errors.Is(err, errTunnelCapacity) {
		if stream != nil {
			stream.Reset()
		}
		t.Fatalf("第 1025 条流未按容量拒绝: %v", err)
	}
	streams[0].Reset()
	stream, err := client.openStream(ctx, session)
	if err != nil {
		t.Fatalf("取消后未释放流槽: %v", err)
	}
	stream.Reset()
}

// 对端绕过入口门禁直接打开多条流，出口仍须拒绝；无规则请求也受握手期限约束。
func TestTunnelUnfinishedStreamHandshakeAndReceiverCapacity(t *testing.T) {
	engine, client, rule := limitedTunnelPair(t, ResourceLimits{MaxTunnelStreams: 1, TunnelHandshakeTimeout: 150 * time.Millisecond})
	session, err := client.getSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Reset()
	deadline := time.Now().Add(time.Second)
	for {
		if business, _ := engine.Connections(); business == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("半开流未预占业务预算")
		}
		time.Sleep(time.Millisecond)
	}
	extra, err := session.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Reset()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := extra.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrStreamReset) {
		t.Fatalf("出口流数门禁未强制重置: %v", err)
	}
	first.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := first.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrStreamReset) {
		t.Fatalf("逐流半开握手未重置: %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for {
		if business, _ := engine.Connections(); business == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("半开流未释放预算")
		}
		time.Sleep(time.Millisecond)
	}
	conn, err := client.dial(context.Background(), rule)
	if err != nil {
		t.Fatalf("半开流回收后未恢复: %v", err)
	}
	conn.Close()
}

// 满发送窗口下 Reset 必须同时唤醒读写并删除两端流，其他流继续工作。
func TestTunnelResetUnblocksBackpressureWithoutClosingSession(t *testing.T) {
	_, client, rule := limitedTunnelPair(t, ResourceLimits{})
	conn, err := client.dial(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := io.CopyN(conn, zeroReader{}, 8*1024*1024); done <- err }()
	// 先确认已把发送窗口用满：大写入不会在未读取回复时自然完成。
	select {
	case err := <-done:
		t.Fatalf("未施加背压: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	conn.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消后写入仍全部成功")
		}
	case <-time.After(time.Second):
		t.Fatal("重置未解除写背压")
	}
	deadline := time.Now().Add(time.Second)
	for client.session.NumStreams() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("重置未清理会话流")
		}
		time.Sleep(time.Millisecond)
	}
	other, err := client.dial(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetDeadline(time.Now().Add(time.Second))
	other.Write([]byte("ok"))
	b := make([]byte, 2)
	if _, err := io.ReadFull(other, b); err != nil || string(b) != "ok" {
		t.Fatalf("取消影响其他流: %q %v", b, err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

// 已取消的请求（例如等会话锁期间收到 RST）不能留下对端看不到的流。
func TestTunnelCanceledOpenDoesNotConsumeSessionCapacity(t *testing.T) {
	_, client, rule := limitedTunnelPair(t, ResourceLimits{})
	session, err := client.getSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 100; i++ {
		stream, err := client.openStream(ctx, session)
		if stream != nil {
			stream.Reset()
			t.Fatal("已取消请求仍成功开流")
		}
		if err == nil {
			t.Fatal("已取消请求没有错误")
		}
	}
	if session.NumStreams() != 0 {
		t.Fatalf("已取消开流泄漏 %d 个槽", session.NumStreams())
	}
	conn, err := client.dial(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

// 入口从不接收反向业务流，不能让对端用反向 SYN 绕过会话总流数预算。
func TestTunnelEntryRejectsReverseStreams(t *testing.T) {
	a, b := net.Pipe()
	engine := NewEngine(Dependencies{Limits: ResourceLimits{MaxTunnelStreams: 1}})
	client, err := yamux.Client(a, engine.yamuxClientConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(b, engine.yamuxConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	stream, err := server.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Reset()
	stream.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrStreamReset) {
		t.Fatalf("入口未拒绝反向开流: %v", err)
	}
}

// 底层写仍在进行时，发送队列排队也必须服从开流总期限。
func TestTunnelOpenQueueHasTotalDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close() // 对端不读取，最终让发送队列产生背压。
	engine := NewEngine(Dependencies{Limits: ResourceLimits{MaxTunnelStreams: 1024, TunnelHandshakeTimeout: 50 * time.Millisecond}})
	config := engine.yamuxConfig()
	config.ConnectionWriteTimeout = 2 * time.Second
	session, err := yamux.Client(a, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client := &tunnelClient{engine: engine}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 1024; i++ {
			if _, err := client.openStream(context.Background(), session); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("发送队列没有施加背压")
		}
	case <-time.After(time.Second):
		t.Fatal("开流排队超出总期限，仍在等待单次底层写期限")
	}
	if session.NumStreams() != 0 {
		t.Fatalf("排队超时留下 %d 条隐藏流", session.NumStreams())
	}
}

// 载体发送队列满时 FIN/RST 都必须有排队总期限，否则删规则会阻塞控制处理。
func TestTunnelCloseQueueHasTotalDeadline(t *testing.T) {
	for _, half := range []bool{false, true} {
		t.Run(map[bool]string{false: "RST", true: "FIN"}[half], func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			wire := &tunnelWriteSignal{Conn: a, started: make(chan struct{})}
			engine := NewEngine(Dependencies{Limits: ResourceLimits{TunnelHandshakeTimeout: 50 * time.Millisecond}})
			config := engine.yamuxConfig()
			config.ConnectionWriteTimeout = 2 * time.Second
			session, err := yamux.Client(wire, config, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			first, err := session.OpenStream(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-wire.started:
			case <-time.After(time.Second):
				t.Fatal("载体未开始写入")
			}
			// 用短期的底层开流观察队列背压，不假定即时 RTT 帧占用了几个槽。
			for i := 0; i < 100; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				_, err := session.OpenStream(ctx)
				cancel()
				if errors.Is(err, yamux.ErrTimeout) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if i == 99 {
					t.Fatal("发送队列未产生背压")
				}
			}
			done := make(chan error, 1)
			conn := &tunnelStream{Stream: first, engine: engine}
			go func() {
				if half {
					done <- conn.CloseWrite()
				} else {
					done <- conn.Close()
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("队列阻塞未按总期限结束: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("关闭流卡在载体发送队列")
			}
			session.Close()
			if session.NumStreams() != 0 {
				t.Fatal("关闭超时未清理会话流")
			}
		})
	}
}

type tunnelWriteSignal struct {
	net.Conn
	once    sync.Once
	started chan struct{}
}

// 两处取消可能同时 Close；后一次不能抢先返回并让业务槽先于 yamux 槽释放。
func TestTunnelConcurrentCloseWaitsForStreamCleanup(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	engine := NewEngine(Dependencies{Limits: ResourceLimits{TunnelHandshakeTimeout: 200 * time.Millisecond}})
	config := engine.yamuxConfig()
	config.ConnectionWriteTimeout = 2 * time.Second
	session, err := yamux.Client(a, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stream, err := session.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := session.OpenStream(ctx)
		cancel()
		if errors.Is(err, yamux.ErrTimeout) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if i == 99 {
			t.Fatal("发送队列未产生背压")
		}
	}
	conn := &tunnelStream{Stream: stream, engine: engine}
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); conn.Close() }()
	stream.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, yamux.ErrStreamReset) {
		t.Fatalf("未进入重置状态: %v", err)
	}
	conn.Close()
	if session.NumStreams() != 0 {
		t.Fatalf("并发 Close 抢先返回，尚有 %d 个未释放流槽", session.NumStreams())
	}
	<-firstDone
}

// 载体关闭不能等待不再读数据的对端完成 WebSocket 关闭握手。
func TestTunnelCarrierCloseDoesNotWaitForUnresponsivePeer(t *testing.T) {
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{tunnelSubprotocol}})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
		var auth tunnelAuth
		if readTunnelMessage(conn, &auth) != nil {
			return
		}
		if writeTunnelMessage(conn, tunnelResult{OK: true}) != nil {
			return
		}
		<-release
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	defer close(release)
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	client := &tunnelClient{engine: NewEngine(Dependencies{}), endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: uint32(p), ServerName: "exit.test", CertificatePem: cert, EntryId: "entry", Credential: strings.Repeat("a", 43)}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.getSession(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { client.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("关闭载体仍在等对端关闭握手")
	}
}

func (c *tunnelWriteSignal) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(b)
}

// 滚动更新仍须与票 08 使用的 yamux v0.1.2 互通，包括 FIN 后的完整回复。
func TestTunnelYamuxCompatibilityWithPreviousImplementation(t *testing.T) {
	for _, newClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "旧入口", true: "旧出口"}[newClient], func(t *testing.T) {
			a, b := net.Pipe()
			engine := NewEngine(Dependencies{})
			oldConfig := legacyyamux.DefaultConfig()
			oldConfig.LogOutput = io.Discard
			var conn net.Conn
			var accept func() (net.Conn, error)
			if newClient {
				client, err := yamux.Client(a, engine.yamuxConfig(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				server, err := legacyyamux.Server(b, oldConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close()
				accept = server.Accept
				stream, err := client.OpenStream(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				conn = &tunnelStream{Stream: stream, engine: engine}
			} else {
				client, err := legacyyamux.Client(a, oldConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				server, err := yamux.Server(b, engine.yamuxConfig(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer server.Close()
				accept = server.Accept
				conn, err = client.Open()
				if err != nil {
					t.Fatal(err)
				}
			}
			defer conn.Close()
			go func() {
				peer, err := accept()
				if err != nil {
					return
				}
				defer peer.Close()
				data, _ := io.ReadAll(peer)
				peer.Write(append([]byte("reply:"), data...))
			}()
			conn.SetDeadline(time.Now().Add(time.Second))
			conn.Write([]byte("body"))
			if newClient {
				conn.(interface{ CloseWrite() error }).CloseWrite()
			} else {
				conn.Close()
			}
			data, err := io.ReadAll(conn)
			if err != nil || string(data) != "reply:body" {
				t.Fatalf("新旧 yamux 不兼容: %q %v", data, err)
			}
		})
	}
}
