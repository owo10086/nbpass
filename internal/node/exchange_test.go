package node

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"nbpass/internal/platform"
	"nbpass/internal/timing"
)

type exchangeClock struct{ seconds atomic.Int64 }

func (c *exchangeClock) Now() time.Time {
	return time.Now().Add(time.Duration(c.seconds.Load()) * time.Second)
}
func (c *exchangeClock) NewTimer(d time.Duration) timing.Timer {
	return platform.RealClock{}.NewTimer(d)
}

type exchangeClient struct {
	controlpb.ControlServiceClient
	stream *exchangeStream
}

func (c exchangeClient) Connect(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[controlpb.NodeReport, controlpb.PanelMessage], error) {
	c.stream.ctx = ctx
	return c.stream, nil
}

type exchangeStream struct {
	grpc.ClientStream
	ctx     context.Context
	reports chan *controlpb.NodeReport
	clock   *exchangeClock
	updates chan probeUpdate
	delay   int64
	sent    int
}

func (s *exchangeStream) Send(r *controlpb.NodeReport) error {
	select {
	case s.reports <- r:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *exchangeStream) Recv() (*controlpb.PanelMessage, error) {
	select {
	case r := <-s.reports:
		s.sent++
		if s.sent == 4 && s.delay > 0 && (r.EventsOnly || r.ProbeOnly) {
			return nil, errHeartbeatStarved
		}
		if s.sent == 2 && s.updates != nil && !r.ProbeOnly {
			return nil, errProbeStarved
		}
		if s.sent == 4 && s.delay > 0 || s.sent == 2 && s.updates != nil {
			return nil, context.Canceled
		}
		s.clock.seconds.Add(s.delay)
		if s.sent == 1 && s.updates != nil {
			s.updates <- probeUpdate{at: s.clock.Now(), sample: &controlpb.ProbeSample{Sequence: 1}}
		}
		m := &controlpb.PanelMessage{ProtocolVersion: 1}
		for _, e := range r.Events {
			m.AcknowledgedEventIds = append(m.AcknowledgedEventIds, e.EventId)
		}
		return m, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

var errHeartbeatStarved = errors.New("满批补报阻塞了到期心跳")
var errProbeStarved = errors.New("满批补报阻塞了待发探针")

// 慢 ACK 跨过心跳边界或已有新探针时，事件队列不能独占控制流。
func TestEventBackfillYieldsToHeartbeatAndProbe(t *testing.T) {
	for _, kind := range []string{"heartbeat", "probe"} {
		t.Run(kind, func(t *testing.T) {
			clock := &exchangeClock{}
			store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
			queue, err := OpenEventQueue(context.Background(), store, clock, rand.Reader, EventLimits{})
			if err != nil {
				t.Fatal(err)
			}
			for range 80 {
				if err := queue.Record(context.Background(), "sync_failed", "安全摘要", 1); err != nil {
					t.Fatal(err)
				}
			}
			stream := &exchangeStream{reports: make(chan *controlpb.NodeReport, 1), clock: clock}
			updates := make(chan probeUpdate, 1)
			if kind == "heartbeat" {
				stream.delay = 4
			} else {
				stream.updates = updates
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err = exchange(ctx, exchangeClient{stream: stream}, "token", &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "test"}, Dependencies{Clock: clock, Events: queue, PublicIPv4: func(context.Context) (string, error) { return "", nil }}, updates, forwarding.NewEngine(forwarding.Dependencies{}))
			if err != context.Canceled {
				t.Fatalf("控制流未让出独立报告: %v", err)
			}
		})
	}
}

// 主动配置先于事件 ACK 到达时，配置确认与下一批补报仍各自收敛。
func TestPushedSnapshotDoesNotConsumePendingEventAcknowledgement(t *testing.T) {
	clock := &exchangeClock{}
	queue, err := OpenEventQueue(context.Background(), FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}, clock, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if err := queue.Record(context.Background(), "sync_failed", "待补报", 999); err != nil {
			t.Fatal(err)
		}
	}
	stream := &interleavedStream{reports: make(chan *controlpb.NodeReport, 4), messages: make(chan *controlpb.PanelMessage, 4)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	engine := forwarding.NewEngine(forwarding.Dependencies{})
	defer engine.Close()
	done := make(chan error, 1)
	go func() {
		done <- exchange(ctx, interleavedClient{stream: stream}, "token", &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "test"}, Dependencies{Clock: clock, Events: queue, PublicIPv4: func(context.Context) (string, error) { return "", nil }}, nil, engine)
	}()
	receive := func() *controlpb.NodeReport {
		t.Helper()
		select {
		case r := <-stream.reports:
			return r
		case err := <-done:
			t.Fatalf("控制流提前退出: %v", err)
		case <-ctx.Done():
			t.Fatal("事件与配置未独立收敛")
		}
		return nil
	}
	first := receive()
	if len(first.Events) != 16 {
		t.Fatalf("首批事件缺失: %v", first)
	}
	snapshot := &controlpb.ConfigSnapshot{Revision: 1}
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	stream.messages <- &controlpb.PanelMessage{ProtocolVersion: 1, DesiredRevision: 1, Snapshot: snapshot}
	confirmed := receive()
	if confirmed.AttemptedRevision != 1 || confirmed.AppliedRevision != 1 || len(confirmed.Events) != 0 || confirmed.ProbeOnly || confirmed.EventsOnly {
		t.Fatalf("配置确认混入其他报告: %v", confirmed)
	}
	seen := map[string]bool{}
	for batchIndex, batchSize := range []int{16, 16, 8} {
		batch := first
		if batchIndex > 0 {
			batch = receive()
			if !batch.EventsOnly || batch.AppliedRevision != 0 || batch.AttemptedRevision != 0 {
				t.Fatalf("事件补报混入配置确认: %v", batch)
			}
		}
		if len(batch.Events) != batchSize {
			t.Fatalf("补报数量: got %d want %d", len(batch.Events), batchSize)
		}
		ack := &controlpb.PanelMessage{ProtocolVersion: 1, DesiredRevision: 1}
		for _, event := range batch.Events {
			if seen[event.EventId] {
				t.Fatal("配置推送导致重复批次")
			}
			seen[event.EventId] = true
			ack.AcknowledgedEventIds = append(ack.AcknowledgedEventIds, event.EventId)
		}
		stream.messages <- ack
	}
	for {
		remaining, err := queue.Batch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(remaining) == 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("事件确认失败: %v", err)
		case <-ctx.Done():
			t.Fatal("事件确认未持久化")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("控制流取消失败: %v", err)
	}
}

type interleavedClient struct {
	controlpb.ControlServiceClient
	stream *interleavedStream
}

// 重启保留的未满在途批次确认后，剩余事件不能等下一个心跳才补报。
func TestRestartedEventBackfillDrainsAfterShortPendingBatch(t *testing.T) {
	clock := &exchangeClock{}
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	queue, err := OpenEventQueue(context.Background(), store, clock, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Record(context.Background(), "node_started", "上一进程启动", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Batch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := queue.Record(context.Background(), "sync_failed", "等待补报", 3); err != nil {
		t.Fatal(err)
	}
	queue, err = OpenEventQueue(context.Background(), store, clock, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	stream := &interleavedStream{reports: make(chan *controlpb.NodeReport, 4), messages: make(chan *controlpb.PanelMessage, 4)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	engine := forwarding.NewEngine(forwarding.Dependencies{})
	defer engine.Close()
	done := make(chan error, 1)
	go func() {
		done <- exchange(ctx, interleavedClient{stream: stream}, "token", &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "restarted"}, Dependencies{Clock: clock, Events: queue, PublicIPv4: func(context.Context) (string, error) { return "", nil }}, nil, engine)
	}()
	receive := func() *controlpb.NodeReport {
		t.Helper()
		select {
		case r := <-stream.reports:
			return r
		case err := <-done:
			t.Fatalf("事件补报提前结束: %v", err)
		case <-ctx.Done():
			t.Fatal("未满批次确认后剩余事件未继续补报")
		}
		return nil
	}
	first := receive()
	if len(first.Events) != 1 || first.Events[0].Type != "node_started" {
		t.Fatalf("未保留旧在途批次: %v", first)
	}
	stream.messages <- &controlpb.PanelMessage{ProtocolVersion: 1, AcknowledgedEventIds: []string{first.Events[0].EventId}}
	if r := receive(); !r.EventsOnly || len(r.Events) != 1 || r.Events[0].Type != "sync_failed" {
		t.Fatalf("未继续补报排队事件: %v", r)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("取消失败: %v", err)
	}
}

func (c interleavedClient) Connect(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[controlpb.NodeReport, controlpb.PanelMessage], error) {
	c.stream.ctx = ctx
	return c.stream, nil
}

type interleavedStream struct {
	grpc.ClientStream
	ctx      context.Context
	reports  chan *controlpb.NodeReport
	messages chan *controlpb.PanelMessage
}

func (s *interleavedStream) Send(r *controlpb.NodeReport) error {
	select {
	case s.reports <- r:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *interleavedStream) Recv() (*controlpb.PanelMessage, error) {
	select {
	case m := <-s.messages:
		return m, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
