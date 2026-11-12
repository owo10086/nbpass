package forwarding

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"nbpass/internal/controlpb"
	"nbpass/internal/timing"
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
			e.tunnelClients[endpointKey(ep)] = c
			c.mu.Lock() // 模拟另一次仍在预算内的网络握手。
			defer c.mu.Unlock()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "unchanged_endpoint" {
					e.applyTunnelClients(map[[2]string]*controlpb.TunnelEndpoint{endpointKey(ep): ep})
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

// Only the entry authorization recheck uses one-second timers in this fixture.
// Publishing a second timer proves the first recheck completed, rather than
// assuming that advancing time means its goroutine already ran.
type entryRecheckClock struct {
	resourceClock
	created chan *entryRecheckTimer
}
type entryRecheckTimer struct{ ch chan time.Time }

func (t *entryRecheckTimer) C() <-chan time.Time { return t.ch }
func (t *entryRecheckTimer) Stop() bool          { return true }
func (c *entryRecheckClock) NewTimer(d time.Duration) timing.Timer {
	if d != time.Second {
		return c.resourceClock.NewTimer(d)
	}
	timer := &entryRecheckTimer{ch: make(chan time.Time, 1)}
	c.created <- timer
	return timer
}
func TestTunnelRotationFinalSnapshotPreservesRecheckedEntryStream(t *testing.T) {
	for _, hops := range []int{1, 2} {
		t.Run(fmt.Sprint(hops), func(t *testing.T) {
			f := newRelayFixture(t, hops)
			clock := &entryRecheckClock{created: make(chan *entryRecheckTimer, 8)}
			f.deps[0].Clock = clock
			if hops == 1 {
				r := f.snapshots[0].Rules[0]
				r.ChainId = ""
				r.ChainAuthorization = nil
				g := f.snapshots[1].TunnelServer.Grants[0]
				g.Rule.ChainId = ""
				g.Rule.ChainAuthorization = nil
				g.Rule.HopIndex = 0
				g.HopIndex = 0
			}
			f.start(t)
			c := f.connect(t)
			defer c.Close()
			relayExchange(t, c)
			var first *entryRecheckTimer
			select {
			case first = <-clock.created:
			case <-time.After(time.Second):
				t.Fatal("entry watcher did not start")
			}
			entry := proto.Clone(f.snapshots[0]).(*controlpb.ConfigSnapshot)
			server := proto.Clone(f.snapshots[1]).(*controlpb.ConfigSnapshot)
			ep := entry.ExitGroups[0].Exits[0]
			oldCert := ep.CertificatePem
			oldCredential := ep.Credential
			newCert, newKey, _, _ := tunnelTestIdentity(t, ep.ServerName, false)
			newCredential := strings.Repeat("z", 43)
			apply := func() {
				t.Helper()
				for _, pair := range []struct {
					e *Engine
					s *controlpb.ConfigSnapshot
				}{{f.engines[1], server}, {f.engines[0], entry}} {
					pair.s.Revision++
					pair.s.Digest = SnapshotDigest(pair.s)
					if err := pair.e.Apply(pair.s); err != nil {
						t.Fatal(err)
					}
				}
			}
			ep.RotationId = "explicit-rotation"
			ep.TrustedCertificatesPem = [][]byte{newCert}
			server.TunnelServer.RotationId = ep.RotationId
			server.TunnelServer.Peers[0].AdditionalCredentials = []string{newCredential}
			apply()
			ep.CertificatePem = newCert
			ep.TrustedCertificatesPem = [][]byte{oldCert}
			ep.Credential = newCredential
			server.TunnelServer.CertificatePem = newCert
			server.TunnelServer.PrivateKeyPem = newKey
			server.TunnelServer.Peers[0].Credential = newCredential
			server.TunnelServer.Peers[0].AdditionalCredentials = []string{oldCredential}
			apply()
			ep.RotationId = ""
			ep.TrustedCertificatesPem = nil
			server.TunnelServer.RotationId = ""
			server.TunnelServer.Peers[0].AdditionalCredentials = nil
			apply()
			current := f.engines[0].rules["r"].selection.Load().clients[0].currentEndpoint()
			if current.RotationId != "" || current.Credential != newCredential {
				t.Fatal("final snapshot not installed")
			}
			first.ch <- time.Now()
			select {
			case <-clock.created:
			case <-time.After(time.Second):
				t.Fatal("final rotation recheck revoked old entry stream")
			}
			relayExchange(t, c)
			fresh := f.connect(t)
			defer fresh.Close()
			relayExchange(t, fresh)
			// An ordinary credential replacement without a rotation transition must
			// still revoke the old carrier, even while the downstream keeps old state.
			ep.Credential = strings.Repeat("q", 43)
			entry.Revision++
			entry.Digest = SnapshotDigest(entry)
			if err := f.engines[0].Apply(entry); err != nil {
				t.Fatal(err)
			}
			c.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			if _, err := c.Read(b[:]); err == nil {
				t.Fatal("ordinary credential replacement retained old flow")
			} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("ordinary replacement did not revoke old carrier")
			}

		})
	}
}
