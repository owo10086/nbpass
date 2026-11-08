package forwarding

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// 保护失败门槛、恢复边界及未知结果；时间预期独立于实现计算。
func TestExitHealthThresholdAndRecoveryContinuity(t *testing.T) {
	c := &tunnelClient{}
	at := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	c.recordProbe(at, failureLink)
	c.recordProbe(at.Add(5*time.Second), failureLink)
	if !c.available() {
		t.Fatal("不足三次失败就切走")
	}
	c.recordProbe(at.Add(10*time.Second), failureLocal)
	if !c.available() {
		t.Fatal("本地资源被记为链路失败")
	}
	c.recordProbe(at.Add(15*time.Second), failureLink)
	if c.available() {
		t.Fatal("三次链路失败未切走")
	}
	for i := 0; i <= 11; i++ {
		c.recordProbe(at.Add(time.Duration(20+i*5)*time.Second), "")
	}
	if c.available() {
		t.Fatal("恢复不足60秒提前回切")
	}
	c.recordProbe(at.Add(80*time.Second), "")
	if !c.available() {
		t.Fatal("连续成功60秒未恢复")
	}
	c.linkFailed()
	c.recordProbe(at.Add(85*time.Second), "")
	c.recordProbe(at.Add(90*time.Second), failureConfig)
	c.recordProbe(at.Add(145*time.Second), "")
	if c.available() {
		t.Fatal("未完成/采样缺口被算为连续成功")
	}
}

func TestExitProxyUnavailableIsLinkFailure(t *testing.T) {
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unavailable", 503) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	p, _ := strconv.Atoi(port)
	c := &tunnelClient{engine: NewEngine(Dependencies{}), endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: uint32(p), ServerName: "exit.test", CertificatePem: cert}}
	_, err = c.openSession(context.Background())
	if err == nil || classifyTunnel(err) != failureLink {
		t.Fatalf("代理503未作为链路不可达: %v", err)
	}
}

func TestExitStreamOpeningRespectsRemainingBudget(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	session, err := yamux.Client(a, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	stream, err := openTunnelStream(ctx, session)
	if stream != nil || err == nil || time.Since(started) > 200*time.Millisecond {
		t.Fatal("yamux建流突破剩余连接预算")
	}
}

type blockedTunnelWrites struct {
	net.Conn
	blocked atomic.Bool
	release chan struct{}
	once    sync.Once
}

func (c *blockedTunnelWrites) Write(b []byte) (int, error) {
	if c.blocked.Load() {
		<-c.release
	}
	return c.Conn.Write(b)
}
func (c *blockedTunnelWrites) Close() error {
	c.once.Do(func() { close(c.release) })
	return c.Conn.Close()
}

func TestExitAuthorizationWriteRespectsRemainingBudget(t *testing.T) {
	a, b := net.Pipe()
	conn := &blockedTunnelWrites{Conn: a, release: make(chan struct{})}
	client, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(b, yamuxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	stream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	remote, err := server.AcceptStream()
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	conn.blocked.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = exchangeTunnelRequest(ctx, stream, tunnelRequest{RuleID: "r"})
	if err == nil || time.Since(started) > 200*time.Millisecond {
		t.Fatal("逐流授权写入突破剩余连接预算")
	}
}

func TestExitCandidateValidationFailsClosed(t *testing.T) {
	s := cachedTunnelFixture(t)
	ep := proto.Clone(s.ExitGroups[0].Exits[0]).(*controlpb.TunnelEndpoint)
	ep.NodeId = "second"
	s.ExitGroups[0].Exits = append(s.ExitGroups[0].Exits, ep)
	for _, ids := range [][]string{nil, {"missing"}, {ep.NodeId, ep.NodeId}} {
		s.Rules[0].AuthorizedExitIds = ids
		s.Digest = SnapshotDigest(s)
		if ValidateSnapshot(s) == nil {
			t.Fatalf("接受无效多出口候选: %v", ids)
		}
	}
}

func TestExitProbesShareEightSlotsAndIncompleteIsNotFailure(t *testing.T) {
	var active, peak atomic.Int32
	entered := make(chan struct{}, 12)
	e := NewEngine(Dependencies{DialTunnel: func(ctx context.Context, _ string) (net.Conn, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	cert, _, _, _ := tunnelTestIdentity(t, "exit.test", false)
	var clients []*tunnelClient
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		c := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: 443, ServerName: "exit.test", CertificatePem: cert}}
		clients = append(clients, c)
		wg.Add(1)
		go func() { defer wg.Done(); c.probeOnce(ctx) }()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("探测未发起")
		}
	}
	for i := 0; i < 4; i++ {
		c := &tunnelClient{engine: e}
		c.probeOnce(ctx)
		if !c.available() || c.failures != 0 || c.lastProbe != "incomplete" {
			t.Fatal("并发满额被计为失败")
		}
	}
	cancel()
	wg.Wait()
	deadline := time.Now().Add(time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if peak.Load() != 8 || active.Load() != 0 {
		t.Fatalf("并发或取消清理不符: %d/%d", peak.Load(), active.Load())
	}
	for _, c := range clients {
		if !c.available() || c.failures != 0 {
			t.Fatal("取消误伤健康")
		}
	}
}

func TestExitFailureAttribution(t *testing.T) {
	for _, err := range []error{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.EADDRNOTAVAIL} {
		if classifyTunnel(failTunnel(failureLink, err)) != failureLocal {
			t.Fatalf("本地资源误伤健康: %v", err)
		}
	}
	if classifyTunnel(failTunnel(failureTarget, errors.New("拒绝连接"))) != failureTarget {
		t.Fatal("目标失败误分类")
	}
	if classifyTunnel(failTunnel(failureLink, context.Canceled)) != failureLocal {
		t.Fatal("取消被计为链路故障")
	}
	if classifyTunnel(failTunnel(failureLink, &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}})) != failureConfig {
		t.Fatal("TLS配置错误被计为链路故障")
	}
}

func TestExitProbeJitterBounds(t *testing.T) {
	var different bool
	first := exitProbeDelay()
	for i := 0; i < 100; i++ {
		d := exitProbeDelay()
		if d < 4500*time.Millisecond || d > 5500*time.Millisecond {
			t.Fatalf("抖动越界: %v", d)
		}
		different = different || d != first
	}
	if !different {
		t.Fatal("周期没有随机抖动")
	}
}

func TestExitSessionWaitRespectsCallerBudget(t *testing.T) {
	entered := make(chan struct{}, 1)
	cert, _, _, _ := tunnelTestIdentity(t, "exit.test", false)
	e := NewEngine(Dependencies{DialTunnel: func(ctx context.Context, _ string) (net.Conn, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	c := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: 443, ServerName: "exit.test", CertificatePem: cert}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); c.getSession(ctx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("首次握手未发起")
	}
	queued, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	started := time.Now()
	_, err := c.getSession(queued)
	if err == nil || time.Since(started) > 200*time.Millisecond {
		t.Fatal("等待共享会话锁突破连接总预算")
	}
	cancel()
	<-done
}
