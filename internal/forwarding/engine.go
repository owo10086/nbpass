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
	DefaultCheckPort uint32
	CheckListen      func(string, string) (net.Listener, error)
	Limits           ResourceLimits
	Clock            timing.Clock
	// NodeID 用于兼任入口和出口时去重本机业务流；由认证后的节点核心赋值。
	NodeID     string
	LookupIPv4 func(context.Context, string) ([]netip.Addr, error)
	DialTarget func(context.Context, string) (net.Conn, error)
	// DialTargetFrom 仅用于有源地址的最终目标拨号，不能回退到 DialTarget。
	DialTargetFrom func(context.Context, string, string) (net.Conn, error)
	// LocalIPv4 枚举本机接口地址，应用和缓存恢复均重新读取。
	LocalIPv4  func() ([]netip.Addr, error)
	Listen     func(string, string) (net.Listener, error)
	DialTunnel func(context.Context, string) (net.Conn, error)
	Now        func() time.Time
	Persist    func(State) error
}

type Engine struct {
	outbound            atomic.Pointer[outboundBinding]
	traffic             trafficMeter
	checkConfigured     bool
	checkListener       net.Listener
	checkPort           uint32
	checkError          string
	resources           *resourceLimiter
	businessConnections atomic.Uint64
	tunnelConnections   atomic.Uint64
	tunnelServer        *tunnelServer
	healthMu            sync.Mutex
	probeSlots          chan struct{}
	tunnelClients       map[[2]string]*tunnelClient
	deps                Dependencies
	rules               map[string]*listenerRule
	revision            uint64
	digest              string
	highest             uint64
	highestDigest       string
	results             []*controlpb.RuleApplyResult
	tunnelError         string
	exitGroups          map[string]*controlpb.ExitGroup
	grantResults        []*controlpb.TunnelGrantApplyResult
	grantResultsMu      sync.Mutex
	chainRules          map[string]*controlpb.ForwardRule
	chainFailuresMu     sync.Mutex
	chainFailures       map[string]ChainFailureObservation
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
	if deps.LocalIPv4 == nil {
		deps.LocalIPv4 = localIPv4Addresses
	}
	if deps.DialTargetFrom == nil {
		deps.DialTargetFrom = func(ctx context.Context, address, source string) (net.Conn, error) {
			return (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}}).DialContext(ctx, "tcp4", address)
		}
	}
	if deps.Listen == nil {
		deps.Listen = net.Listen
	}
	return &Engine{probeSlots: make(chan struct{}, 8), resources: &resourceLimiter{limits: deps.Limits.defaults(), rules: map[string]int{}}, deps: deps, rules: map[string]*listenerRule{}, tunnelClients: map[[2]string]*tunnelClient{}}
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
	binding := e.validateOutbound(s.OutboundIpv4)
	sourceChanged := e.outboundBinding().source != binding.source
	endpoints, groups := tunnelEndpoints(s.ExitGroups)
	desired := map[string]*controlpb.ForwardRule{}
	for _, r := range s.Rules {
		desired[r.Id] = r
	}
	checkpoint := e.State()
	checkpoint.HighestRevision, checkpoint.HighestDigest = s.Revision, s.Digest
	checkpoint.OutboundIPv4 = s.OutboundIpv4
	sourceRequired := hasTerminalGrants(s.TunnelServer)
	for _, r := range s.Rules {
		sourceRequired = sourceRequired || r.Kind == controlpb.RuleKind_RULE_KIND_DIRECT
	}
	sourceFailed := binding.err != nil && sourceRequired
	if sourceFailed {
		checkpoint.AppliedRevision, checkpoint.AppliedDigest = 0, ""
	}
	checkpoint.Rules, checkpoint.Results = nil, nil
	checkpoint.ExitGroups = s.ExitGroups
	checkpoint.TunnelError = ""
	checkpoint.CheckError = ""
	checkpoint.CheckConfigured = true
	if checkpoint.CheckPort != e.effectiveCheckPort(s.CheckPort) {
		checkpoint.CheckPort = 0
		checkpoint.CheckError = "检测端口配置应用尚未完成"
	}
	if !proto.Equal(checkpoint.TunnelServer, s.TunnelServer) {
		// 更新授权也属于撤销：最终写盘失败后不能恢复旧凭据或旧授权。
		checkpoint.TunnelServer = nil
		checkpoint.TunnelGrantResults = nil
		if s.TunnelServer != nil {
			checkpoint.TunnelError = "出口隧道配置应用尚未完成"
		}
	}
	for _, r := range s.Rules {
		result := &controlpb.RuleApplyResult{RuleId: r.Id, ApplyError: "配置应用尚未完成"}
		if current := e.rules[r.Id]; current != nil && sameListenerRule(current.config, r) && sameTunnelGroupRoutes(current.exit, groups[r.ExitGroupId]) && (r.Kind != controlpb.RuleKind_RULE_KIND_DIRECT || !sourceChanged && binding.err == nil) {
			checkpoint.Rules = append(checkpoint.Rules, r)
			result.AppliedRevision, result.ApplyError = s.Revision, ""
		}
		checkpoint.Results = append(checkpoint.Results, result)
	}
	if err := e.persist(checkpoint); err != nil {
		return err
	}
	e.highest, e.highestDigest = s.Revision, s.Digest
	e.setChainRules(s)
	e.outbound.Store(binding)
	if sourceFailed {
		e.revision, e.digest = 0, ""
	}
	for id, current := range e.rules {
		next := desired[id]
		if !sameListenerRule(current.config, next) || current.config.Kind == controlpb.RuleKind_RULE_KIND_DIRECT && (sourceChanged || binding.err != nil) {
			current.stop()
			delete(e.rules, id)
		}
	}
	var failures []error
	if err := e.ListenCheckPort(e.effectiveCheckPort(s.CheckPort)); err != nil {
		failures = append(failures, err)
	}
	e.tunnelError = ""
	if err := e.applyTunnelServer(s.TunnelServer); err != nil {
		e.tunnelError = ErrorSummary(fmt.Sprintf("出口隧道监听失败: %v", err), 1024)
		failures = append(failures, errors.New(e.tunnelError))
	}
	if binding.err != nil && hasTerminalGrants(s.TunnelServer) {
		e.tunnelError = ErrorSummary(binding.err.Error(), 1024)
		failures = append(failures, binding.err)
	}
	e.applyTunnelClients(endpoints)
	e.setExitGroups(groups)
	grantResults := e.buildGrantResults(s.Revision, s.TunnelServer)
	readiness := e.checkNewTunnelRules(ctx, s.Rules, groups)
	e.results = nil
	for _, r := range s.Rules {
		result := &controlpb.RuleApplyResult{RuleId: r.Id}
		err := readiness[r.Id]
		if r.Kind == controlpb.RuleKind_RULE_KIND_DIRECT && binding.err != nil {
			err = binding.err
		}
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
	state.TunnelGrantResults = grantResults
	if len(failures) == 0 {
		state.AppliedRevision, state.AppliedDigest = s.Revision, s.Digest
	}
	if err := e.persist(state); err != nil {
		for _, r := range e.results {
			if r.ApplyError == "" {
				r.ApplyError = ErrorSummary(err.Error(), 1024)
			}
		}
		e.setGrantResults(failedGrantResults(grantResults, err))
		return errors.Join(append(failures, err)...)
	}
	e.setGrantResults(grantResults)
	e.revision, e.digest = state.AppliedRevision, state.AppliedDigest
	return errors.Join(failures...)
}

func tunnelEndpoints(exitGroups []*controlpb.ExitGroup) (map[[2]string]*controlpb.TunnelEndpoint, map[string]*controlpb.ExitGroup) {
	endpoints := map[[2]string]*controlpb.TunnelEndpoint{}
	groups := map[string]*controlpb.ExitGroup{}
	for _, g := range exitGroups {
		groups[g.Id] = g
		for _, ep := range g.Exits {
			endpoints[endpointKey(ep)] = ep
		}
	}
	return endpoints, groups
}

func (e *Engine) applyTunnelClients(endpoints map[[2]string]*controlpb.TunnelEndpoint) {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	for id, client := range e.tunnelClients {
		same := sameTunnelRoute(client.currentEndpoint(), endpoints[id])
		if !same {
			client.close()
			delete(e.tunnelClients, id)
		}
	}
	for id, ep := range endpoints {
		if e.tunnelClients[id] != nil {
			e.tunnelClients[id].updateEndpoint(ep)
		}
		if e.tunnelClients[id] == nil {
			client := &tunnelClient{endpoint: proto.Clone(ep).(*controlpb.TunnelEndpoint), engine: e}
			e.tunnelClients[id] = client
			client.startProbes()
		}
	}
}

func sameListenerRule(a, b *controlpb.ForwardRule) bool {
	if a == nil || b == nil {
		return a == b
	}
	a, b = proto.Clone(a).(*controlpb.ForwardRule), proto.Clone(b).(*controlpb.ForwardRule)
	a.AuthorizedExitIds, b.AuthorizedExitIds = nil, nil
	return proto.Equal(a, b)
}
func (e *Engine) selectTunnel(r *controlpb.ForwardRule, ep *controlpb.ExitGroup) *tunnelSelection {
	selection := &tunnelSelection{rule: proto.Clone(r).(*controlpb.ForwardRule)}
	for _, endpoint := range ep.GetExits() {
		allowed := r.ChainId == "" && len(r.AuthorizedExitIds) == 0 && len(ep.Exits) == 1
		for _, id := range r.AuthorizedExitIds {
			allowed = allowed || id == endpoint.NodeId
		}
		if allowed {
			if client := e.tunnelClients[endpointKey(endpoint)]; client != nil {
				selection.clients = append(selection.clients, client)
			}
		}
	}
	return selection
}

func (e *Engine) startRule(r *controlpb.ForwardRule, ep *controlpb.ExitGroup) error {
	selection := e.selectTunnel(r, ep)
	if current := e.rules[r.Id]; current != nil {
		current.exit = ep
		current.selection.Store(selection)
		return nil
	}
	listener, err := e.deps.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(r.ListenPort))))
	if err != nil {
		return fmt.Errorf("规则 %s 端口 %d 监听失败: %w", r.Id, r.ListenPort, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	current := &listenerRule{exit: ep, config: proto.Clone(r).(*controlpb.ForwardRule), listener: listener, ctx: ctx, cancel: cancel, connections: map[net.Conn]struct{}{}, engine: e}
	current.selection.Store(selection)
	e.rules[r.Id] = current
	e.registerTrafficRule(r.Id)
	current.wg.Add(1)
	go current.accept()
	return nil
}

func (e *Engine) Close() {
	if e.checkListener != nil {
		e.checkListener.Close()
		e.checkListener = nil
	}
	e.healthMu.Lock()
	for _, client := range e.tunnelClients {
		client.close()
	}
	for _, client := range e.tunnelClients {
		if client.probeDone != nil {
			<-client.probeDone
		}
	}
	e.tunnelClients = map[[2]string]*tunnelClient{}
	e.healthMu.Unlock()
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
	exit        *controlpb.ExitGroup
	selection   atomic.Pointer[tunnelSelection]
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
		r.acceptTunnel(c)
	}
}
func (r *listenerRule) serve(client net.Conn) {
	defer r.engine.meterConnection(r.config.Id)()
	var target net.Conn
	var err error
	if r.config.Kind == controlpb.RuleKind_RULE_KIND_TUNNEL {
		target, err = r.selection.Load().dial(r.ctx)
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
	return e.dialTargetWithin(ctx, r, 10*time.Second, e.outboundBinding().source)
}

func (e *Engine) dialTargetWithin(ctx context.Context, r *controlpb.ForwardRule, budget time.Duration, source string) (net.Conn, error) {
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
		address := net.JoinHostPort(ip.String(), strconv.Itoa(int(r.TargetPort)))
		var c net.Conn
		var err error
		if source != "" {
			c, err = e.deps.DialTargetFrom(attempt, address, source)
		} else {
			c, err = e.deps.DialTarget(attempt, address)
		}
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
		if config == nil || old.ListenPort != config.ListenPort || old.ServerName != config.ServerName || old.RotationId == "" && config.RotationId == "" && (!bytes.Equal(old.CertificatePem, config.CertificatePem) || !bytes.Equal(old.PrivateKeyPem, config.PrivateKeyPem)) {
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
