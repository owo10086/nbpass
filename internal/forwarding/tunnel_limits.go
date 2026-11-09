package forwarding

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	yamux "github.com/libp2p/go-yamux/v5"
)

// 接收流在读取规则身份之前也占节点/待拨号槽，避免半开逐流握手绕过预算。
func (l *resourceLimiter) reserveTunnel() *resourceLease {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.connections >= l.limits.MaxConnections || l.pending >= l.limits.MaxPendingDials {
		return nil
	}
	l.connections++
	l.pending++
	return &resourceLease{owner: l, pending: true}
}

func (l *resourceLease) assignRule(rule string) bool {
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	if l.released || l.owner.rules[rule] >= l.owner.limits.MaxConnectionsPerRule {
		return false
	}
	l.rule = rule
	l.owner.rules[rule]++
	return true
}

func (r *listenerRule) acceptTunnel(client net.Conn) {
	lease := r.engine.resources.acquire(r.config.Id)
	if lease == nil {
		client.Close()
		return
	}
	if !r.track(client) {
		lease.release()
		return
	}
	finishTraffic := r.engine.meterConnection(r.config.Id)
	r.engine.businessConnections.Add(1)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		cleanup := &tunnelAttemptCleanup{}
		defer cleanup.after(lease.release)
		defer finishTraffic()
		defer r.engine.businessConnections.Add(^uint64(0))
		defer r.untrack(client)
		r.serveLimitedTunnel(client, lease, cleanup)
	}()
}

func (r *listenerRule) serveLimitedTunnel(client net.Conn, lease *resourceLease, cleanup *tunnelAttemptCleanup) {
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	input := newDirectInput(ctx, client, cancel, r.engine.deps.Clock)
	defer func() { cancel(); client.Close(); <-input.done }()
	selection := r.selection.Load()
	if selection == nil {
		return
	}
	target, err := selection.dial(ctx, cleanup)
	cleanup.after(lease.dialDone)
	if err != nil {
		return
	}
	if !r.track(target) {
		return
	}
	defer r.untrack(target)
	client, target = r.engine.meteredConns(r.config.Id, client, target)
	proxyDirect(ctx, client, target, input, r.engine.resources.limits.IdleTimeout)
}

// 同机入口已经预占逻辑业务与拨号预算；出口只在其他入口交来时扣槽。
// 解析和目标拨号期间仍预读有界数据，以便 RST 立即取消未完成的目标拨号。
func (s *tunnelServer) serveLimitedStream(parent context.Context, stream net.Conn, auth tunnelAuth, lease *resourceLease) {
	if lease != nil {
		defer lease.release()
		s.engine.businessConnections.Add(1)
		defer s.engine.businessConnections.Add(^uint64(0))
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	defer stop()
	handshake, stopHandshakeTimer := s.engine.tunnelTimeout(ctx, s.engine.resources.limits.TunnelHandshakeTimeout)
	defer stopHandshakeTimer()
	stopHandshake := context.AfterFunc(handshake, func() { stream.Close() })
	defer stopHandshake()
	stream.SetDeadline(time.Now().Add(s.engine.resources.limits.TunnelHandshakeTimeout))
	var request tunnelRequest
	if err := readTunnelMessage(stream, &request); err != nil || request.RuleID == "" {
		return
	}
	if !stopHandshake() || handshake.Err() != nil {
		return
	}
	stopHandshakeTimer()
	check := request.Check != nil
	if check {
		if request.RuleID != request.Check.Id {
			s.rejectStream(stream)
			return
		}
		// 兼容票22旧入口的完整规则对象；认证会话提供入口身份，
		// 监听端口等元数据不参与跨节点授权。
		request = streamAuthorization(auth.EntryID, request.Check)
	}
	rule := s.authorizeStream(auth, request)
	if rule == nil {
		s.rejectStream(stream)
		return
	}
	if lease != nil && !lease.assignRule(rule.Id) {
		s.finishStream(stream, tunnelResult{Failure: failureLocal})
		return
	}
	if check {
		// 预检只核验授权，不拨目标；FIN 保留响应，随后 RST 回收流槽。
		s.finishStream(stream, tunnelResult{OK: true, Ready: true})
		return
	}
	// 在目标拨号前复查授权，撤权同时取消 DNS/拨号并归还共享预算。
	stopAuthorization := s.watchStreamAuthorization(ctx, cancel, stream, auth, request)
	defer stopAuthorization()
	stream.SetDeadline(time.Time{})
	input := newDirectInput(ctx, stream, cancel, s.engine.deps.Clock)
	defer func() { cancel(); stream.Close(); <-input.done }()
	dialCtx, stopDial := s.engine.tunnelTimeout(ctx, s.engine.resources.limits.DialTimeout)
	// 目标始终取自出口授权配置，不接受入口自选的拨号地址。
	target, err := s.engine.dialTargetWithin(dialCtx, rule, s.engine.resources.limits.DialTimeout)
	stopDial()
	if lease != nil {
		lease.dialDone()
	}
	if err != nil {
		class := classifyTunnel(failTunnel(failureTarget, err))
		if ctx.Err() != nil || s.authorizeExistingStream(auth, request) == nil {
			class = failureConfig
		}
		s.finishStream(stream, tunnelResult{Failure: class})
		return
	}
	defer target.Close()
	stopTarget := context.AfterFunc(ctx, func() { target.Close() })
	defer stopTarget()
	stream.SetWriteDeadline(time.Now().Add(s.engine.resources.limits.TunnelHandshakeTimeout))
	// 拨号期间授权可能已变更；交付成功前再核验一次，不等待下一秒复查。
	if ctx.Err() != nil || s.authorizeExistingStream(auth, request) == nil {
		s.rejectStream(stream)
		return
	}
	if err := writeTunnelMessage(stream, tunnelResult{OK: true}); err != nil {
		return
	}
	stream.SetWriteDeadline(time.Time{})
	proxyDirect(ctx, stream, target, input, s.engine.resources.limits.IdleTimeout)
}

func (s *tunnelServer) rejectStream(stream net.Conn) {
	s.finishStream(stream, tunnelResult{Failure: failureConfig})
}

func (s *tunnelServer) finishStream(stream net.Conn, result tunnelResult) {
	stream.SetWriteDeadline(time.Now().Add(s.engine.resources.limits.TunnelHandshakeTimeout))
	if err := writeTunnelMessage(stream, result); err != nil {
		return
	}
	// FIN 先封存控制响应，随后生命周期的 RST 仍立即回收流槽。
	// 若直接 RST，新 yamux 会丢弃对端尚未读取的响应，破坏既有协议。
	if half, ok := stream.(interface{ CloseWrite() error }); ok {
		half.CloseWrite()
	}
}

func (e *Engine) yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.ConnectionWriteTimeout = e.resources.limits.TunnelHandshakeTimeout
	// 禁止自动扩窗；每流接收窗口保持协议要求的最小 256 KiB，复制缓冲
	// 则复用每方向 32 KiB 的有界传输。流数与业务预算共同约束总量。
	c.MaxStreamWindowSize = c.InitialStreamWindowSize
	c.MaxIncomingStreams = uint32(e.resources.limits.MaxTunnelStreams)
	c.AcceptBacklog = e.resources.limits.MaxTunnelStreams
	return c
}

func (e *Engine) yamuxClientConfig() *yamux.Config {
	c := e.yamuxConfig()
	// 隧道只有入口开业务流；拒绝反向 SYN，避免另占一份接收流预算。
	c.MaxIncomingStreams = 0
	return c
}

func (c *tunnelClient) openStream(ctx context.Context, session *yamux.Session, cleanups ...*tunnelAttemptCleanup) (*yamux.Stream, error) {
	if err := c.openMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer c.openMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.NumStreams() >= c.engine.resources.limits.MaxTunnelStreams {
		return nil, errTunnelCapacity
	}
	// v5.1.0 在发 SYN 时因 ctx 取消而失败，会遗留 streams/inflight 项。
	// 完成这一步短写后取得句柄，再按取消状态 Reset。发送队列也受独立
	// 总期限约束；只有载体无法在期限内发送控制帧，才关闭整个会话。
	budget := c.engine.resources.limits.TunnelHandshakeTimeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < budget {
		budget = time.Until(deadline)
	}
	finish := c.engine.watchTunnelProgress(session, budget)
	stream, err := session.OpenStream(context.WithoutCancel(ctx))
	if !finish() {
		return nil, context.DeadlineExceeded
	}
	if err != nil {
		session.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		stream := &tunnelStream{Stream: stream, engine: c.engine}
		if len(cleanups) > 0 {
			cleanups[0].close(ctx, stream)
		} else {
			stream.Close()
		}
		return nil, err
	}
	return stream, nil
}

// 库的单次 Write 超时不覆盖发送队列的排队总时长。控制帧失去进展时
// 关闭载体，解除 Open/FIN/RST 的排队并回收全部流；正常取消只重置本流。
func (e *Engine) watchTunnelProgress(session *yamux.Session, budget time.Duration) func() bool {
	ctx, cancel := e.tunnelTimeout(context.Background(), budget)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); session.Close() })
	return func() bool {
		stopped := stop()
		expired := ctx.Err() != nil
		cancel()
		if !stopped {
			<-done
		} else if expired {
			// 超时与 stop 成功可能相邻发生，不能丢弃已经创建的流句柄。
			session.Close()
		}
		return stopped && !expired
	}
}

func (s *tunnelStream) control(operation func() error) error {
	finish := s.engine.watchTunnelProgress(s.Stream.Session(), s.engine.resources.limits.TunnelHandshakeTimeout)
	err := operation()
	if !finish() {
		return context.DeadlineExceeded
	}
	return err
}

var errTunnelCapacity = errors.New("隧道会话流数已达上限")

// 网络 Deadline 仍使用真实时间，同时允许测试台推进核心时钟立即取消。
func (e *Engine) tunnelTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	timer := e.deps.Clock.NewTimer(timeout)
	go func() {
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C():
			cancel()
		}
	}()
	return ctx, cancel
}

// 会话重连和开流会串行等待网络；等待锁也必须计入调用方的总期限。
// 零值可用，关闭客户端仍可不带期限地等待正在进行的有界握手结束。
type tunnelMutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *tunnelMutex) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *tunnelMutex) Lock()   { _ = m.LockContext(context.Background()) }
func (m *tunnelMutex) Unlock() { <-m.gate }

// 单次连接的全部失败尝试共用回收登记；超时只结束调用方等待，
// Reset 仍有独立的控制帧期限，回收完成前继续占用原有资源预算。
type tunnelAttemptCleanup struct {
	wg      sync.WaitGroup
	pending atomic.Int64
}

func (c *tunnelAttemptCleanup) close(ctx context.Context, conn net.Conn) error {
	done := make(chan error, 1)
	c.wg.Add(1)
	c.pending.Add(1)
	go func() {
		defer c.wg.Done()
		err := conn.Close()
		c.pending.Add(-1)
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// 必须在本次所有尝试都已登记后调用；不会再与新的 Add 并发。
func (c *tunnelAttemptCleanup) after(finish func()) {
	if c.pending.Load() == 0 {
		finish()
		return
	}
	go func() { c.wg.Wait(); finish() }()
}
