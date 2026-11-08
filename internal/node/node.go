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
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
	"nbpass/internal/forwarding"
	"nbpass/internal/probe"
	"nbpass/internal/publicip"
	"nbpass/internal/rotation"
	"nbpass/internal/timing"
)

const ProtocolVersion = 1
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
	SelfCheckLookup     func(context.Context, string) ([]string, error)
	SelfCheckDial       func(context.Context, string) (net.Conn, error)
	SelfCheckStore      SelfCheckStore
	selfChecks          *selfCheckRunner
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
	Events              *EventQueue
	Forwarding          forwarding.Dependencies
}

func Run(ctx context.Context, config Config, deps Dependencies) error {
	u, err := url.Parse(config.ControlURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("控制地址必须是不带路径的 HTTPS 地址")
	}
	if deps.Clock == nil || deps.Store == nil || deps.Random == nil {
		return errors.New("节点外部依赖不完整")
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
	engine := forwarding.NewEngine(deps.Forwarding)
	defer engine.Close()
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
	report := &controlpb.NodeReport{NodeId: credential.NodeID, ProcessInstance: base64.RawURLEncoding.EncodeToString(instance), ProtocolVersion: ProtocolVersion, SoftwareVersion: SoftwareVersion}
	probeCtx, stopProbe := context.WithCancel(ctx)
	defer stopProbe()
	updates := make(chan probeUpdate, 1)
	go sampleProbe(probeCtx, deps, started, updates)
	for {
		if ctx.Err() != nil {
			return nil
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
	pendingFull := false
	drainEvents := false
	var response, eventAck, selfAck timing.Timer
	var selfAckC <-chan time.Time
	selfPending := map[string]bool{}
	drainSelf := true
	var responseC, eventAckC <-chan time.Time
	defer func() {
		if response != nil {
			response.Stop()
		}
		if eventAck != nil {
			eventAck.Stop()
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
	)
	send := func(kind reportKind, sample *controlpb.ProbeSample) error {
		outgoing := proto.Clone(report).(*controlpb.NodeReport)
		outgoing.Probe, outgoing.ProbeOnly, outgoing.EventsOnly = sample, kind == probeReport, kind == eventReport
		outgoing.Events, outgoing.PublicIpv4 = nil, ""
		outgoing.SelfCheckOnly = kind == selfCheckReport
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
		outgoing.Rotation = nil
		outgoing.RotationOnly = kind == rotationReport
		if kind == rotationReport {
			outgoing.Rotation = deps.rotations.snapshot()
		}
		if kind != configReport {
			outgoing.AttemptedRevision, outgoing.ApplyError = 0, ""
		}
		if kind == probeReport || kind == eventReport || kind == rotationReport || kind == selfCheckReport {
			outgoing.AppliedRevision, outgoing.AppliedDigest = 0, ""
		}
		if kind == heartbeatReport {
			queryCtx, queryCancel := timeout(ctx, clock, 5*time.Second)
			if value, err := deps.PublicIPv4(queryCtx); err == nil && queryCtx.Err() == nil {
				if !deps.rotations.blocksIPObservation() {
					outgoing.PublicIpv4, _ = publicip.Parse(value)
				}
			}
			queryCancel()
		}
		if deps.Events != nil && len(pending) == 0 && (kind == heartbeatReport || kind == eventReport) {
			batch, err := deps.Events.Batch(ctx)
			if err != nil {
				return err
			}
			for _, e := range batch {
				outgoing.Events = append(outgoing.Events, e.Proto())
				pending[e.ID] = true
			}
			pendingFull = len(batch) == eventlog.MaxBatch
			if len(batch) > 0 {
				eventAck = clock.NewTimer(15 * time.Second)
				eventAckC = eventAck.C()
			}
		}
		if kind == eventReport && len(outgoing.Events) == 0 {
			return nil
		}
		// 发送串行，传输只持有本轮不可变副本；接收持续独立运行。
		result := make(chan error, 1)
		go func() { result <- stream.Send(outgoing) }()
		timer := clock.NewTimer(15 * time.Second)
		defer timer.Stop()
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
		if kind != configReport && responseC == nil {
			response = clock.NewTimer(15 * time.Second)
			responseC = response.C()
		}
		return nil
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
	if deps.rotations != nil && deps.rotations.snapshot() != nil {
		if err := send(rotationReport, nil); err != nil {
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
			heartbeat.Stop()
			heartbeat = clock.NewTimer(nextHeartbeat.Sub(clock.Now()))
			drainEvents = false
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
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-selfAckC:
			return errors.New("自测确认超时")
		case <-selfWake:
			drainSelf = true
		case <-rotationWake:
			if err := send(rotationReport, nil); err != nil {
				return err
			}
		case <-responseC:
			return errors.New("控制响应超时")
		case <-eventAckC:
			return errors.New("事件确认超时")
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
			if m.ProtocolVersion != ProtocolVersion {
				return errors.New("控制协议不兼容")
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
					drainEvents = drainEvents || pendingFull
				}
			}
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
			if snapshot.Revision < report.AttemptedRevision || (snapshot.Revision == revision && snapshot.Digest == digest) {
				continue
			}
			report.AttemptedRevision = snapshot.Revision
			err := forwarding.ValidateSnapshot(snapshot)
			if err == nil {
				err = deps.selfChecks.apply(ctx, snapshot.SelfCheckConfig, snapshot.Revision)
			}
			if err == nil {
				err = engine.Apply(snapshot)
			}
			report.AppliedRevision, report.AppliedDigest = engine.Applied()
			report.ApplyError = ""
			if err != nil {
				if deps.Events != nil {
					if recordErr := deps.Events.Record(ctx, "sync_failed", "节点配置应用失败", snapshot.Revision); recordErr != nil {
						return errors.New("同步失败事件持久化失败")
					}
					drainEvents = true
				}
				report.ApplyError = strings.ToValidUTF8(err.Error(), "�")
				if len(report.ApplyError) > 1024 {
					report.ApplyError = report.ApplyError[:1024]
					for !utf8.ValidString(report.ApplyError) {
						report.ApplyError = report.ApplyError[:len(report.ApplyError)-1]
					}
				}
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
