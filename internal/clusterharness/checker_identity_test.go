package clusterharness_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"nbpass/internal/checker"
	"nbpass/internal/clusterharness"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
)

// 仅构造攻击/重放流；真实被测节点及正常执行路径仍运行 node.Run，断言只经 HTTP。
func checkerWireNode(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client) (string, grpc.BidiStreamingClient[controlpb.NodeReport, controlpb.PanelMessage]) {
	t.Helper()
	created := addNode(c, "原始协议检测点")
	id := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	n := h.StartNode(created["installation_ticket"].(string), func(cfg *node.Config, _ *node.Dependencies) { cfg.EnrollOnly = true })
	awaitEnrollment(t, n, false)
	store, err := n.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	cred, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(h.Control.Certificate())
	conn, err := grpc.NewClient(h.Control.Listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+cred.Token)
	stream, err := controlpb.NewControlServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "checker-wire", PublicIpv4: "223.5.5.5"}); err != nil {
		t.Fatal(err)
	}
	if _, err = stream.Recv(); err != nil {
		t.Fatal(err)
	}
	awaitNode(t, c, id, connected)
	return id, stream
}
func nextCheckerTask(t *testing.T, stream grpc.BidiStreamingClient[controlpb.NodeReport, controlpb.PanelMessage]) checker.Task {
	t.Helper()
	for {
		m, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if len(m.CheckerTasks) > 0 {
			var task checker.Task
			if err = json.Unmarshal(m.CheckerTasks[0], &task); err != nil {
				t.Fatal(err)
			}
			return task
		}
	}
}
func checkerWireResult(task checker.Task, now time.Time) []byte {
	result := checker.Result{Task: task, StartedAt: task.IssuedAt, FinishedAt: now, Control: checker.Connection{Address: "203.0.113.9:443", Connected: true}, Target: &checker.Connection{Address: "8.8.8.8:18080", Connected: true}}
	data, _ := json.Marshal(result)
	return data
}
func TestCheckerRejectsImpersonationAndWrongTask(t *testing.T) {
	for _, attack := range []string{"other_checker", "wrong_ip", "unassigned"} {
		t.Run(attack, func(t *testing.T) {
			h, c := selfCheckBrowser(t)
			_, assigned := checkerWireNode(t, h, c)
			var ip atomic.Value
			ip.Store("8.8.8.8")
			id, _ := startUnknownTarget(t, h, c, &ip)
			task := nextCheckerTask(t, assigned)
			stream := assigned
			if attack == "other_checker" {
				_, stream = checkerWireNode(t, h, c)
			}
			if attack == "wrong_ip" {
				task.TargetIP = "1.1.1.1"
			}
			if attack == "unassigned" {
				task.ID = "00000000000000000000000000000000"
			}
			stream.Send(&controlpb.NodeReport{NodeId: task.CheckerID, ProtocolVersion: 1, ProcessInstance: "checker-wire", CheckerOnly: true, CheckerResults: [][]byte{checkerWireResult(task, h.Clock.Now())}})
			// 读取到拒绝后，再从 HTTP 检查该轮没有被篡改。
			for {
				_, err := stream.Recv()
				if err != nil {
					break
				}
			}
			v := c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
			r := v["rounds"].([]any)[0].(map[string]any)
			if r["final_result"] != "unknown" || v["consecutive_abnormal"] != float64(0) {
				t.Fatalf("冒充结果被采纳: %v", v)
			}
		})
	}
}

// 在恰好六十秒的边界，即使证据声称探测成功也作废；独立报告不能推进其他状态。
func TestCheckerDeadlineAndReportIsolation(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "有效且隔离", true: "截止边界"}[late], func(t *testing.T) {
			h, c := selfCheckBrowser(t)
			checkerID, stream := checkerWireNode(t, h, c)
			var ip atomic.Value
			ip.Store("8.8.8.8")
			id, _ := startUnknownTarget(t, h, c, &ip)
			task := nextCheckerTask(t, stream)
			before := nodeRow(t, c, checkerID)
			// 非 checker_only 的探针包中夹带结果，不能消费该载荷。
			stream.Send(&controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "checker-wire", ProbeOnly: true, CheckerResults: [][]byte{checkerWireResult(task, h.Clock.Now())}})
			if _, err := stream.Recv(); err != nil {
				t.Fatal(err)
			}
			if r := c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)["rounds"].([]any)[0].(map[string]any); r["final_result"] != "unknown" {
				t.Fatal("探针报告消费了补判载荷")
			}
			seconds := 5
			if late {
				seconds = 60
			}
			advanceCheckerClock(t, h, c, id, seconds/10*10)
			if !late {
				h.Clock.Advance(5 * time.Second)
			}
			data := checkerWireResult(task, task.IssuedAt)
			if late {
				data = checkerWireResult(task, h.Clock.Now())
			} // 真实执行器在截止取消后结束。
			stream.Send(&controlpb.NodeReport{NodeId: id, ProtocolVersion: 1, ProcessInstance: "checker-wire", CheckerOnly: true, CheckerResults: [][]byte{data}, PublicIpv4: "1.1.1.1", AppliedRevision: 99, AttemptedRevision: 99, AppliedDigest: "forged", Probe: &controlpb.ProbeSample{Sequence: 1, Values: map[string]float64{"cpu_percent": 80}}, Events: []*controlpb.Event{{EventId: "invalid-event"}}})
			acked := false
			for !acked {
				m, err := stream.Recv()
				if err != nil {
					t.Fatal(err)
				}
				acked = len(m.AcknowledgedCheckerIds) > 0
			}
			want := "normal"
			if late {
				want = "unknown"
			}
			awaitArbitration(t, c, id, want)
			after := nodeRow(t, c, checkerID)
			for _, key := range []string{"last_seen_at", "public_ipv4", "applied_revision", "software_version"} {
				if before[key] != after[key] {
					t.Fatalf("补判报告推进 %s: %v", key, after)
				}
			}
			if c.Do("GET", "/api/nodes/"+checkerID+"/probe", nil, 200)["realtime"] != nil {
				t.Fatal("消费了夹带探针")
			}
		})
	}
}

func TestCheckerEvidenceAllowsSmallClockSkew(t *testing.T) {
	h, c := selfCheckBrowser(t)
	_, stream := checkerWireNode(t, h, c)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	id, _ := startUnknownTarget(t, h, c, &ip)
	task := nextCheckerTask(t, stream)
	var result checker.Result
	json.Unmarshal(checkerWireResult(task, task.IssuedAt), &result)
	result.StartedAt = task.IssuedAt.Add(-time.Second)
	result.FinishedAt = result.StartedAt
	data, _ := json.Marshal(result)
	stream.Send(&controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "checker-wire", CheckerOnly: true, CheckerResults: [][]byte{data}})
	for {
		m, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if len(m.AcknowledgedCheckerIds) > 0 {
			break
		}
	}
	awaitArbitration(t, c, id, "normal")
}
