package node

import (
	"context"
	"crypto/rand"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChainGrantReportErrorsStayBounded(t *testing.T) {
	report := &controlpb.NodeReport{AttemptedRevision: 5, AttemptedDigest: "digest", RevocationsApplied: true}
	for i := 0; i < 80; i++ {
		report.TunnelGrantResults = append(report.TunnelGrantResults, &controlpb.TunnelGrantApplyResult{EntryId: strings.Repeat("e", 43), RuleId: strings.Repeat("r", 43), Generation: 7, AppliedRevision: 5, ApplyError: strings.Repeat("错误", 2000)})
	}
	if err := limitConfigReport(report); err != nil {
		t.Fatal(err)
	}
	if proto.Size(report) > 64<<10 || len(report.TunnelGrantResults) != 80 {
		t.Fatal("lost results or over limit")
	}
	for _, result := range report.TunnelGrantResults {
		if result.ApplyError == "" || len(result.ApplyError) > 1024 || result.Generation != 7 {
			t.Fatal("lost identity/error")
		}
	}
}

func TestChainControlReport64KiBBoundary(t *testing.T) {
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		r := &controlpb.NodeReport{ApplyError: strings.Repeat("x", size-4)}
		if proto.Size(r) != size {
			t.Fatal(proto.Size(r), size)
		}
		err := limitConfigReport(r)
		if (err == nil) != (size == 64<<10) {
			t.Fatal(size, err)
		}
	}
}

type chainBoundaryServer struct {
	controlpb.UnimplementedControlServiceServer
}

func (chainBoundaryServer) Connect(stream grpc.BidiStreamingServer[controlpb.NodeReport, controlpb.PanelMessage]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return stream.Send(&controlpb.PanelMessage{DesiredRevision: 1})
}
func TestChainControlGRPC64KiBBoundary(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(64<<10), grpc.MaxSendMsgSize(64<<10))
	controlpb.RegisterControlServiceServer(server, chainBoundaryServer{})
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := controlpb.NewControlServiceClient(conn)
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		stream, err := client.Connect(ctx)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		err = stream.Send(&controlpb.NodeReport{ApplyError: strings.Repeat("x", size-4)})
		if err == nil {
			_, err = stream.Recv()
		}
		cancel()
		if size == 64<<10 && err != nil || size > 64<<10 && status.Code(err) != codes.ResourceExhausted {
			t.Fatal(size, err)
		}
	}
}

// The first heartbeat and the complete cached attempted report share a stream,
// but must never share the 64 KiB payload budget or acknowledge unsent events.
func TestChainCachedReportReconnectFramesAndBoundedEvents(t *testing.T) {
	clock := &exchangeClock{}
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	events, err := OpenEventQueue(context.Background(), store, clock, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 17; i++ {
		if err = events.Record(context.Background(), "chain_flow", strings.Repeat("e", 2048), 9); err != nil {
			t.Fatal(err)
		}
	}
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "same-process", AttemptedRevision: 9, AttemptedDigest: strings.Repeat("d", 64), RevocationsApplied: true, ApplyError: strings.Repeat("x", 1024)}
	for i := 0; i < 80; i++ {
		report.TunnelGrantResults = append(report.TunnelGrantResults, &controlpb.TunnelGrantApplyResult{EntryId: fmt.Sprintf("entry-%043d", i), RuleId: fmt.Sprintf("rule-%043d", i), Generation: 4, ApplyError: strings.Repeat("失败", 1000)})
	}
	engine := forwarding.NewEngine(forwarding.Dependencies{})
	defer engine.Close()
	for attempt := 0; attempt < 3; attempt++ {
		if attempt == 2 {
			report.ProcessInstance = "new-process"
		}
		stream := &interleavedStream{reports: make(chan *controlpb.NodeReport, 16), messages: make(chan *controlpb.PanelMessage, 16)}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		rotations := &rotationRunner{state: rotationJournal{Results: map[string]*controlpb.RotationProgress{}, AttemptedAt: map[string]int64{}}}
		for i := 0; i < 8; i++ {
			id := fmt.Sprint(i)
			rotations.state.Results[id] = &controlpb.RotationProgress{ActionId: id, RecheckRounds: [][]byte{[]byte(strings.Repeat("r", 2048))}}
			rotations.state.AttemptedAt[id] = clock.Now().UnixNano()
		}
		done := make(chan error, 1)
		go func() {
			done <- exchange(ctx, interleavedClient{stream: stream}, "token", report, Dependencies{Clock: clock, Events: events, rotations: rotations, PublicIPv4: func(context.Context) (string, error) { return "", nil }}, make(chan probeUpdate), engine)
		}()
		receive := func() *controlpb.NodeReport {
			t.Helper()
			select {
			case r := <-stream.reports:
				return r
			case e := <-done:
				t.Fatalf("exchange ended: %v", e)
			case <-ctx.Done():
				t.Fatal("frame missing")
			}
			return nil
		}
		hb, cfg := receive(), receive()
		if len(hb.TunnelGrantResults) != 0 || hb.AttemptedRevision != 0 || hb.RevocationsApplied || len(hb.Events) != 16 || hb.Recovery == nil {
			t.Fatal("heartbeat confused with durable attempted report", hb)
		}
		if len(cfg.TunnelGrantResults) != 80 || cfg.AttemptedRevision != 9 || !cfg.RevocationsApplied || len(cfg.Events) != 0 || cfg.Recovery != nil {
			t.Fatal("cached complete grant report lost on reconnect")
		}
		for _, r := range []*controlpb.NodeReport{hb, cfg} {
			if proto.Size(r) > 64<<10 {
				t.Fatal("composed report exceeds wire budget", proto.Size(r))
			}
		}
		// No ACK on the first two connections: the same persistent event batch and
		// complete grant list must be retried. A fabricated ACK cannot erase data.
		if attempt == 2 {
			stream.messages <- &controlpb.PanelMessage{ProtocolVersion: 1, AcknowledgedEventIds: []string{"never-sent"}}
			select {
			case e := <-done:
				if e == nil || !strings.Contains(e.Error(), "在途批次") {
					t.Fatal(e)
				}
			case <-ctx.Done():
				t.Fatal("unsent event ACK accepted")
			}
		} else {
			cancel()
			<-done
		}
		cancel()
	}
	batch, err := events.Batch(context.Background())
	if err != nil || len(batch) != 16 {
		t.Fatal("unacknowledged event batch lost", len(batch), err)
	}
}
