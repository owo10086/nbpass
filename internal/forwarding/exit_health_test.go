package forwarding

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	yamux "github.com/libp2p/go-yamux/v5"
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
	engine := NewEngine(Dependencies{})
	session, err := yamux.Client(a, engine.yamuxClientConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	client := &tunnelClient{engine: engine}
	for i := 0; i < 1024; i++ {
		if _, err = client.openStream(ctx, session); err != nil {
			break
		}
	}
	if err == nil || time.Since(started) > 200*time.Millisecond {
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
	engine := NewEngine(Dependencies{})
	client, err := yamux.Client(conn, engine.yamuxClientConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(b, engine.yamuxConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	stream, err := client.OpenStream(context.Background())
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
	s := cachedTunnelFixture(t, &Dependencies{})
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

// 多出口的预检仍逐规则过滤授权，任一已确认出口通过即可启用监听。
func TestExitReadinessUsesConfirmedBackup(t *testing.T) {
	for _, confirmPrimary := range []bool{false, true} {
		t.Run(fmt.Sprint(confirmPrimary), func(t *testing.T) {
			deps := Dependencies{}
			snapshot := cachedTunnelFixture(t, &deps)
			exit := NewEngine(deps)
			defer exit.Close()
			config := &controlpb.ConfigSnapshot{Revision: 1, TunnelServer: snapshot.TunnelServer}
			config.Digest = SnapshotDigest(config)
			if err := exit.Apply(config); err != nil {
				t.Fatal(err)
			}
			snapshot.TunnelServer = nil
			group := snapshot.ExitGroups[0]
			backup := group.Exits[0]
			primary := proto.Clone(backup).(*controlpb.TunnelEndpoint)
			primary.NodeId, primary.Credential = "denied-primary", strings.Repeat("b", 43)
			group.Exits = []*controlpb.TunnelEndpoint{primary, backup}
			snapshot.Rules[0].AuthorizedExitIds = []string{backup.NodeId}
			if confirmPrimary {
				snapshot.Rules[0].AuthorizedExitIds = append(snapshot.Rules[0].AuthorizedExitIds, primary.NodeId)
			}
			snapshot.Digest = SnapshotDigest(snapshot)
			entry := NewEngine(deps)
			defer entry.Close()
			if err := entry.Apply(snapshot); err != nil {
				t.Fatalf("主出口预检拒绝阻止了已确认备出口: %v", err)
			}
			conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(snapshot.Rules[0].ListenPort))), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
		})
	}
}

// 真实 yamux 发送队列堵塞时，预检按调用方期限返回；后台 RST 完成前
// 节点/规则/待拨号预算仍保留，解除背压后同会话可继续使用。
func TestExitReadinessBudgetIncludesResetWait(t *testing.T) {
	a, b := net.Pipe()
	wire := &blockedTunnelWrites{Conn: a, release: make(chan struct{})}
	engine := NewEngine(Dependencies{Limits: ResourceLimits{MaxConnections: 1, MaxConnectionsPerRule: 1, MaxPendingDials: 1, TunnelHandshakeTimeout: time.Second}})
	config := engine.yamuxClientConfig()
	config.ConnectionWriteTimeout = 2 * time.Second
	session, err := yamux.Client(wire, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	server, err := yamux.Server(b, engine.yamuxConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client := &tunnelClient{engine: engine, endpoint: &controlpb.TunnelEndpoint{EntryId: "entry"}, session: &tunnelSession{Session: session, checkSupported: true}}
	requested := make(chan struct{})
	remoteDone := make(chan error, 1)
	go func() {
		stream, err := server.AcceptStream()
		if err != nil {
			remoteDone <- err
			return
		}
		defer stream.Reset()
		var request tunnelRequest
		if err := readTunnelMessage(stream, &request); err != nil {
			remoteDone <- err
			return
		}
		close(requested)
		_, err = stream.Read(make([]byte, 1))
		remoteDone <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.ready(ctx, &controlpb.ForwardRule{Id: "r"}) }()
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("预检未开始")
	}
	wire.blocked.Store(true)
	for i := 0; i < 100; i++ {
		attempt, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err = session.OpenStream(attempt)
		stop()
		if errors.Is(err, yamux.ErrTimeout) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if i == 99 {
			t.Fatal("未形成真实载体背压")
		}
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("无响应的预检成功")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("预检超时后仍同步等待RST，突破总预算")
	}
	if lease := engine.resources.acquire("r"); lease != nil {
		lease.release()
		t.Fatal("RST尚未完成即归还预算")
	}
	wire.once.Do(func() { close(wire.release) })
	waitTunnelResourcesReleased(t, engine)
	select {
	case err := <-remoteDone:
		if !errors.Is(err, yamux.ErrStreamReset) {
			t.Fatalf("未回收远端流: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RST未完成")
	}
	if session.IsClosed() {
		t.Fatal("正常回收单流关闭了共享会话")
	}
}

func TestExitSerialReadinessReleasesPendingBudgetBeforeNextRule(t *testing.T) {
	_, client, rule := limitedTunnelPair(t, ResourceLimits{})
	client.engine = NewEngine(Dependencies{Limits: ResourceLimits{MaxPendingDials: 1}})
	for i := 0; i < 20; i++ {
		if err := client.ready(context.Background(), rule); err != nil {
			t.Fatalf("串行预检被前一次已完成清理占槽: %v", err)
		}
		lease := client.engine.resources.acquire("next-rule")
		if lease == nil {
			t.Fatal("预检已返回但未同步释放待拨号预算")
		}
		lease.release()
	}
}
