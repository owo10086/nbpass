package panel

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/selfcheck"
)

func selfCheckWire(t *testing.T, sequence uint64, now time.Time) []byte {
	t.Helper()
	round := selfcheck.Round{ID: fmt.Sprintf("%032x", sequence), Sequence: sequence, ConfigRevision: 7, ConfigKey: selfcheck.ConfigKey(selfcheck.Config{Enabled: true, Settings: selfcheck.Defaults()}), StartedAt: now, FinishedAt: now, Source: "self_check", Result: "abnormal"}
	for _, host := range selfcheck.Defaults().Sites {
		round.Sites = append(round.Sites, selfcheck.Site{Host: host, Addresses: []string{"203.0.113.1"}, ResolveAttempts: 1, Result: "failed", Connections: []selfcheck.Connection{{Address: "203.0.113.1:443", Error: "TCP 连接失败或超时"}}})
	}
	data, err := json.Marshal(round)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// 验证协议路由物理隔离：混入其他载荷的自测包只消费自测，其他类型不能消费自测。
func TestSelfCheckReportPayloadsAreIndependent(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	digest := sha256.Sum256([]byte("self-check-token"))
	now := p.deps.Clock.Now()
	if _, err := p.db.Exec("INSERT INTO nodes(id,name,token_hash,created_at,last_seen_at,desired_revision,process_instance) VALUES('n','节点',?,0,123,7,'instance')", digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES('n',X'','expected')"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO node_self_check(node_id,enabled) VALUES('n',1)"); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), instance: "instance"}
	p.controls["n"] = session
	wire := selfCheckWire(t, 1, now)
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "instance", SelfCheckOnly: true, SelfCheckResults: [][]byte{wire}, PublicIpv4: "1.1.1.1", AppliedRevision: 7, AppliedDigest: "expected", AttemptedRevision: 7, Probe: &controlpb.ProbeSample{Sequence: 1, Values: map[string]float64{"cpu_percent": 50}}, Events: []*controlpb.Event{{EventId: "unrelated-invalid-event"}}}
	response, err := p.acceptReport(ctx, "n", digest[:], session, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.AcknowledgedSelfCheckIds) != 1 || len(response.AcknowledgedEventIds) != 0 {
		t.Fatalf("确认串线: %v", response)
	}
	var last, applied, probes, events, ips int
	if err := p.db.QueryRow("SELECT last_seen_at,applied_revision FROM nodes WHERE id='n'").Scan(&last, &applied); err != nil {
		t.Fatal(err)
	}
	if last != 123 || applied != 0 {
		t.Fatalf("自测推进活性或配置: %d %d", last, applied)
	}
	for query, target := range map[string]*int{"SELECT count(*) FROM probe_realtime": &probes, "SELECT count(*) FROM events WHERE event_id='unrelated-invalid-event'": &events, "SELECT count(*) FROM node_regions": &ips} {
		if err := p.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if probes != 0 || events != 0 || ips != 0 {
		t.Fatalf("自测消费其他载荷: %d %d %d", probes, events, ips)
	}
	for i, kind := range []string{"heartbeat", "probe", "events", "config", "rotation"} {
		report = &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "instance", SelfCheckResults: [][]byte{selfCheckWire(t, uint64(i+2), now)}, ProbeOnly: kind == "probe", EventsOnly: kind == "events", RotationOnly: kind == "rotation"}
		if kind == "rotation" {
			report.Rotation = &controlpb.RotationProgress{}
		}
		if kind == "config" {
			report.AttemptedRevision = 7
			report.AppliedRevision = 7
			report.AppliedDigest = "expected"
		}
		response, err = p.acceptReport(ctx, "n", digest[:], session, report)
		if err != nil {
			t.Fatal(kind, err)
		}
		if len(response.AcknowledgedSelfCheckIds) != 0 {
			t.Fatal(kind, "消费自测确认")
		}
	}
	var count int
	if err := p.db.QueryRow("SELECT count(*) FROM self_check_rounds").Scan(&count); err != nil || count != 1 {
		t.Fatalf("无关包消费了自测: %d %v", count, err)
	}
}

// 持久化故障物理验证：判定事务提交后事件写失败，重试/重启仍恰好投递同一待办。
func TestSelfCheckNotificationOutboxSurvivesEventWriteFailure(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	ctx := context.Background()
	now := p.deps.Clock.Now()
	for _, query := range []string{"INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0)", "INSERT INTO node_self_check(node_id,enabled) VALUES('n',1)", "CREATE TRIGGER reject_self_check_event BEFORE INSERT ON events WHEN NEW.type='node_blocked' BEGIN SELECT RAISE(ABORT,'test failure'); END"} {
		if _, err := p.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for seq := uint64(1); seq <= 2; seq++ {
		if _, err := p.acceptSelfChecks(ctx, "n", [][]byte{selfCheckWire(t, seq, now)}); err != nil {
			t.Fatal(err)
		}
	}
	wire := selfCheckWire(t, 3, now)
	if _, err := p.acceptSelfChecks(ctx, "n", [][]byte{wire}); err == nil {
		t.Fatal("故障事件写入没有阻止确认")
	}
	if _, err := p.db.Exec("DROP TRIGGER reject_self_check_event"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openDiskPanel(t, dir)
	for range 2 {
		if _, err := p.acceptSelfChecks(ctx, "n", [][]byte{wire}); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"SELECT count(*) FROM events WHERE type='node_blocked'", "SELECT count(*) FROM telegram_deliveries WHERE incident_id LIKE 'self-check:%'"} {
		var count int
		if err := p.db.QueryRow(query).Scan(&count); err != nil || count != 1 {
			t.Fatalf("持久待办丢失或重复: %s=%d %v", query, count, err)
		}
	}
}
