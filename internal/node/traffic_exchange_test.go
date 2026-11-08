package node

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 只验证发送/确认的物理隔离：事件 ACK 未到、配置确认无回包、流量 ACK 夹带其他确认。
func TestTrafficExchangeDoesNotSharePendingAcknowledgements(t *testing.T) {
	clock := &exchangeClock{}
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	events, err := OpenEventQueue(context.Background(), store, clock, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := events.Record(context.Background(), "sync_failed", "安全摘要", 1); err != nil {
			t.Fatal(err)
		}
	}
	queue, err := OpenTrafficQueue(context.Background(), store, clock)
	if err != nil {
		t.Fatal(err)
	}
	sample := &controlpb.RuleTraffic{RuleId: "rule", BucketUnix: clock.Now().Truncate(time.Minute).Unix(), UploadBytes: 4, DownloadBytes: 6, SampledAt: clock.Now().Format(time.RFC3339Nano)}
	if err := queue.Capture(context.Background(), "current", []*controlpb.RuleTraffic{sample}); err != nil {
		t.Fatal(err)
	}
	stream := &interleavedStream{reports: make(chan *controlpb.NodeReport, 16), messages: make(chan *controlpb.PanelMessage, 16)}
	updates := make(chan probeUpdate, 1)
	trafficUpdates := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	engine := forwarding.NewEngine(forwarding.Dependencies{})
	defer engine.Close()
	done := make(chan error, 1)
	go func() {
		done <- exchange(ctx, interleavedClient{stream: stream}, "token", &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current"}, Dependencies{Clock: clock, Events: events, Traffic: queue, trafficUpdates: trafficUpdates, PublicIPv4: func(context.Context) (string, error) { return "", nil }}, updates, engine)
	}()
	receive := func() *controlpb.NodeReport {
		t.Helper()
		select {
		case r := <-stream.reports:
			return r
		case err := <-done:
			t.Fatalf("控制流提前结束: %v", err)
		case <-ctx.Done():
			t.Fatal("独立报告被阻塞")
		}
		return nil
	}
	heartbeat := receive()
	traffic := receive()
	if len(heartbeat.Events) != 16 || !traffic.TrafficOnly || len(traffic.Traffic) != 1 || traffic.Probe != nil || len(traffic.Events) != 0 || traffic.AppliedRevision != 0 {
		t.Fatalf("流量受事件在途状态阻塞或混入载荷: %v", traffic)
	}
	// 无流量确认也必须能应用配置、发送配置确认和独立探针。
	snapshot := &controlpb.ConfigSnapshot{Revision: 1}
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	stream.messages <- &controlpb.PanelMessage{ProtocolVersion: 1, DesiredRevision: 1, Snapshot: snapshot}
	if r := receive(); r.AttemptedRevision != 1 || r.TrafficOnly {
		t.Fatalf("配置确认被流量阻塞: %v", r)
	}
	updates <- probeUpdate{at: clock.Now(), sample: &controlpb.ProbeSample{Sequence: 1}}
	if r := receive(); !r.ProbeOnly || len(r.Traffic) > 0 {
		t.Fatalf("探针被流量阻塞: %v", r)
	}
	// 流量 ACK 夹带事件 ACK 和新配置，必须仅消费自身字段。
	ack := &controlpb.PanelMessage{ProtocolVersion: 1, TrafficOnly: true, Snapshot: &controlpb.ConfigSnapshot{Revision: 999}}
	for _, s := range traffic.Traffic {
		ack.AcknowledgedTraffic = append(ack.AcknowledgedTraffic, &controlpb.TrafficAck{ProcessInstance: s.ProcessInstance, Sequence: s.Sequence})
	}
	for _, e := range heartbeat.Events {
		ack.AcknowledgedEventIds = append(ack.AcknowledgedEventIds, e.EventId)
	}
	stream.messages <- ack
	for len(queue.Batch()) != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("流量 ACK 未处理")
		case <-time.After(time.Millisecond):
		}
	}
	if pending, err := events.Batch(context.Background()); err != nil || len(pending) != 16 {
		t.Fatalf("流量 ACK 消费了事件在途状态: %v %v", pending, err)
	}
	sample.UploadBytes = 8
	sample.SampledAt = clock.Now().Format(time.RFC3339Nano)
	if err := queue.Capture(context.Background(), "current", []*controlpb.RuleTraffic{sample}); err != nil {
		t.Fatal(err)
	}
	trafficUpdates <- struct{}{}
	secondTraffic := receive()
	if !secondTraffic.TrafficOnly || secondTraffic.Traffic[0].UploadBytes != 8 {
		t.Fatalf("第二轮流量仍受其他 ACK 阻塞: %v", secondTraffic)
	}
	// 超时重传可能使旧 ACK 迟到或重复，不能因此重置整个控制连接。
	stream.messages <- ack
	secondAck := &controlpb.PanelMessage{ProtocolVersion: 1, TrafficOnly: true}
	for _, s := range secondTraffic.Traffic {
		secondAck.AcknowledgedTraffic = append(secondAck.AcknowledgedTraffic, &controlpb.TrafficAck{ProcessInstance: s.ProcessInstance, Sequence: s.Sequence})
	}
	stream.messages <- secondAck
	for len(queue.Batch()) != 0 {
		select {
		case err := <-done:
			t.Fatalf("迟到流量 ACK 使控制流退出: %v", err)
		case <-ctx.Done():
			t.Fatal("新一轮流量未确认")
		case <-time.After(time.Millisecond):
		}
	}
	// 事件单独确认后才能继续发剩余事件。
	stream.messages <- &controlpb.PanelMessage{ProtocolVersion: 1, AcknowledgedEventIds: ack.AcknowledgedEventIds}
	if r := receive(); !r.EventsOnly || len(r.Events) != 4 {
		t.Fatalf("流量污染了事件补报: %v", r)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("取消失败: %v", err)
	}
}
