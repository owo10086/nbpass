// Package node 是独立节点核心：凭票据接入，经验证的 TLS 控制流与断线重连。
package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"nbpass/internal/checker"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/dnswatch"
	"nbpass/internal/forwarding"
	"nbpass/internal/nodeupdate"
	"nbpass/internal/probe"
	"nbpass/internal/publicip"
	"nbpass/internal/rotation"
	"nbpass/internal/timing"
)

const ProtocolVersion = compatibility.ControlCurrent
const SoftwareVersion = "0.1.0"

type Credentials struct {
	ControlURL string `json:"control_url"`
	NodeID     string `json:"node_id"`
	Token      string `json:"token"`
}

type Store interface {
	Load(context.Context) (Credentials, error)
	Save(context.Context, Credentials) error
}

type Config struct {
	ControlURL         string
	InstallationTicket string
	EnrollOnly         bool
}

type Dependencies struct {
	localReady      *atomic.Bool
	Updater         nodeupdate.Service
	ArtifactSHA256  string
	ArtifactSize    int64
	SoftwareVersion string
	upgrades        *upgradeRunner
	LocalHealth     func(string, string, bool)

	CheckerLookup       func(context.Context, string) ([]string, error)
	CheckerDial         func(context.Context, string) (net.Conn, error)
	exitHealth          func() []*controlpb.ExitHealth
	SelfCheckLookup     func(context.Context, string) ([]string, error)
	SelfCheckDial       func(context.Context, string) (net.Conn, error)
	SelfCheckStore      SelfCheckStore
	selfChecks          *selfCheckRunner
	DNSWatchLookup      func(context.Context, string, dnswatch.Resolver) ([]string, error)
	RotationStore       RotationStore
	RotationIPv4Sources [2]func(context.Context) (string, error)
	RotationLocalIPv4   func(context.Context) ([]string, error)
	RotationHTTP        func(context.Context, rotation.Config, rotation.Variables) error
	rotations           *rotationRunner
	Probe               probe.Collector
	Connections         probe.Connections
	Clock               timing.Clock
	Store               Store
	Random              io.Reader
	RootCAs             *x509.CertPool
	DialContext         func(context.Context, string) (net.Conn, error)
	PublicIPv4          func(context.Context) (string, error)
	Traffic             *TrafficQueue
	trafficUpdates      <-chan struct{}
	Events              *EventQueue
	Forwarding          forwarding.Dependencies
	ConfigStore         ConfigStore
}

func Run(ctx context.Context, config Config, deps Dependencies) (runErr error) {
	if err := deps.Forwarding.Limits.Validate(); err != nil {
		return err
	}
	u, err := url.Parse(config.ControlURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("控制地址必须是不带路径的 HTTPS 地址")
	}
	if deps.Clock == nil || deps.Store == nil || deps.Random == nil {
		return errors.New("节点外部依赖不完整")
	}
	if deps.DNSWatchLookup == nil {
		deps.DNSWatchLookup = (dnswatch.Client{}).Lookup
	}
	if deps.PublicIPv4 == nil {
		deps.PublicIPv4 = defaultPublicIPv4.Query
	}
	if deps.RotationStore == nil {
		deps.RotationStore, _ = deps.Store.(RotationStore)
	}
	if deps.RotationIPv4Sources[0] == nil {
		deps.RotationIPv4Sources[0] = deps.PublicIPv4
	}
	if deps.RotationIPv4Sources[1] == nil {
		secondary := defaultPublicIPv4
		secondary.URL = "https://ifconfig.me/ip"
		deps.RotationIPv4Sources[1] = secondary.Query
	}
	if deps.RotationLocalIPv4 == nil {
		deps.RotationLocalIPv4 = localPublicIPv4
	}
	if deps.RotationHTTP == nil {
		deps.RotationHTTP = (rotation.HTTPExecutor{}).Do
	}
	if deps.SelfCheckStore == nil {
		deps.SelfCheckStore, _ = deps.Store.(SelfCheckStore)
	}
	started := deps.Clock.Now()
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "443")
	}
	dial := deps.DialContext
	if dial == nil {
		dial = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", address)
		}
	}
	client, err := grpc.NewClient("passthrough:///"+address,
		grpc.WithContextDialer(dial),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: deps.RootCAs, ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(64<<10), grpc.MaxCallSendMsgSize(64<<10)))
	if err != nil {
		return errors.New("无法创建控制客户端")
	}
	defer client.Close()
	service := controlpb.NewControlServiceClient(client)
	credential, err := deps.Store.Load(ctx)
	previous, hasPrevious := credential, err == nil
	if err == nil && config.EnrollOnly && config.InstallationTicket != "" {
		err = os.ErrNotExist
	}
	if errors.Is(err, os.ErrNotExist) {
		if len(config.InstallationTicket) != 43 {
			return errors.New("首次接入需要安装票据")
		}
		callCtx, cancel := timeout(ctx, deps.Clock, 15*time.Second)
		response, enrollErr := service.Enroll(callCtx, &controlpb.EnrollRequest{InstallationTicket: config.InstallationTicket})
		cancel()
		if enrollErr != nil {
			return errors.New("安装票据兑换失败，请检查票据有效性与控制地址证书")
		}
		credential = Credentials{ControlURL: config.ControlURL, NodeID: response.NodeId, Token: response.Token}
		// 先清旧身份状态再落盘凭据：中途失败时凭据仍是旧身份，重置 token 重跑会再次清理。
		if err := clearForeignState(ctx, deps, hasPrevious, previous, credential); err != nil {
			return errors.New("无法清理旧节点身份的本地缓存，请在面板重置 token 后重新接入")
		}
		if err := deps.Store.Save(ctx, credential); err != nil {
			return errors.New("节点凭据持久化失败，请在面板重置 token 后重新接入")
		}
	} else if err != nil {
		return errors.New("无法读取节点凭据")
	}
	if credential.ControlURL != config.ControlURL || len(credential.Token) != 43 || credential.NodeID == "" {
		return errors.New("缓存凭据与控制地址不匹配，请在面板重置 token 后重新接入")
	}
	if config.EnrollOnly {
		return nil
	}
	if deps.Events == nil {
		if store, ok := deps.Store.(EventStore); ok {
			deps.Events, err = OpenEventQueue(ctx, store, deps.Clock, deps.Random, EventLimits{})
			if err != nil {
				return errors.New("无法打开节点事件队列")
			}
		}
	}
	if deps.Events != nil {
		if err := deps.Events.Record(ctx, "node_started", "节点进程启动", 0); err != nil {
			return errors.New("节点启动事件持久化失败")
		}
	}
	instance := make([]byte, 32)
	if _, err := io.ReadFull(deps.Random, instance); err != nil {
		return errors.New("无法生成节点进程实例")
	}
	if deps.Forwarding.Now == nil {
		deps.Forwarding.Now = deps.Clock.Now
	}
	deps.Forwarding.NodeID = credential.NodeID
	if deps.Forwarding.Clock == nil {
		deps.Forwarding.Clock = deps.Clock
	}
	if deps.ConfigStore == nil {
		deps.ConfigStore, _ = deps.Store.(ConfigStore)
	}
	if deps.ConfigStore != nil {
		deps.Forwarding.Persist = func(state forwarding.State) error {
			data, err := json.Marshal(configCache{Version: int(compatibility.StateCurrent), NodeID: credential.NodeID, ControlURL: credential.ControlURL, State: state})
			if err != nil {
				return err
			}
			return deps.ConfigStore.SaveConfig(ctx, data)
		}
	}
	if deps.Forwarding.Clock == nil {
		deps.Forwarding.Clock = deps.Clock
	}
	deps.Forwarding.DefaultCheckPort = 18080
	engine := forwarding.NewEngine(deps.Forwarding)
	deps.exitHealth = engine.ExitHealth
	if deps.SoftwareVersion == "" {
		deps.SoftwareVersion = SoftwareVersion
	}
	if deps.ArtifactSHA256 == "" && deps.Updater != nil {
		deps.ArtifactSHA256, deps.ArtifactSize, _ = nodeupdate.ExecutableDigest()
	}
	report := &controlpb.NodeReport{UpgradeCapable: deps.Updater != nil, TunnelRotationVersion: 1, ExitFailoverVersion: 1, RotationRecheckCapable: true, DnsWatchCapable: true, TunnelAuthorizationVersion: 1, NodeId: credential.NodeID, ProcessInstance: base64.RawURLEncoding.EncodeToString(instance), ProtocolVersion: ProtocolVersion, SoftwareVersion: deps.SoftwareVersion}
	report.Compatibility = compatibility.Current()
	defer func() {
		engine.Close()
		if deps.Traffic != nil {
			if saveErr := deps.Traffic.Capture(context.WithoutCancel(ctx), report.ProcessInstance, engine.Traffic()); saveErr != nil && runErr == nil {
				// 转发完全停止后落盘，避免遗漏最后一轮成功写入的字节。
				runErr = errors.New("节点最后一轮流量持久化失败")
			}
		}
	}()
	if err := openConfigCache(ctx, credential, &deps, engine); err != nil {
		return err
	}
	if !engine.State().CheckConfigured {
		_ = engine.ListenCheckPort(18080)
	}
	if deps.Connections == nil {
		deps.Connections = engine
	}
	rotationCtx, stopRotations := context.WithCancel(ctx)
	deps.rotations, err = openRotations(rotationCtx, deps)
	if err != nil {
		stopRotations()
		return errors.New("无法打开换 IP 动作缓存")
	}
	defer func() {
		stopRotations()
		if deps.rotations != nil {
			deps.rotations.wg.Wait()
		}
	}()
	selfCtx, stopSelfChecks := context.WithCancel(ctx)
	deps.selfChecks, err = openSelfChecks(selfCtx, deps)
	if err != nil {
		stopSelfChecks()
		return errors.New("无法打开自测缓存")
	}
	defer func() {
		stopSelfChecks()
		if deps.selfChecks != nil {
			deps.selfChecks.wg.Wait()
		}
	}()
	upgradeCtx, stopUpgrades := context.WithCancel(ctx)
	deps.upgrades = startUpgrades(upgradeCtx, deps)
	defer func() {
		stopUpgrades()
		if deps.upgrades != nil {
			<-deps.upgrades.done
		}
	}()
	deps.localReady = &atomic.Bool{}
	if deps.LocalHealth != nil {
		deps.localReady.Store(engine.UpgradeReady(ctx) == nil)
	}
	if deps.LocalHealth != nil {
		healthCtx, stopHealth := context.WithCancel(ctx)
		healthDone := make(chan struct{})
		defer func() { stopHealth(); <-healthDone }()
		go func() {
			defer close(healthDone)
			for {
				deps.LocalHealth(report.ProcessInstance, deps.SoftwareVersion, deps.localReady.Load())
				timer := deps.Clock.NewTimer(time.Second)
				select {
				case <-healthCtx.Done():
					timer.Stop()
					return
				case <-timer.C():
				}
			}
		}()
	}
	state := engine.State()
	report.AttemptedRevision, report.AttemptedDigest, report.RuleResults = state.HighestRevision, state.HighestDigest, state.Results
	report.AppliedRevision, report.AppliedDigest = engine.Applied()
	report.ApplyError = engine.ApplyError()
	report.RevocationsApplied = state.HighestRevision > 0
	if deps.Traffic == nil {
		store, _ := deps.Store.(TrafficStore)
		deps.Traffic, err = OpenTrafficQueue(ctx, store, deps.Clock)
		if err != nil {
			return errors.New("无法打开节点流量队列")
		}
	}
	trafficCtx, stopTraffic := context.WithCancel(ctx)
	trafficUpdates, trafficDone := make(chan struct{}, 1), make(chan struct{})
	deps.trafficUpdates = trafficUpdates
	go sampleTraffic(trafficCtx, deps.Traffic, report.ProcessInstance, engine, trafficUpdates, trafficDone)
	defer func() {
		stopTraffic()
		<-trafficDone
	}()
	probeCtx, stopProbe := context.WithCancel(ctx)
	defer stopProbe()
	updates := make(chan probeUpdate, 1)
	go sampleProbe(probeCtx, deps, started, updates)
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Local readiness is independent of control connectivity, including failed Connect calls.
		if deps.LocalHealth != nil && !deps.localReady.Load() {
			deps.localReady.Store(engine.UpgradeReady(ctx) == nil)
		}
		// 由注入时钟控制重试，不额外等待 gRPC 内部的墙上时钟退避。
		client.ResetConnectBackoff()
		err := exchange(ctx, service, credential.Token, report, deps, updates, engine)
		if status.Code(err) == codes.Unauthenticated || status.Code(err) == codes.FailedPrecondition {
			return errors.New("节点凭据或进程实例已失效，请检查面板状态")
		}
		timer := deps.Clock.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C():
		}
	}
}

func exchange(ctx context.Context, service controlpb.ControlServiceClient, token string, report *controlpb.NodeReport, deps Dependencies, updates <-chan probeUpdate, engine *forwarding.Engine) error {
	clock := deps.Clock
	streamCtx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token))
	defer cancel()
	onlineCtx, markOffline := context.WithCancel(streamCtx)
	defer markOffline()
	var dnsJobs chan<- *controlpb.DNSWatchTask
	var dnsResults <-chan *controlpb.DNSWatchResult
	var stopDNS func()
	defer func() {
		if stopDNS != nil {
			stopDNS()
		}
	}()
	dnsSeen := map[string]bool{}
	var dnsResult *controlpb.DNSWatchResult
	checks := newCheckerExchange(onlineCtx, deps, report.NodeId)
	stream, err := service.Connect(streamCtx)
	if err != nil {
		return err
	}
	type incoming struct {
		message *controlpb.PanelMessage
		err     error
	}
	messages := make(chan incoming)
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				markOffline()
			} // 断流证据即时撤销许可，不等待主循环消费错误。
			select {
			case messages <- incoming{m, err}:
			case <-streamCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	// 单独追踪事件在途批次；无 ACK 的主动快照或探针回包不能消费它。
	pending := map[string]bool{}
	drainEvents := true
	var response, eventAck, selfAck timing.Timer
	var checkerAck timing.Timer
	var checkerAckC <-chan time.Time
	checkerPending := map[string]bool{}
	var selfAckC <-chan time.Time
	selfPending := map[string]bool{}
	drainSelf := true
	pendingTraffic := map[string]bool{}
	drainTraffic := deps.Traffic != nil
	var trafficAck timing.Timer
	var trafficAckC <-chan time.Time
	defer func() {
		if trafficAck != nil {
			trafficAck.Stop()
		}
	}()
	var responseC, eventAckC <-chan time.Time
	defer func() {
		if response != nil {
			response.Stop()
		}
		if eventAck != nil {
			eventAck.Stop()
		}
		if checkerAck != nil {
			checkerAck.Stop()
		}
		if selfAck != nil {
			selfAck.Stop()
		}
	}()
	type reportKind int
	const (
		heartbeatReport reportKind = iota
		probeReport
		eventReport
		configReport
		rotationReport
		selfCheckReport
		dnsReport
		trafficReport
		checkerReport
		upgradeReport
	)
	var checkerResult []byte
	send := func(kind reportKind, sample *controlpb.ProbeSample) error {
		outgoing := proto.Clone(report).(*controlpb.NodeReport)
		outgoing.Probe, outgoing.ProbeOnly, outgoing.EventsOnly = sample, kind == probeReport, kind == eventReport
		outgoing.Events, outgoing.PublicIpv4 = nil, ""
		outgoing.Recovery = nil
		outgoing.UpgradeProgress = nil
		outgoing.UpgradeOnly = kind == upgradeReport
		if kind == upgradeReport {
			outgoing.UpgradeProgress = deps.upgrades.snapshot()
			if len(outgoing.UpgradeProgress) == 0 {
				return nil
			}
			var progress nodeupdate.Progress
			if json.Unmarshal(outgoing.UpgradeProgress, &progress) == nil {
				progress.Verification = &nodeupdate.Verification{Instance: report.ProcessInstance, Version: deps.SoftwareVersion, Platform: nodeupdate.Platform, SHA256: deps.ArtifactSHA256, Size: deps.ArtifactSize}
				if progress.State == "reconnecting" || progress.State == "unconfirmed" {
					progress.Verification.Ready = engine.UpgradeReady(ctx) == nil
				}
				outgoing.UpgradeProgress, _ = json.Marshal(progress)
			}
		}
		outgoing.SelfCheckOnly = kind == selfCheckReport
		outgoing.CheckerOnly = kind == checkerReport
		outgoing.CheckerResults = nil
		if kind == checkerReport {
			outgoing.CheckerResults = [][]byte{checkerResult}
			var result checker.Result
			if err := json.Unmarshal(checkerResult, &result); err != nil {
				return err
			}
			checkerPending[result.Task.ID] = true
			if checkerAckC == nil {
				checkerAck = clock.NewTimer(15 * time.Second)
				checkerAckC = checkerAck.C()
			}
		}
		outgoing.SelfCheckResults = nil
		if kind == selfCheckReport {
			if len(selfPending) > 0 {
				return nil
			}
			outgoing.SelfCheckResults = deps.selfChecks.batch()
			if len(outgoing.SelfCheckResults) == 0 {
				return nil
			}
			for _, data := range outgoing.SelfCheckResults {
				var round struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(data, &round); err != nil {
					return err
				}
				selfPending[round.ID] = true
			}
			selfAck = clock.NewTimer(15 * time.Second)
			selfAckC = selfAck.C()
		}
		outgoing.DnsWatchOnly = kind == dnsReport
		outgoing.DnsWatchResults = nil
		if kind == dnsReport {
			outgoing.DnsWatchResults = []*controlpb.DNSWatchResult{dnsResult}
		}
		outgoing.TrafficOnly, outgoing.Traffic = kind == trafficReport, nil
		outgoing.Rotation = nil
		outgoing.RotationOnly = kind == rotationReport
		if kind == rotationReport {
			outgoing.Rotation = deps.rotations.snapshot()
		}
		if kind != configReport {
			outgoing.AttemptedRevision, outgoing.ApplyError = 0, ""
			outgoing.RuleResults, outgoing.AttemptedDigest = nil, ""
			outgoing.RevocationsApplied = false
		} else if err := limitConfigReport(outgoing); err != nil {
			return err
		}
		if kind == probeReport || kind == eventReport || kind == rotationReport || kind == selfCheckReport || kind == dnsReport || kind == trafficReport || kind == checkerReport || kind == upgradeReport {
			outgoing.AppliedRevision, outgoing.AppliedDigest = 0, ""
		}
		if kind == heartbeatReport {
			outgoing.UpgradeCapable = deps.upgrades != nil && deps.upgrades.available.Load()
			if deps.LocalHealth != nil && !deps.localReady.Load() {
				deps.localReady.Store(engine.UpgradeReady(ctx) == nil)
			}
			outgoing.Recovery = deps.rotations.recoveryReport(engine.State().HighestRevision, clock.Now())
			queryCtx, queryCancel := timeout(ctx, clock, 5*time.Second)
			if value, err := deps.PublicIPv4(queryCtx); err == nil && queryCtx.Err() == nil {
				if !deps.rotations.blocksIPObservation() {
					outgoing.PublicIpv4, _ = publicip.Parse(value)
				}
			}
			queryCancel()
		}
		if deps.Events != nil && len(pending) == 0 && (kind == eventReport || kind == heartbeatReport && proto.Size(outgoing.Recovery) <= 20<<10) {
			batch, err := deps.Events.Batch(ctx)
			if err != nil {
				return err
			}
			for _, e := range batch {
				outgoing.Events = append(outgoing.Events, e.Proto())
				pending[e.ID] = true
			}
			if len(batch) > 0 {
				eventAck = clock.NewTimer(15 * time.Second)
				eventAckC = eventAck.C()
			}
		}
		if kind == eventReport && len(outgoing.Events) == 0 {
			return nil
		}
		if kind == trafficReport {
			outgoing.Traffic = deps.Traffic.Batch()
			if len(outgoing.Traffic) == 0 {
				return nil
			}
			for _, s := range outgoing.Traffic {
				pendingTraffic[trafficAckKey(s.ProcessInstance, s.Sequence)] = true
			}
			trafficAck = clock.NewTimer(15 * time.Second)
			trafficAckC = trafficAck.C()
		}
		limitRecoveryReport(outgoing)
		// 发送串行，传输只持有本轮不可变副本；接收持续独立运行。
		result := make(chan error, 1)
		timer := clock.NewTimer(15 * time.Second)
		defer timer.Stop()
		go func() { result <- stream.Send(outgoing) }()
		select {
		case err := <-result:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C():
			return errors.New("控制发送超时")
		}
		if kind != configReport && kind != trafficReport && kind != upgradeReport && responseC == nil {
			response = clock.NewTimer(15 * time.Second)
			responseC = response.C()
		}
		return nil
	}
	var upgradeWake <-chan struct{}
	if deps.upgrades != nil {
		upgradeWake = deps.upgrades.wake
	}
	var rotationWake <-chan struct{}
	if deps.rotations != nil {
		rotationWake = deps.rotations.wake
	}
	var selfWake <-chan struct{}
	if deps.selfChecks != nil {
		selfWake = deps.selfChecks.wake
	}
	nextHeartbeat := clock.Now().Add(10 * time.Second)
	if err := send(heartbeatReport, nil); err != nil {
		return err
	}
	if report.AttemptedRevision > 0 {
		if err := send(configReport, nil); err != nil {
			return err
		}
	}
	if deps.rotations != nil && deps.rotations.snapshot() != nil {
		if err := send(rotationReport, nil); err != nil {
			return err
		}
	}
	if deps.upgrades != nil {
		if err := send(upgradeReport, nil); err != nil {
			return err
		}
	}
	heartbeat := clock.NewTimer(nextHeartbeat.Sub(clock.Now()))
	defer func() { heartbeat.Stop() }()
	sendProbe := func(update probeUpdate) error {
		if clock.Now().Sub(update.at) >= 2*probe.IntervalSeconds*time.Second {
			return nil
		}
		return send(probeReport, update.sample)
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 事件连续补报也必须让出到期心跳和最新探针。
		if !clock.Now().Before(nextHeartbeat) {
			nextHeartbeat = clock.Now().Add(10 * time.Second)
			if err := send(heartbeatReport, nil); err != nil {
				return err
			}
			if deps.upgrades != nil {
				if err := send(upgradeReport, nil); err != nil {
					return err
				}
			}
			heartbeat.Stop()
			heartbeat = clock.NewTimer(nextHeartbeat.Sub(clock.Now()))
			drainEvents = true
		}
		select {
		case update := <-updates:
			if err := sendProbe(update); err != nil {
				return err
			}
		default:
		}
		if drainSelf && len(selfPending) == 0 {
			drainSelf = false
			if err := send(selfCheckReport, nil); err != nil {
				return err
			}
		}
		if drainEvents && len(pending) == 0 {
			drainEvents = false
			if err := send(eventReport, nil); err != nil {
				return err
			}
		}
		if drainTraffic && len(pendingTraffic) == 0 {
			drainTraffic = false
			if err := send(trafficReport, nil); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-checkerAckC:
			return errors.New("检测点结果确认超时")
		case result := <-checks.results:
			if onlineCtx.Err() != nil {
				continue
			}
			checkerResult, _ = json.Marshal(result)
			if err := send(checkerReport, nil); err != nil {
				return err
			}
		case <-selfAckC:
			return errors.New("自测确认超时")
		case <-selfWake:
			drainSelf = true
		case dnsResult = <-dnsResults:
			if err := send(dnsReport, nil); err != nil {
				return err
			}
		case <-upgradeWake:
			if err := send(upgradeReport, nil); err != nil {
				return err
			}
		case <-rotationWake:
			if err := send(rotationReport, nil); err != nil {
				return err
			}
		case <-responseC:
			return errors.New("控制响应超时")
		case <-eventAckC:
			return errors.New("事件确认超时")
		case <-trafficAckC:
			// 只重发流量，不清除其他报告的确认/超时状态。
			pendingTraffic = map[string]bool{}
			trafficAckC = nil
			drainTraffic = true
		case <-deps.trafficUpdates:
			drainTraffic = true
		case <-heartbeat.C():
		case update := <-updates:
			if err := sendProbe(update); err != nil {
				return err
			}
		case received := <-messages:
			if received.err != nil {
				return received.err
			}
			m := received.message
			if !compatibility.Contains(compatibility.ControlPrevious, compatibility.ControlCurrent, m.ProtocolVersion) {
				return errors.New("控制协议不兼容")
			}
			if m.UpgradeOnly {
				if len(m.UpgradeManifest) > 0 {
					deps.upgrades.submit(m.UpgradeManifest)
				}
				continue
			}
			if m.TrafficOnly {
				matched := make([]*controlpb.TrafficAck, 0, len(m.AcknowledgedTraffic))
				for _, a := range m.AcknowledgedTraffic {
					if a != nil && pendingTraffic[trafficAckKey(a.ProcessInstance, a.Sequence)] {
						matched = append(matched, a)
					}
				}
				// 累计报告可能超时重传；迟到或重复 ACK 只忽略，不重置控制流。
				if len(matched) == 0 {
					continue
				}
				saveErr := deps.Traffic.Ack(ctx, matched)
				for _, a := range matched {
					delete(pendingTraffic, trafficAckKey(a.ProcessInstance, a.Sequence))
				}
				if len(pendingTraffic) == 0 {
					trafficAck.Stop()
					trafficAckC = nil
					drainTraffic = saveErr == nil
				}
				continue
			}
			if response != nil {
				response.Stop()
			}
			responseC = nil
			if len(m.AcknowledgedSelfCheckIds) > 0 {
				for _, id := range m.AcknowledgedSelfCheckIds {
					if !selfPending[id] {
						return errors.New("自测确认不属于在途批次")
					}
				}
				if err := deps.selfChecks.ack(ctx, m.AcknowledgedSelfCheckIds); err != nil {
					return err
				}
				for _, id := range m.AcknowledgedSelfCheckIds {
					delete(selfPending, id)
				}
				if len(selfPending) == 0 {
					selfAck.Stop()
					selfAckC = nil
					drainSelf = true
				}
			}
			if len(m.AcknowledgedEventIds) > 0 {
				for _, id := range m.AcknowledgedEventIds {
					if !pending[id] {
						return errors.New("事件确认不属于在途批次")
					}
				}
				if err := deps.Events.Ack(ctx, m.AcknowledgedEventIds); err != nil {
					return err
				}
				for _, id := range m.AcknowledgedEventIds {
					delete(pending, id)
				}
				if len(pending) == 0 {
					eventAck.Stop()
					eventAckC = nil
					// 重启可恢复未满在途批次；确认后仍须尝试后续事件。
					drainEvents = true
				}
			}
			if len(dnsSeen) > 1024 {
				dnsSeen = map[string]bool{}
			}
			if len(m.DnsWatchTasks) > 0 && dnsJobs == nil {
				dnsJobs, dnsResults, stopDNS = startDNSWatchRunner(streamCtx, deps)
			}
			for _, task := range m.DnsWatchTasks {
				if task == nil || dnsSeen[task.TaskId] || len(task.TaskId) != 43 || !clock.Now().Before(time.Unix(0, task.ExpiresAt)) {
					continue
				}
				resolver := dnswatch.Resolver{Kind: "dns", Address: task.Resolver}
				domain, err := dnswatch.Domain(task.Domain)
				if resolver.Validate() != nil || err != nil || domain != task.Domain {
					continue
				}
				select {
				case dnsJobs <- task:
					dnsSeen[task.TaskId] = true
				default:
				}
			}
			for _, id := range m.AcknowledgedCheckerIds {
				if !checkerPending[id] {
					return errors.New("检测点确认不属于在途结果")
				}
				delete(checkerPending, id)
				delete(checks.active, id)
			}
			if len(checkerPending) == 0 && checkerAck != nil {
				checkerAck.Stop()
				checkerAckC = nil
			}
			checks.submit(m.CheckerTasks)
			if m.RotationCommand != nil {
				deps.rotations.submit(onlineCtx, report.NodeId, m.RotationCommand)
			}
			snapshot := m.Snapshot
			if snapshot == nil {
				continue
			}
			if snapshot.Revision != m.DesiredRevision {
				return errors.New("配置快照版本不一致")
			}
			revision, digest := engine.Applied()
			if snapshot.Revision < report.AttemptedRevision || (snapshot.Revision == revision && snapshot.Digest == digest && exitAddressVersionsApplied(engine, snapshot)) {
				continue
			}
			report.AttemptedRevision = snapshot.Revision
			report.AttemptedDigest = snapshot.Digest
			err := forwarding.ValidateSnapshot(snapshot)
			if err == nil {
				err = deps.selfChecks.apply(ctx, snapshot.SelfCheckConfig, snapshot.Revision)
			}
			if err == nil {
				err = engine.ApplyContext(onlineCtx, snapshot)
			}
			report.AppliedRevision, report.AppliedDigest = engine.Applied()
			if deps.localReady != nil && deps.LocalHealth != nil {
				deps.localReady.Store(err == nil && engine.UpgradeReady(ctx) == nil)
			}
			state := engine.State()
			report.RuleResults = state.Results
			report.RevocationsApplied = state.HighestRevision == snapshot.Revision && state.HighestDigest == snapshot.Digest
			if state.HighestRevision != snapshot.Revision || state.HighestDigest != snapshot.Digest {
				report.RuleResults = nil
				for _, r := range snapshot.Rules {
					result := &controlpb.RuleApplyResult{RuleId: r.Id, ApplyError: "配置未应用：缓存写入或快照校验失败"}
					for _, previous := range state.Results {
						if previous.RuleId == r.Id {
							result.AppliedRevision = previous.AppliedRevision
						}
					}
					report.RuleResults = append(report.RuleResults, result)
				}
			}
			report.ApplyError = ""
			if err != nil {
				if deps.Events != nil {
					if recordErr := deps.Events.Record(ctx, "sync_failed", "节点配置应用失败", snapshot.Revision); recordErr != nil {
						return errors.New("同步失败事件持久化失败")
					}
					drainEvents = true
				}
				report.ApplyError = forwarding.ErrorSummary(err.Error(), 1024)
			}
			if err := send(configReport, nil); err != nil {
				return err
			}
		}
	}
}

func timeout(ctx context.Context, clock timing.Clock, d time.Duration) (context.Context, context.CancelFunc) {
	child, cancel := context.WithCancel(ctx)
	timer := clock.NewTimer(d)
	go func() {
		defer timer.Stop()
		select {
		case <-child.Done():
		case <-timer.C():
			cancel()
		}
	}()
	return child, cancel
}
