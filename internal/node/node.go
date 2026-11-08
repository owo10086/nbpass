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
	Clock       timing.Clock
	Store       Store
	Random      io.Reader
	RootCAs     *x509.CertPool
	DialContext func(context.Context, string) (net.Conn, error)
	PublicIPv4  func(context.Context) (string, error)
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
	instance := make([]byte, 32)
	if _, err := io.ReadFull(deps.Random, instance); err != nil {
		return errors.New("无法生成节点进程实例")
	}
	report := &controlpb.NodeReport{NodeId: credential.NodeID, ProcessInstance: base64.RawURLEncoding.EncodeToString(instance), ProtocolVersion: ProtocolVersion, SoftwareVersion: SoftwareVersion}
	for {
		if ctx.Err() != nil {
			return nil
		}
		// 由注入时钟控制重试，不额外等待 gRPC 内部的墙上时钟退避。
		client.ResetConnectBackoff()
		err := exchange(ctx, service, credential.Token, report, deps)
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

func exchange(ctx context.Context, service controlpb.ControlServiceClient, token string, report *controlpb.NodeReport, deps Dependencies) error {
	clock := deps.Clock
	streamCtx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token))
	defer cancel()
	stream, err := service.Connect(streamCtx)
	if err != nil {
		return err
	}
	for {
		next := clock.Now().Add(10 * time.Second)
		current := proto.Clone(report).(*controlpb.NodeReport)
		queryCtx, queryCancel := timeout(ctx, clock, 5*time.Second)
		if value, err := deps.PublicIPv4(queryCtx); err == nil && queryCtx.Err() == nil {
			current.PublicIpv4, _ = publicip.Parse(value)
		}
		queryCancel()
		// 每次请求得到确认才开始下一活性周期，未响应的流也必须超时重连。
		result := make(chan error, 1)
		go func() {
			if err := stream.Send(current); err != nil && !errors.Is(err, io.EOF) {
				result <- err
				return
			}
			message, err := stream.Recv()
			if err == nil && message.ProtocolVersion != ProtocolVersion {
				err = errors.New("控制协议不兼容")
			}
			result <- err
		}()
		timer := clock.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C():
			return errors.New("控制响应超时")
		case err := <-result:
			timer.Stop()
			if err != nil {
				return err
			}
		}
		timer = clock.NewTimer(next.Sub(clock.Now()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C():
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
