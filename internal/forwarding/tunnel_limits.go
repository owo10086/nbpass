package forwarding

import (
	"context"
	"errors"
	"io"
	"net"
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
		defer lease.release()
		defer finishTraffic()
		defer r.engine.businessConnections.Add(^uint64(0))
		defer r.untrack(client)
		r.serveLimitedTunnel(client, lease)
	}()
}

func (r *listenerRule) serveLimitedTunnel(client net.Conn, lease *resourceLease) {
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	input := newDirectInput(ctx, client, cancel, r.engine.deps.Clock)
	defer func() { cancel(); client.Close(); <-input.done }()
	if r.tunnel == nil {
		return
	}
	target, err := r.tunnel.dial(ctx, r.config)
	lease.dialDone()
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
	rule := s.authorizeStream(auth, request)
	if rule == nil || lease != nil && !lease.assignRule(rule.Id) {
		s.rejectStream(stream)
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
		s.rejectStream(stream)
		return
	}
	defer target.Close()
	stopTarget := context.AfterFunc(ctx, func() { target.Close() })
	defer stopTarget()
	stream.SetWriteDeadline(time.Now().Add(s.engine.resources.limits.TunnelHandshakeTimeout))
	// 拨号期间授权可能已变更；交付成功前再核验一次，不等待下一秒复查。
	if ctx.Err() != nil || s.authorizeStream(auth, request) == nil {
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
	stream.SetWriteDeadline(time.Now().Add(s.engine.resources.limits.TunnelHandshakeTimeout))
	if err := writeTunnelMessage(stream, tunnelResult{}); err != nil {
		return
	}
	// FIN 先封存拒绝响应，随后生命周期的 RST 仍立即回收流槽。
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

func (c *tunnelClient) openStream(ctx context.Context, session *yamux.Session) (*yamux.Stream, error) {
	c.openMu.Lock()
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
		(&tunnelStream{Stream: stream, engine: c.engine}).Close()
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
