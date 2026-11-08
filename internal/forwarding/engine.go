package forwarding

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/timing"
)

type Dependencies struct {
	Limits ResourceLimits
	Clock  timing.Clock
	// NodeID 用于兼任入口和出口时去重本机业务流；由认证后的节点核心赋值。
	NodeID     string
	LookupIPv4 func(context.Context, string) ([]netip.Addr, error)
	DialTarget func(context.Context, string) (net.Conn, error)
	Listen     func(string, string) (net.Listener, error)
	DialTunnel func(context.Context, string) (net.Conn, error)
	Now        func() time.Time
	Persist    func(State) error
}

type Engine struct {
	traffic             trafficMeter
	resources           *resourceLimiter
	businessConnections atomic.Uint64
	tunnelConnections   atomic.Uint64
	tunnelServer        *tunnelServer
	tunnelClients       map[string]*tunnelClient
	deps                Dependencies
	rules               map[string]*listenerRule
	revision            uint64
	digest              string
	highest             uint64
	highestDigest       string
	results             []*controlpb.RuleApplyResult
	tunnelError         string
}

func NewEngine(deps Dependencies) *Engine {
	if deps.Clock == nil {
		deps.Clock = resourceClock{}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.DialTunnel == nil {
		deps.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	}
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
	return &Engine{resources: &resourceLimiter{limits: deps.Limits.defaults(), rules: map[string]int{}}, deps: deps, rules: map[string]*listenerRule{}, tunnelClients: map[string]*tunnelClient{}}
}

// Connections 返回逻辑业务连接数；目标侧 TCP 和控制连接不重复计数。
func (e *Engine) Connections() (business, tunnel uint64) {
	return e.businessConnections.Load(), e.tunnelConnections.Load()
}

func (e *Engine) Applied() (uint64, string) { return e.revision, e.digest }

// Apply 保留未变化的监听，先撤销缺失/变化的规则，再启动新规则。
// 调用方串行使用引擎；任何运行时撤销之前必须先提交不含旧规则的缓存。
func (e *Engine) Apply(s *controlpb.ConfigSnapshot) error {
	return e.ApplyContext(context.Background(), s)
}

// ApplyContext 使授权探测随控制会话取消，整份快照共享探测预算。
func (e *Engine) ApplyContext(ctx context.Context, s *controlpb.ConfigSnapshot) error {
	if err := ValidateSnapshot(s); err != nil {
		return err
	}
	if s.Revision < e.highest || s.Revision == e.highest && s.Digest != e.highestDigest {
		return errors.New("拒绝旧版本或同版本不同内容的快照")
	}
	endpoints, groups := tunnelEndpoints(s.ExitGroups)
	desired := map[string]*controlpb.ForwardRule{}
	for _, r := range s.Rules {
		desired[r.Id] = r
	}
	checkpoint := e.State()
	checkpoint.HighestRevision, checkpoint.HighestDigest = s.Revision, s.Digest
	checkpoint.Rules, checkpoint.Results = nil, nil
	checkpoint.ExitGroups = s.ExitGroups
	checkpoint.TunnelError = ""
	if !proto.Equal(checkpoint.TunnelServer, s.TunnelServer) {
		// 更新授权也属于撤销：最终写盘失败后不能恢复旧凭据或旧授权。
		checkpoint.TunnelServer = nil
		if s.TunnelServer != nil {
			checkpoint.TunnelError = "出口隧道配置应用尚未完成"
		}
	}
	for _, r := range s.Rules {
		result := &controlpb.RuleApplyResult{RuleId: r.Id, ApplyError: "配置应用尚未完成"}
		if current := e.rules[r.Id]; current != nil && proto.Equal(current.config, r) && proto.Equal(current.exit, groups[r.ExitGroupId]) {
			checkpoint.Rules = append(checkpoint.Rules, r)
			result.AppliedRevision, result.ApplyError = s.Revision, ""
		}
		checkpoint.Results = append(checkpoint.Results, result)
	}
	if err := e.persist(checkpoint); err != nil {
		return err
	}
	e.highest, e.highestDigest = s.Revision, s.Digest
	for id, current := range e.rules {
		next := desired[id]
		var ep *controlpb.TunnelEndpoint
		if next != nil {
			ep = groups[next.ExitGroupId]
		}
		if !proto.Equal(current.config, next) || !proto.Equal(current.exit, ep) {
			current.stop()
			delete(e.rules, id)
		}
	}
	var failures []error
	e.tunnelError = ""
	if err := e.applyTunnelServer(s.TunnelServer); err != nil {
		e.tunnelError = ErrorSummary(fmt.Sprintf("出口隧道监听失败: %v", err), 1024)
		failures = append(failures, errors.New(e.tunnelError))
	}
	e.applyTunnelClients(endpoints)
	readiness := e.checkNewTunnelRules(ctx, s.Rules, groups)
	e.results = nil
	for _, r := range s.Rules {
		result := &controlpb.RuleApplyResult{RuleId: r.Id}
		err := readiness[r.Id]
		if err == nil {
			err = e.startRule(r, groups[r.ExitGroupId])
		}
		if err != nil {
			result.ApplyError = ErrorSummary(err.Error(), 1024)
			failures = append(failures, errors.New(result.ApplyError))
		} else {
			result.AppliedRevision = s.Revision
		}
		e.results = append(e.results, result)
	}
	state := e.State()
	if len(failures) == 0 {
		state.AppliedRevision, state.AppliedDigest = s.Revision, s.Digest
	}
	if err := e.persist(state); err != nil {
		for _, r := range e.results {
			if r.ApplyError == "" {
				r.ApplyError = ErrorSummary(err.Error(), 1024)
			}
		}
		return errors.Join(append(failures, err)...)
	}
	e.revision, e.digest = state.AppliedRevision, state.AppliedDigest
	return errors.Join(failures...)
}

func tunnelEndpoints(exitGroups []*controlpb.ExitGroup) (map[string]*controlpb.TunnelEndpoint, map[string]*controlpb.TunnelEndpoint) {
	endpoints := map[string]*controlpb.TunnelEndpoint{}
	groups := map[string]*controlpb.TunnelEndpoint{}
	for _, g := range exitGroups {
		for _, ep := range g.Exits {
			endpoints[ep.NodeId] = ep
			groups[g.Id] = ep
		}
	}
	return endpoints, groups
}

func (e *Engine) applyTunnelClients(endpoints map[string]*controlpb.TunnelEndpoint) {
	for id, client := range e.tunnelClients {
		if !proto.Equal(client.endpoint, endpoints[id]) {
			client.close()
			delete(e.tunnelClients, id)
		}
	}
	for id, ep := range endpoints {
		if e.tunnelClients[id] == nil {
			e.tunnelClients[id] = &tunnelClient{endpoint: proto.Clone(ep).(*controlpb.TunnelEndpoint), engine: e}
		}
	}
}

func (e *Engine) startRule(r *controlpb.ForwardRule, ep *controlpb.TunnelEndpoint) error {
	if e.rules[r.Id] != nil {
		return nil
	}
	listener, err := e.deps.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(r.ListenPort))))
	if err != nil {
		return fmt.Errorf("规则 %s 端口 %d 监听失败: %w", r.Id, r.ListenPort, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var client *tunnelClient
	if ep != nil {
		client = e.tunnelClients[ep.NodeId]
	}
	current := &listenerRule{exit: ep, tunnel: client, config: proto.Clone(r).(*controlpb.ForwardRule), listener: listener, ctx: ctx, cancel: cancel, connections: map[net.Conn]struct{}{}, engine: e}
	e.rules[r.Id] = current
	e.registerTrafficRule(r.Id)
	current.wg.Add(1)
	go current.accept()
	return nil
}

func (e *Engine) Close() {
	for _, client := range e.tunnelClients {
		client.close()
	}
	if e.tunnelServer != nil {
		e.tunnelServer.close()
	}
	for _, r := range e.rules {
		r.stop()
	}
	e.rules = map[string]*listenerRule{}
}

type listenerRule struct {
	config      *controlpb.ForwardRule
	exit        *controlpb.TunnelEndpoint
	tunnel      *tunnelClient
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
	r.engine.unregisterTrafficRule(r.config.Id)
}
func (r *listenerRule) accept() {
	defer r.wg.Done()
	for {
		c, err := r.listener.Accept()
		if err != nil {
			return
		}
		if r.config.Kind == controlpb.RuleKind_RULE_KIND_DIRECT {
			r.acceptDirect(c)
			continue
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
	defer r.engine.meterConnection(r.config.Id)()
	var target net.Conn
	var err error
	if r.config.Kind == controlpb.RuleKind_RULE_KIND_TUNNEL {
		if r.tunnel == nil {
			return
		}
		target, err = r.tunnel.dial(r.ctx, r.config)
	} else {
		target, err = r.engine.dialTarget(r.ctx, r.config)
	}
	if err != nil {
		return
	}
	if !r.track(target) {
		return
	}
	defer r.untrack(target)
	r.engine.proxyMetered(r.config.Id, client, target)
}

// 每个新连接最多尝试三个不同 IPv4，总预算 10 秒；只在交付业务数据前重试。
func (e *Engine) dialTarget(ctx context.Context, r *controlpb.ForwardRule) (net.Conn, error) {
	return e.dialTargetWithin(ctx, r, 10*time.Second)
}

func (e *Engine) dialTargetWithin(ctx context.Context, r *controlpb.ForwardRule, budget time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
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

func (e *Engine) applyTunnelServer(config *controlpb.TunnelServer) error {
	if e.tunnelServer != nil {
		old := e.tunnelServer.config
		if config == nil || old.ListenPort != config.ListenPort || !bytes.Equal(old.CertificatePem, config.CertificatePem) || !bytes.Equal(old.PrivateKeyPem, config.PrivateKeyPem) {
			e.tunnelServer.close()
			e.tunnelServer = nil
		} else {
			e.tunnelServer.update(config)
			return nil
		}
	}
	if config == nil {
		return nil
	}
	server, err := e.startTunnelServer(config)
	if err != nil {
		return err
	}
	e.tunnelServer = server
	return nil
}
