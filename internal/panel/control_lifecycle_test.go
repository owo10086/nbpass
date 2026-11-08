package panel

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"nbpass/internal/controlpb"
)

// 物理生命周期断言：恶意传输不读回包时，撤销也必须释放 handler。
// 业务操作仍走 HTTP；只将传输的阻塞写作为可注入外部故障。
func TestRevocationReleasesHandlerWithBlockedTransportWrite(t *testing.T) {
	testRevocationReleasesTransport(t, false)
}

func TestRevocationClosesStreamWaitingForFirstReport(t *testing.T) {
	testRevocationReleasesTransport(t, true)
}

func testRevocationReleasesTransport(t *testing.T, waitFirst bool) {
	t.Helper()
	p := openDiskPanel(t, t.TempDir())
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	created := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": "节点 A"}, cookie, 201)
	var installation struct {
		ID     string `json:"id"`
		Ticket string `json:"installation_ticket"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &installation); err != nil {
		t.Fatal(err)
	}
	service := &controlService{p: p}
	credential, err := service.Enroll(context.Background(), &controlpb.EnrollRequest{InstallationTicket: installation.Ticket})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+credential.Token)))
	defer cancel()
	stream := &blockedWriteStream{ctx: ctx, started: make(chan struct{}), waitFirst: waitFirst}
	done := make(chan error, 1)
	go func() { defer cancel(); done <- service.Connect(stream) }()
	select {
	case <-stream.started:
	case <-time.After(2 * time.Second):
		t.Fatal("未开始传输")
	}
	diskRequest(t, p, "POST", "/api/nodes/"+installation.ID+"/token/revoke", nil, cookie, 204)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("撤销后传输处理器未退出")
	}
}

type blockedWriteStream struct {
	ctx       context.Context
	started   chan struct{}
	received  bool
	waitFirst bool
}

func (s *blockedWriteStream) Context() context.Context { return s.ctx }
func (s *blockedWriteStream) Send(*controlpb.PanelMessage) error {
	close(s.started)
	<-s.ctx.Done()
	return s.ctx.Err()
}
func (s *blockedWriteStream) Recv() (*controlpb.NodeReport, error) {
	if s.waitFirst {
		close(s.started)
		<-s.ctx.Done()
		return nil, s.ctx.Err()
	}
	if !s.received {
		s.received = true
		return &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "instance-a", SoftwareVersion: "test"}, nil
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}
func (*blockedWriteStream) SetHeader(metadata.MD) error  { return nil }
func (*blockedWriteStream) SendHeader(metadata.MD) error { return nil }
func (*blockedWriteStream) SetTrailer(metadata.MD)       {}
func (*blockedWriteStream) SendMsg(any) error            { panic("unused") }
func (*blockedWriteStream) RecvMsg(any) error            { panic("unused") }
