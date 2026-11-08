package forwarding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

type Dependencies struct {
	LookupIPv4 func(context.Context, string) ([]netip.Addr, error)
	DialTarget func(context.Context, string) (net.Conn, error)
	Listen     func(string, string) (net.Listener, error)
}

type Engine struct {
	businessConnections atomic.Uint64
	deps                Dependencies
	rules               map[string]*listenerRule
	revision            uint64
	digest              string
	highest             uint64
	highestDigest       string
}

func NewEngine(deps Dependencies) *Engine {
	if deps.LookupIPv4 == nil {
		deps.LookupIPv4 = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		}
	}
	if deps.DialTarget == nil {
		deps.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	}
	if deps.Listen == nil {
		deps.Listen = net.Listen
	}
	return &Engine{deps: deps, rules: map[string]*listenerRule{}}
}

// Connections 返回逻辑业务连接数；目标侧 TCP 和控制连接不重复计数。
func (e *Engine) Connections() (business, tunnel uint64) { return e.businessConnections.Load(), 0 }

func (e *Engine) Applied() (uint64, string) { return e.revision, e.digest }

// Apply 保留未变化的监听，先撤销缺失/变化的规则，再启动新规则。
// 调用方串行使用引擎；逐规则缓存与部分应用结果由票 04 在此边界扩展。
func (e *Engine) Apply(s *controlpb.ConfigSnapshot) error {
	if err := ValidateSnapshot(s); err != nil {
		return err
	}
	if s.Revision < e.highest || s.Revision == e.highest && s.Digest != e.highestDigest {
		return errors.New("拒绝旧版本或同版本不同内容的快照")
	}
	e.highest, e.highestDigest = s.Revision, s.Digest
	desired := map[string]*controlpb.ForwardRule{}
	for _, r := range s.Rules {
		desired[r.Id] = r
	}
	for id, current := range e.rules {
		if !proto.Equal(current.config, desired[id]) {
			current.stop()
			delete(e.rules, id)
		}
	}
	var failures []error
	for _, r := range s.Rules {
		if e.rules[r.Id] != nil {
			continue
		}
		listener, err := e.deps.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(r.ListenPort))))
		if err != nil {
			failures = append(failures, fmt.Errorf("规则 %s 监听失败: %w", r.Id, err))
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		current := &listenerRule{config: proto.Clone(r).(*controlpb.ForwardRule), listener: listener, ctx: ctx, cancel: cancel, connections: map[net.Conn]struct{}{}, engine: e}
		e.rules[r.Id] = current
		current.wg.Add(1)
		go current.accept()
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	e.revision, e.digest = s.Revision, s.Digest
	return nil
}
func (e *Engine) Close() {
	for _, r := range e.rules {
		r.stop()
	}
	e.rules = map[string]*listenerRule{}
}

type listenerRule struct {
	config      *controlpb.ForwardRule
	listener    net.Listener
	ctx         context.Context
	cancel      context.CancelFunc
	engine      *Engine
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	wg          sync.WaitGroup
}

func (r *listenerRule) track(c net.Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		c.Close()
		return false
	}
	r.connections[c] = struct{}{}
	return true
}
func (r *listenerRule) untrack(c net.Conn) {
	c.Close()
	r.mu.Lock()
	delete(r.connections, c)
	r.mu.Unlock()
}
func (r *listenerRule) stop() {
	r.cancel()
	r.listener.Close()
	r.mu.Lock()
	for c := range r.connections {
		c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}
func (r *listenerRule) accept() {
	defer r.wg.Done()
	for {
		c, err := r.listener.Accept()
		if err != nil {
			return
		}
		if !r.track(c) {
			return
		}
		r.engine.businessConnections.Add(1)
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer r.engine.businessConnections.Add(^uint64(0))
			defer r.untrack(c)
			r.serve(c)
		}()
	}
}
func (r *listenerRule) serve(client net.Conn) {
	target, err := r.engine.dialTarget(r.ctx, r.config)
	if err != nil {
		return
	}
	if !r.track(target) {
		return
	}
	defer r.untrack(target)
	done := make(chan struct{}, 1)
	copyHalf := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		if err != nil {
			client.Close()
			target.Close()
		}
	}
	go func() { copyHalf(target, client); done <- struct{}{} }()
	copyHalf(client, target)
	<-done
}

// 每个新连接最多尝试三个不同 IPv4，总预算 10 秒；只在交付业务数据前重试。
func (e *Engine) dialTarget(ctx context.Context, r *controlpb.ForwardRule) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(r.TargetAddress); err == nil {
		ips = []netip.Addr{ip}
	} else {
		var err error
		ips, err = e.deps.LookupIPv4(ctx, r.TargetAddress)
		if err != nil {
			return nil, err
		}
	}
	seen := map[netip.Addr]bool{}
	attempts := 0
	var last error
	for _, ip := range ips {
		if seen[ip] || !AllowedIP(ip, r.AllowPrivate) {
			continue
		}
		seen[ip] = true
		if attempts == 3 || ctx.Err() != nil {
			break
		}
		attempts++
		// 单候选最多 3 秒，为后续 A 记录保留总预算。
		attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
		c, err := e.deps.DialTarget(attempt, net.JoinHostPort(ip.String(), strconv.Itoa(int(r.TargetPort))))
		cancel()
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("没有被规则允许的 IPv4 目标")
	}
	return nil, last
}
