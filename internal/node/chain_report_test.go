package node

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"net"
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
