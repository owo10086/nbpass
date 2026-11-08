// Package node 是独立节点核心：凭票据接入，经验证的 TLS 控制流与断线重连。
package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
	"nbpass/internal/probe"
	"nbpass/internal/publicip"
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
	Probe       probe.Collector
	Connections probe.Connections
	Clock       timing.Clock
	Store       Store
	Random      io.Reader
	RootCAs     *x509.CertPool
	DialContext func(context.Context, string) (net.Conn, error)
	PublicIPv4  func(context.Context) (string, error)
	Events      *EventQueue
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
		err := exchange(ctx, service, credential.Token, report, deps, updates)
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

func exchange(ctx context.Context, service controlpb.ControlServiceClient, token string, report *controlpb.NodeReport, deps Dependencies, updates <-chan probeUpdate) error {
	clock := deps.Clock
	streamCtx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token))
	defer cancel()
	stream, err := service.Connect(streamCtx)
	if err != nil {
		return err
	}
	// 每条新流先发活性；探针有自己的周期，不改变既有活性节奏。
	report.Probe, report.ProbeOnly, report.EventsOnly = nil, false, false
	nextHeartbeat := clock.Now().Add(10 * time.Second)
	for {
		// 每次请求得到确认才开始下一活性周期，未响应的流也必须超时重连。
		type response struct {
			message *controlpb.PanelMessage
			err     error
		}
		result := make(chan response, 1)
		progress := false
		// 超时取消与传输退出并非同步；旧流只能持有本轮不可变的报告。
		outgoing := proto.Clone(report).(*controlpb.NodeReport)
		// 公网 IP 属于 10 秒活性周期；独立探针不查询，避免被外部查询拖住。
		if !outgoing.ProbeOnly && !outgoing.EventsOnly {
			queryCtx, queryCancel := timeout(ctx, clock, 5*time.Second)
			if value, err := deps.PublicIPv4(queryCtx); err == nil && queryCtx.Err() == nil {
				outgoing.PublicIpv4, _ = publicip.Parse(value)
			}
			queryCancel()
		}
		if deps.Events != nil && !outgoing.ProbeOnly {
			batch, err := deps.Events.Batch(ctx)
			if err != nil {
				return err
			}
			for _, e := range batch {
				outgoing.Events = append(outgoing.Events, e.Proto())
			}
		}
		go func() {
			if err := stream.Send(outgoing); err != nil && !errors.Is(err, io.EOF) {
				result <- response{err: err}
				return
			}
			message, err := stream.Recv()
			if err == nil && message.ProtocolVersion != ProtocolVersion {
				err = errors.New("控制协议不兼容")
			}
			result <- response{message, err}
		}()
		timer := clock.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C():
			return errors.New("控制响应超时")
		case received := <-result:
			timer.Stop()
			if received.err != nil {
				return received.err
			}
			if deps.Events != nil {
				sentIDs := make(map[string]bool, len(outgoing.Events))
				for _, e := range outgoing.Events {
					sentIDs[e.EventId] = true
				}
				for _, id := range received.message.AcknowledgedEventIds {
					if !sentIDs[id] {
						return errors.New("事件确认不属于当前批次")
					}
				}
				if err := deps.Events.Ack(ctx, received.message.AcknowledgedEventIds); err != nil {
					return err
				}
				progress = len(received.message.AcknowledgedEventIds) > 0
			}
		}
		// 满批有确认进展才继续补报，不推进活性或其他报告状态。
		if len(outgoing.Events) == eventlog.MaxBatch && progress {
			report.Probe, report.ProbeOnly, report.EventsOnly = nil, false, true
			// 每批之间让出到期活性及最新探针，慢 ACK 不能饿死其他上报。
			select {
			case update := <-updates:
				if clock.Now().Sub(update.at) < 2*probe.IntervalSeconds*time.Second {
					report.Probe = update.sample
					report.ProbeOnly = clock.Now().Before(nextHeartbeat)
					report.EventsOnly = false
				}
			default:
			}
			if !clock.Now().Before(nextHeartbeat) {
				report.ProbeOnly, report.EventsOnly = false, false
				nextHeartbeat = clock.Now().Add(10 * time.Second)
			}
			continue
		}
		report.Probe = nil
		report.EventsOnly = false
		for {
			timer = clock.NewTimer(nextHeartbeat.Sub(clock.Now()))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C():
				report.ProbeOnly = false
			case update := <-updates:
				timer.Stop()
				if clock.Now().Sub(update.at) >= 2*probe.IntervalSeconds*time.Second {
					continue
				}
				report.Probe = update.sample
				report.ProbeOnly = clock.Now().Before(nextHeartbeat)
			}
			if !report.ProbeOnly {
				nextHeartbeat = clock.Now().Add(10 * time.Second)
			}
			break
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
