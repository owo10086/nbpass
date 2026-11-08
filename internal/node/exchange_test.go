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
			err = exchange(ctx, exchangeClient{stream: stream}, "token", &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "test"}, Dependencies{Clock: clock, Events: queue, PublicIPv4: func(context.Context) (string, error) { return "", nil }}, updates)
			if err != context.Canceled {
				t.Fatalf("控制流未让出独立报告: %v", err)
			}
		})
	}
}
