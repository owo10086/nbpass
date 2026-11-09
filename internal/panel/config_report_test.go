package panel

import (
	"context"
	"encoding/json"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/platform"
	"nbpass/internal/timing"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 物理协议防护：直接注入恶意/迟到报告，业务读取仍走管理 HTTP。
func TestConfigReportCannotReplaceConfirmedDigestOrRevision(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	defer p.Close()
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	created := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": "入口"}, cookie, 201)
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	snapshot := &controlpb.ConfigSnapshot{Revision: 2}
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	data, err := proto.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET desired_revision=2,token_hash=? WHERE id=?", digest, result.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES(?,?,?)", result.ID, data, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), instance: "current"}
	p.controls[result.ID] = session
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: 2, AppliedRevision: 2, AppliedDigest: snapshot.Digest}
	if _, err := p.acceptReport(context.Background(), result.ID, digest, session, report); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*controlpb.NodeReport{
		{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: 1, AppliedRevision: 1, AppliedDigest: "old"},
		{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: 3, AppliedRevision: 3, AppliedDigest: "future"},
		{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: 2, AppliedRevision: 2, AppliedDigest: "tampered"},
		{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: 2, ApplyError: "late failure"},
	} {
		if _, err := p.acceptReport(context.Background(), result.ID, digest, session, bad); err != nil {
			t.Fatal(err)
		}
		response := diskRequest(t, p, "GET", "/api/nodes", nil, cookie, 200)
		var body struct {
			Nodes []nodeView `json:"nodes"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		row := body.Nodes[0]
		if row.AppliedRevision != 2 || row.AppliedDigest != snapshot.Digest || row.ApplyError != "" {
			t.Fatalf("迟到或伪造报告覆盖确认: %+v", row)
		}
	}
	if _, err := p.acceptReport(context.Background(), result.ID, digest, &controlSession{instance: "current"}, report); err == nil {
		t.Fatal("旧控制流覆盖状态")
	}
	report.ProcessInstance = "retired"
	if _, err := p.acceptReport(context.Background(), result.ID, digest, session, report); err == nil {
		t.Fatal("旧进程实例覆盖状态")
	}

}

// 物理传输验证：没有节点报告驱动时，60 秒计时器仍独立重发完整快照。
func TestControlResendsFullSnapshotEverySixtySeconds(t *testing.T) {
	clock := &syncOnlyClock{timers: make(chan *syncOnlyTimer, 4)}
	deps := diskDependencies(t.TempDir())
	deps.Clock = clock
	p, err := Open(context.Background(), Config{PublicOrigin: "https://panel.test"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	token, digest, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &controlpb.ConfigSnapshot{Revision: 7, Rules: []*controlpb.ForwardRule{{Id: "r", EntryGroupId: "g", ListenPort: 12345, TargetAddress: "8.8.8.8", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: 443, Kind: controlpb.RuleKind_RULE_KIND_DIRECT}}}
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	data, err := proto.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO nodes(id,name,token_hash,created_at,desired_revision) VALUES('n','入口',?,0,7)", digest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES('n',?,?)", data, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)))
	defer cancel()
	stream := &snapshotStream{ctx: ctx, sent: make(chan *controlpb.PanelMessage, 4)}
	done := make(chan error, 1)
	go func() { done <- (&controlService{p: p}).Connect(stream) }()
	receive := func() *controlpb.PanelMessage {
		t.Helper()
		select {
		case m := <-stream.sent:
			return m
		case <-time.After(2 * time.Second):
			t.Fatal("未发送配置快照")
			return nil
		}
	}
	initial := receive()
	if !proto.Equal(initial.Snapshot, snapshot) {
		t.Fatalf("首包不是完整快照: %v", initial)
	}
	// 面板还会创建保留期清理等一分钟计时器，不能假设第一个属于控制流。
	// 推进全部一分钟计时器，仍只断言真实控制流独立重发的完整快照。
	var repeated *controlpb.PanelMessage
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for repeated == nil {
		select {
		case timer := <-clock.timers:
			timer.channel <- time.Now()
		case repeated = <-stream.sent:
		case <-deadline.C:
			t.Fatal("一分钟计时器触发后未重发配置快照")
		}
	}
	if repeated.DesiredRevision != 7 || !proto.Equal(repeated.Snapshot, snapshot) {
		t.Fatalf("周期重发丢失版本或完整内容: %v", repeated)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("控制流未取消")
	}
}

type syncOnlyClock struct{ timers chan *syncOnlyTimer }

func (c *syncOnlyClock) Now() time.Time { return time.Now() }
func (c *syncOnlyClock) NewTimer(d time.Duration) timing.Timer {
	if d == 60*time.Second {
		timer := &syncOnlyTimer{channel: make(chan time.Time, 1)}
		c.timers <- timer
		return timer
	}
	return platform.RealClock{}.NewTimer(d)
}

type syncOnlyTimer struct{ channel chan time.Time }

func (t *syncOnlyTimer) C() <-chan time.Time { return t.channel }
func (t *syncOnlyTimer) Stop() bool          { return true }

type snapshotStream struct {
	ctx      context.Context
	sent     chan *controlpb.PanelMessage
	received bool
}

func (s *snapshotStream) Context() context.Context { return s.ctx }
func (s *snapshotStream) Send(m *controlpb.PanelMessage) error {
	select {
	case s.sent <- m:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *snapshotStream) Recv() (*controlpb.NodeReport, error) {
	if !s.received {
		s.received = true
		return &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "instance"}, nil
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}
func (*snapshotStream) SetHeader(metadata.MD) error  { return nil }
func (*snapshotStream) SendHeader(metadata.MD) error { return nil }
func (*snapshotStream) SetTrailer(metadata.MD)       {}
func (*snapshotStream) SendMsg(any) error            { panic("unused") }
func (*snapshotStream) RecvMsg(any) error            { panic("unused") }
