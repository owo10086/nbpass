package clusterharness_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"nbpass/internal/controlpb"
	"nbpass/internal/node"
	"nbpass/internal/probe"
)

// 合并后若每份探针都查询公网 IP，查询停滞会阻断 5 秒独立探针。
func TestProbeRemainsIndependentOfPublicIPv4Queries(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "地区与探针")
	id := created["id"].(string)
	f := &probeFixture{values: probe.Values{"cpu_percent": 20}}
	var blocked atomic.Bool
	var ip atomic.Value
	ip.Store("223.5.5.5")
	queryStarted := make(chan struct{}, 1)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.Probe = f
		deps.PublicIPv4 = func(ctx context.Context) (string, error) {
			if blocked.Load() {
				select {
				case queryStarted <- struct{}{}:
				default:
				}
				<-ctx.Done()
				return "", ctx.Err()
			}
			return ip.Load().(string), nil
		}
	})
	awaitUptime(t, c, id, 0)
	before := nodeRow(t, c, id)
	if before["public_ipv4"] != "223.5.5.5" || before["domestic_checker"] != true {
		t.Fatalf("初始地区未上报: %v", before)
	}
	blocked.Store(true)
	f.set(probe.Values{"cpu_percent": 80}, nil)
	h.Clock.Advance(5 * time.Second)
	view := awaitUptime(t, c, id, 5)
	if view["realtime"].(map[string]any)["cpu_percent"] != float64(80) {
		t.Fatal("独立探针未更新")
	}
	if row := nodeRow(t, c, id); row["last_seen_at"] != before["last_seen_at"] || row["public_ipv4"] != "223.5.5.5" {
		t.Fatalf("探针推进了活性或 IP: %v", row)
	}
	h.Clock.Advance(5 * time.Second)
	select {
	case <-queryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("10 秒活性未查询公网 IP")
	}
	h.Clock.Advance(5 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool {
		return row["last_seen_at"] == h.Clock.Now().Format(time.RFC3339Nano)
	})
	awaitUptime(t, c, id, 15)
	if nodeRow(t, c, id)["public_ipv4"] != "223.5.5.5" {
		t.Fatal("查询超时清除了最后有效 IP")
	}
	blocked.Store(false)
	ip.Store("8.8.8.8")
	h.Clock.Advance(5 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool {
		return row["public_ipv4"] == "8.8.8.8" && row["domestic_checker"] == false && row["last_seen_at"] == h.Clock.Now().Format(time.RFC3339Nano)
	})
	awaitUptime(t, c, id, 20)
}

// 使用真实 TLS/gRPC 发送携带其他 IP 的探针；所有业务结果仅经面板 HTTP API 断言。
func TestProbeOnlyReportDoesNotUpdateRegionOrLiveness(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "独立探针报告")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string), func(config *node.Config, _ *node.Dependencies) {
		config.EnrollOnly = true
	})
	awaitEnrollment(t, n, false)
	store, err := n.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(h.Control.Certificate())
	conn, err := grpc.NewClient(h.Control.Listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+credential.Token)
	stream, err := controlpb.NewControlServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	send := func(report *controlpb.NodeReport) {
		t.Helper()
		if err := stream.Send(report); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "probe-region-test", SoftwareVersion: "heartbeat", PublicIpv4: "223.5.5.5"}
	send(report)
	before := nodeRow(t, c, id)
	h.Clock.Advance(5 * time.Second)
	report.ProbeOnly, report.PublicIpv4, report.SoftwareVersion, report.AppliedRevision = true, "8.8.8.8", "probe-only", 99
	report.Probe = &controlpb.ProbeSample{Sequence: 1, Values: map[string]float64{"cpu_percent": 80}}
	send(report)
	row := nodeRow(t, c, id)
	for _, key := range []string{"last_seen_at", "public_ipv4", "domestic_checker", "software_version", "applied_revision"} {
		if row[key] != before[key] {
			t.Errorf("独立探针修改了 %s: %v -> %v", key, before[key], row[key])
		}
	}
	view := c.Do("GET", probePath(id), nil, 200)
	if view["fresh"] != true || view["realtime"].(map[string]any)["cpu_percent"] != float64(80) {
		t.Fatalf("独立探针未存储: %v", view["realtime"])
	}
	h.Clock.Advance(5 * time.Second)
	report.ProbeOnly, report.SoftwareVersion, report.AppliedRevision = false, "heartbeat", 0
	report.Probe = &controlpb.ProbeSample{Sequence: 2, Values: map[string]float64{"cpu_percent": 40}}
	send(report)
	row = nodeRow(t, c, id)
	if row["public_ipv4"] != "8.8.8.8" || row["domestic_checker"] != false || row["last_seen_at"] != h.Clock.Now().Format(time.RFC3339Nano) {
		t.Fatalf("带探针的活性报告未更新 IP/活性: %v", row)
	}
	view = c.Do("GET", probePath(id), nil, 200)
	if view["realtime"].(map[string]any)["cpu_percent"] != float64(40) {
		t.Fatal("带 IP 的活性报告丢失探针")
	}
}
