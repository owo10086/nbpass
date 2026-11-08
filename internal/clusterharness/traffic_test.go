package clusterharness_test

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"nbpass/internal/clusterharness"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
)

func ruleTraffic(t *testing.T, c *clusterharness.Client, id string) map[string]any {
	t.Helper()
	for _, item := range c.Do("GET", "/api/rules", nil, 200)["rules"].([]any) {
		r := item.(map[string]any)
		if r["id"] == id {
			v, ok := r["traffic"].(map[string]any)
			if !ok {
				t.Fatalf("规则缺少流量统计: %v", r)
			}
			return v
		}
	}
	t.Fatal("规则不存在")
	return nil
}

func awaitTraffic(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, up, down float64, connections any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		last = ruleTraffic(t, c, id)
		if last["upload_bytes"] == up && last["download_bytes"] == down && last["active_connections"] == connections {
			return
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("流量统计期望 %v/%v/%v，得到 %v", up, down, connections, last)
}

func TestTrafficDirectLifecycleAndOfflineRestart(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "计量入口")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	port := freePort(t)
	body := directBody(entryGroup(c, id)["id"].(string), port, "127.0.0.1", echoTarget(t, "A:"))
	rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	rev := awaitRevision(t, c, id, 0)
	first := tcpExchange(t, port, "A:")
	awaitTraffic(t, h, c, rid, 4, 6, float64(1))
	first.Close()
	awaitTraffic(t, h, c, rid, 4, 6, float64(0))
	body["target_port"] = echoTarget(t, "LONG:")
	c.Do("PUT", "/api/rules/"+rid, body, 200)
	awaitRevision(t, c, id, rev)
	second := tcpExchange(t, port, "LONG:")
	awaitTraffic(t, h, c, rid, 8, 15, float64(1))
	n.Disconnect()
	awaitNode(t, c, id, func(r map[string]any) bool { return r["connected"] == false })
	third := tcpExchange(t, port, "LONG:")
	h.Clock.Advance(5 * time.Second)
	second.Close()
	third.Close()
	// 离线停止也必须把最后一轮计量落盘；新进程通过当前控制会话补报旧实例。
	n.Restart()
	n.Restore()
	awaitTraffic(t, h, c, rid, 12, 24, float64(0))
	fourth := tcpExchange(t, port, "LONG:")
	awaitTraffic(t, h, c, rid, 16, 33, float64(1))
	fourth.Close()
	awaitTraffic(t, h, c, rid, 16, 33, float64(0))
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	awaitTraffic(t, h, c, rid, 16, 33, float64(0))
}

func TestTrafficTunnelCountsOnlyEntry(t *testing.T) {
	h, c, _, _, _, port, body := tunnelSetup(t)
	rid := body["id"].(string)
	a := tcpExchange(t, port, "T:")
	b := tcpExchange(t, port, "T:")
	awaitTraffic(t, h, c, rid, 8, 12, float64(2))
	a.Close()
	b.Close()
	awaitTraffic(t, h, c, rid, 8, 12, float64(0))
}

func TestTrafficOfflineBackfillSurvivesMovingRuleToAnotherEntryGroup(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "原入口"), addNode(c, "新入口")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	port := freePort(t)
	body := directBody(entryGroup(c, id)["id"].(string), port, "127.0.0.1", echoTarget(t, "A:"))
	rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRevision(t, c, id, 0)
	tcpExchange(t, port, "A:").Close()
	awaitTraffic(t, h, c, rid, 4, 6, float64(0))
	n.Disconnect()
	awaitNode(t, c, id, func(r map[string]any) bool { return r["connected"] == false })
	tcpExchange(t, port, "A:").Close()
	body["entry_group_id"] = entryGroup(c, b["id"].(string))["id"]
	c.Do("PUT", "/api/rules/"+rid, body, 200)
	// 已撤销的离线缓存仍可转发；补报应归原规则，不能因当前入口组改变被丢弃。
	tcpExchange(t, port, "A:").Close()
	n.Restart()
	n.Restore()
	awaitTraffic(t, h, c, rid, 12, 18, nil)
}

// 真实 TLS/gRPC 注入重复、乱序及混合载荷，业务断言仍只读取面板 HTTP API。
func TestTrafficReportsDeduplicateAndIsolateAllControlState(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "独立流量报告")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string), func(config *node.Config, _ *node.Dependencies) { config.EnrollOnly = true })
	awaitEnrollment(t, n, false)
	store, err := n.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := x509.NewCertPool()
	root.AddCert(h.Control.Certificate())
	conn, err := grpc.NewClient(h.Control.Listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: root, MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+credential.Token)
	stream, err := controlpb.NewControlServiceClient(conn).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	send := func(report *controlpb.NodeReport) *controlpb.PanelMessage {
		t.Helper()
		if err := stream.Send(report); err != nil {
			t.Fatal(err)
		}
		for {
			m, err := stream.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if !report.TrafficOnly || m.TrafficOnly {
				return m
			}
		}
	}
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "traffic-test", SoftwareVersion: "original", PublicIpv4: "223.5.5.5", Probe: &controlpb.ProbeSample{Sequence: 1, Values: map[string]float64{"cpu_percent": 10}}}
	send(report)
	body := directBody(entryGroup(c, id)["id"].(string), freePort(t), "127.0.0.1", 443)
	rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	before := nodeRow(t, c, id)
	events := len(eventRows(c, "?node_id="+id))
	h.Clock.Advance(5 * time.Second)
	sample := &controlpb.RuleTraffic{ProcessInstance: report.ProcessInstance, Sequence: 1, RuleId: rid, BucketUnix: h.Clock.Now().Truncate(time.Minute).Unix(), UploadBytes: 20, DownloadBytes: 30, ActiveConnections: 2, SampledAt: h.Clock.Now().Format(time.RFC3339Nano)}
	report.TrafficOnly = true
	report.Traffic = []*controlpb.RuleTraffic{sample}
	report.Probe = &controlpb.ProbeSample{Sequence: 2, Values: map[string]float64{"cpu_percent": 90}}
	report.Events = []*controlpb.Event{{EventId: "invalid-event"}}
	report.PublicIpv4 = "非法且与流量无关的公网地址"
	report.SoftwareVersion = "poison"
	report.AppliedRevision = uint64(before["desired_revision"].(float64))
	report.AppliedDigest = "poison"
	report.AttemptedRevision = report.AppliedRevision
	report.ApplyError = "poison"
	ack := send(report)
	if !ack.TrafficOnly || ack.Snapshot != nil || len(ack.AcknowledgedEventIds) != 0 || len(ack.AcknowledgedTraffic) != 1 {
		t.Fatalf("确认混入其他状态: %v", ack)
	}
	awaitTraffic(t, h, c, rid, 20, 30, float64(2))
	for _, key := range []string{"last_seen_at", "public_ipv4", "domestic_checker", "software_version", "applied_revision", "attempted_revision", "apply_error", "applied_digest"} {
		if row := nodeRow(t, c, id); row[key] != before[key] {
			t.Fatalf("流量修改了 %s: %v -> %v", key, before[key], row[key])
		}
	}
	if len(eventRows(c, "?node_id="+id)) != events {
		t.Fatal("流量推进了事件状态")
	}
	if rt := c.Do("GET", probePath(id), nil, 200)["realtime"].(map[string]any); rt["cpu_percent"] != float64(10) {
		t.Fatal("流量推进了探针状态")
	}
	// 同序号重投、同序号修改数值、较新序号回退均不能重复或负计数。
	send(report)
	sample.UploadBytes = 200
	send(report)
	sample.Sequence = 2
	sample.UploadBytes = 1
	send(report)
	awaitTraffic(t, h, c, rid, 20, 30, float64(2))
	// 新进程从零累计，旧实例补报仍归旧实例；其连接数不能覆盖当前进程。
	sample.ProcessInstance = "old-offline-process"
	sample.Sequence = 1
	sample.UploadBytes = 4
	sample.DownloadBytes = 6
	sample.ActiveConnections = 99
	send(report)
	awaitTraffic(t, h, c, rid, 24, 36, float64(2))
	sample.ProcessInstance = report.ProcessInstance
	sample.Sequence = 3
	sample.UploadBytes = 25
	sample.DownloadBytes = 40
	send(report)
	awaitTraffic(t, h, c, rid, 29, 46, float64(99))
	// 过期桶即使通过新样本时间包装也不能补入，新桶仍可正常计数。
	sample.BucketUnix = h.Clock.Now().Add(-25 * time.Hour).Truncate(time.Minute).Unix()
	sample.Sequence = 4
	sample.UploadBytes = 1000
	sample.DownloadBytes = 1000
	send(report)
	awaitTraffic(t, h, c, rid, 29, 46, float64(99))
	// 无心跳，连续流量报告也不能延长 90 秒活性。
	for range 4 {
		h.Clock.Advance(20 * time.Second)
		sample.SampledAt = h.Clock.Now().Format(time.RFC3339Nano)
		sample.Sequence++
		send(report)
	}
	h.Clock.Advance(5 * time.Second)
	awaitNode(t, c, id, func(r map[string]any) bool { return r["connected"] == false })
}

type retryTrafficStore struct {
	node.FileStore
	fail     atomic.Bool
	rejected atomic.Int64
}

func (s *retryTrafficStore) SaveTraffic(ctx context.Context, data []byte) error {
	if s.fail.Load() {
		s.rejected.Add(1)
		return errors.New("模拟流量检查点落盘失败")
	}
	return s.FileStore.SaveTraffic(ctx, data)
}

func TestTrafficCheckpointFailureReplayAndTwentyFourHourExpiry(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "流量持久补报")
	id := created["id"].(string)
	store := &retryTrafficStore{FileStore: node.FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}}
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) { d.Store = store })
	awaitNode(t, c, id, connected)
	port := freePort(t)
	rid := c.Do("POST", "/api/rules", directBody(entryGroup(c, id)["id"].(string), port, "127.0.0.1", echoTarget(t, "A:")), 201)["id"].(string)
	awaitRevision(t, c, id, 0)
	a := tcpExchange(t, port, "A:")
	awaitTraffic(t, h, c, rid, 4, 6, float64(1))
	// 阻止将内存ACK写入下一检查点，重启后同实例/序号再次交付，面板仍只累计一次。
	store.fail.Store(true)
	a.Close()
	n.Restart()
	if store.rejected.Load() == 0 {
		t.Fatal("未触发检查点落盘失败")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	store.fail.Store(false)
	awaitTraffic(t, h, c, rid, 4, 6, float64(0))
	n.Disconnect()
	awaitNode(t, c, id, func(r map[string]any) bool { return r["connected"] == false })
	tcpExchange(t, port, "A:").Close()
	h.Clock.Advance(25 * time.Hour)
	login(c)
	// 过期的离线业务字节不再补入，旧桶也不会混入新桶的增量。
	store.fail.Store(false)
	n.Restore()
	n.Restart()
	awaitTraffic(t, h, c, rid, 4, 6, float64(0))
	tcpExchange(t, port, "A:")
	awaitTraffic(t, h, c, rid, 8, 12, float64(1))
}
