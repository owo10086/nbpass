package forwarding

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"nbpass/internal/controlpb"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 授权内容不变时，凭据轮换和已有流复查并发也不能制造假撤权。
func TestTunnelRotationExistingStreamAuthorizationIsAtomic(t *testing.T) {
	rule := &controlpb.ForwardRule{Id: "r", AuthorizationGeneration: 1, TargetAddress: "example.test", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}
	config := func(credential string) *controlpb.TunnelServer {
		return &controlpb.TunnelServer{RotationId: "rotation", Peers: []*controlpb.TunnelPeer{{EntryId: "entry", Credential: credential}}, Grants: []*controlpb.TunnelGrant{{EntryId: "entry", Rule: rule}}}
	}
	s := &tunnelServer{config: config(strings.Repeat("a", 43))}
	auth := tunnelAuth{EntryID: "entry", Credential: strings.Repeat("a", 43)}
	request := streamAuthorization("entry", rule)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			s.update(config(strings.Repeat(string(rune('a'+i%2)), 43)))
		}
	}()
	defer wg.Wait()
	for i := 0; i < 10000; i++ {
		if s.authorizeExistingStream(auth, request) == nil {
			t.Fatal("轮换凭据竞态误撤销已有流")
		}
	}
}

type pausedTunnelConn struct {
	net.Conn
	armed  atomic.Bool
	paused chan struct{}
	resume chan struct{}
}

func (c *pausedTunnelConn) Write(data []byte) (int, error) {
	if c.armed.CompareAndSwap(true, false) {
		close(c.paused)
		<-c.resume
	}
	return c.Conn.Write(data)
}

// 将真实 TLS/WSS/yamux 的旧载体写入暂停，精确构造“已取旧载体、尚未授权即撤旧”。
func TestTunnelRotationRetriesUncommittedDialOnCurrentCarrier(t *testing.T) {
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	rule := &controlpb.ForwardRule{Id: "r", AuthorizationGeneration: 1, Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, TargetAddress: "127.0.0.1", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, AllowPrivate: true}
	e := NewEngine(Dependencies{DialTarget: func(context.Context, string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() { defer b.Close(); b.Write([]byte("ok")) }()
		return a, nil
	}})
	config := &controlpb.TunnelServer{CertificatePem: cert, PrivateKeyPem: key, Peers: []*controlpb.TunnelPeer{{EntryId: "entry", Credential: strings.Repeat("a", 43)}}, Grants: []*controlpb.TunnelGrant{{EntryId: "entry", Rule: rule}}}
	server, err := e.startTunnelServer(config)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	var wire *pausedTunnelConn
	e.deps.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		if err != nil {
			return nil, err
		}
		if wire == nil {
			wire = &pausedTunnelConn{Conn: conn, paused: make(chan struct{}), resume: make(chan struct{})}
			return wire, nil
		}
		return conn, nil
	}
	ep := &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: uint32(server.listener.Addr().(*net.TCPAddr).Port), ServerName: "exit.test", CertificatePem: cert, EntryId: "entry", Credential: strings.Repeat("a", 43)}
	client := &tunnelClient{engine: e, endpoint: ep}
	defer client.close()
	if _, err := client.getSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	wire.armed.Store(true)
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() { conn, err := client.dial(context.Background(), rule); done <- result{conn, err} }()
	select {
	case <-wire.paused:
	case <-time.After(2 * time.Second):
		t.Fatal("旧载体未进入受控暂停")
	}
	updated := proto.Clone(ep).(*controlpb.TunnelEndpoint)
	updated.Credential = strings.Repeat("b", 43)
	client.updateEndpoint(updated)
	next := proto.Clone(config).(*controlpb.TunnelServer)
	next.Peers[0].Credential = updated.Credential
	server.update(next)
	close(wire.resume)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("轮换中尚未交付的新连接失败: %v", got.err)
		}
		defer got.conn.Close()
		got.conn.SetReadDeadline(time.Now().Add(time.Second))
		data := make([]byte, 2)
		if _, err := io.ReadFull(got.conn, data); err != nil || string(data) != "ok" {
			t.Fatalf("当前载体重试未到目标: %q %v", data, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("在途新连接未恢复")
	}
}

// 轮换重试不得重新等待已超时的会话锁，预检和业务共享调用方期限。
func TestTunnelRotationRetryKeepsCallerBudget(t *testing.T) {
	for _, check := range []bool{false, true} {
		t.Run(fmt.Sprint(check), func(t *testing.T) {
			e := NewEngine(Dependencies{})
			c := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{EntryId: "entry"}}
			c.mu.Lock()
			defer c.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				r := &controlpb.ForwardRule{Id: "r"}
				if check {
					done <- c.ready(ctx, r)
				} else {
					_, err := c.dial(ctx, r)
					done <- err
				}
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("排队超时仍成功")
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("轮换重试等待锁突破调用方期限")
			}
		})
	}
}

func TestTunnelRotationOrdinaryCredentialReplacementStillRevokes(t *testing.T) {
	rule := &controlpb.ForwardRule{Id: "r", AuthorizationGeneration: 1}
	s := &tunnelServer{config: &controlpb.TunnelServer{Peers: []*controlpb.TunnelPeer{{EntryId: "entry", Credential: strings.Repeat("a", 43)}}, Grants: []*controlpb.TunnelGrant{{EntryId: "entry", Rule: rule}}}}
	auth := tunnelAuth{EntryID: "entry", Credential: strings.Repeat("a", 43)}
	request := streamAuthorization("entry", rule)
	if s.authorizeStream(auth, request) == nil {
		t.Fatal("初始授权未通过")
	}
	next := proto.Clone(s.config).(*controlpb.TunnelServer)
	next.Peers[0].Credential = strings.Repeat("b", 43)
	s.update(next)
	if s.authorizeExistingStream(auth, request) != nil {
		t.Fatal("普通凭据替换没有撤销旧凭据的已有流")
	}
}

func TestTunnelRotationUnchangedEndpointAndReferenceReleaseDoNotWaitForHandshake(t *testing.T) {
	for _, operation := range []string{"unchanged_endpoint", "release_reference"} {
		t.Run(operation, func(t *testing.T) {
			e := NewEngine(Dependencies{})
			ep := &controlpb.TunnelEndpoint{NodeId: "exit", EntryId: "entry"}
			session := &tunnelSession{}
			c := &tunnelClient{engine: e, endpoint: ep, session: session, users: map[*tunnelSession]int{session: 1}}
			e.tunnelClients[ep.NodeId] = c
			c.mu.Lock() // 模拟另一次仍在预算内的网络握手。
			defer c.mu.Unlock()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "unchanged_endpoint" {
					e.applyTunnelClients(map[string]*controlpb.TunnelEndpoint{ep.NodeId: ep})
				} else {
					c.releaseSession(session)
				}
			}()
			select {
			case <-done:
			case <-time.After(200 * time.Millisecond):
				t.Fatal("无网络操作错误等待了网络握手锁")
			}
		})
	}
}
