package clusterharness_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/probe"
)

// 丢失认证门禁、未接入节点被补成零或返回不完整时间轴时失败。
func TestProbePendingNodeHasMissingRealtimeAndMinuteHistory(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "尚未接入")
	path := "/api/nodes/" + created["id"].(string) + "/probe"
	h.Client().Do("GET", path, nil, 401)
	c.Do("GET", "/api/nodes/unknown/probe", nil, 404)
	view := c.Do("GET", path, nil, 200)
	if view["realtime"] != nil || view["fresh"] != false {
		t.Fatalf("未接入应缺测: %v", view)
	}
	points := view["minutes"].([]any)
	if len(points) != 1440 {
		t.Fatalf("最近 24 小时应有 1440 分钟: %d", len(points))
	}
	for _, item := range points {
		point := item.(map[string]any)
		if point["average"] != nil || point["maximum"] != nil || point["samples"] != float64(0) {
			t.Fatalf("缺测不能补零: %v", point)
		}
	}
}

type probeFixture struct {
	mu               sync.Mutex
	values           probe.Values
	err              error
	business, tunnel uint64
}

func (f *probeFixture) Collect(context.Context) (probe.Values, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := probe.Values{}
	for k, v := range f.values {
		values[k] = v
	}
	return values, f.err
}

func (f *probeFixture) Connections() (uint64, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.business, f.tunnel
}

func (f *probeFixture) set(values probe.Values, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values, f.err = values, err
}

func probePath(id string) string { return "/api/nodes/" + id + "/probe" }

func startProbeNode(h *clusterharness.Harness, ticket string, f *probeFixture) *clusterharness.Node {
	return h.StartNode(ticket, func(_ *node.Config, deps *node.Dependencies) {
		deps.Probe, deps.Connections = f, f
	})
}

func awaitProbe(t *testing.T, c *clusterharness.Client, id string, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		view := c.Do("GET", probePath(id), nil, 200)
		if predicate(view) {
			return view
		}
		if time.Now().After(deadline) {
			t.Fatalf("探针状态未收敛: fresh=%v realtime=%v", view["fresh"], view["realtime"])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitUptime(t *testing.T, c *clusterharness.Client, id string, seconds float64) map[string]any {
	t.Helper()
	return awaitProbe(t, c, id, func(v map[string]any) bool {
		rt, ok := v["realtime"].(map[string]any)
		return ok && v["fresh"] == true && rt["uptime_seconds"] == seconds
	})
}

func probePoint(t *testing.T, view map[string]any, minute time.Time) map[string]any {
	t.Helper()
	for _, item := range view["minutes"].([]any) {
		point := item.(map[string]any)
		if point["minute"] == minute.UTC().Truncate(time.Minute).Format(time.RFC3339) {
			return point
		}
	}
	t.Fatalf("分钟不在窗口: %s", minute)
	return nil
}

// 若采样周期退回 10 秒、把系统连接数当业务数量或运行时长使用主机 uptime，此测试失败。
func TestProbeSamplesEveryFiveSecondsAndSeparatesConnections(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "入口 A")
	id := created["id"].(string)
	f := &probeFixture{values: probe.Values{
		"cpu_percent": 20, "memory_used_bytes": 200, "memory_total_bytes": 1000,
		"disk_used_bytes": 400, "disk_total_bytes": 2000,
		"network_receive_bytes_per_second": 300, "network_transmit_bytes_per_second": 600,
		"system_tcp_connections": 37, "uptime_seconds": 999999,
	}, business: 3, tunnel: 2}
	startProbeNode(h, created["installation_ticket"].(string), f)
	view := awaitUptime(t, c, id, 0)
	rt := view["realtime"].(map[string]any)
	for key, want := range map[string]float64{"cpu_percent": 20, "memory_used_bytes": 200, "memory_total_bytes": 1000,
		"disk_used_bytes": 400, "disk_total_bytes": 2000, "network_receive_bytes_per_second": 300,
		"network_transmit_bytes_per_second": 600, "system_tcp_connections": 37, "business_connections": 3, "tunnel_connections": 2} {
		if rt[key] != want {
			t.Fatalf("%s = %v, want %v", key, rt[key], want)
		}
	}
	if nodeRow(t, c, id)["status"] != "online" {
		t.Fatal("真实节点未在线")
	}
	h.Clock.Advance(4 * time.Second)
	if c.Do("GET", probePath(id), nil, 200)["sampled_at"] != view["sampled_at"] {
		t.Fatal("5 秒前提前采样")
	}
	f.set(probe.Values{"cpu_percent": 80, "system_tcp_connections": 41}, nil)
	h.Clock.Advance(time.Second)
	view = awaitUptime(t, c, id, 5)
	if view["realtime"].(map[string]any)["cpu_percent"] != float64(80) {
		t.Fatal("5 秒实时值未刷新")
	}
}

// 错误的平均分母、丢失最大值、跨分钟混合或仅在内存保存聚合时失败。
func TestProbeMinuteAverageMaximumMissingFieldsAndPersistence(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "出口 A")
	id := created["id"].(string)
	start := h.Clock.Now()
	f := &probeFixture{values: probe.Values{"cpu_percent": 20, "memory_used_bytes": 200}, business: 3, tunnel: 1}
	n := startProbeNode(h, created["installation_ticket"].(string), f)
	awaitUptime(t, c, id, 0)
	f.set(probe.Values{"cpu_percent": 80}, nil)
	h.Clock.Advance(5 * time.Second)
	view := awaitUptime(t, c, id, 5)
	point := probePoint(t, view, start)
	avg, max := point["average"].(map[string]any), point["maximum"].(map[string]any)
	if point["samples"] != float64(2) || avg["cpu_percent"] != float64(50) || max["cpu_percent"] != float64(80) || avg["memory_used_bytes"] != float64(200) || avg["disk_used_bytes"] != nil {
		t.Fatalf("分钟聚合口径错误: %v", point)
	}
	f.set(probe.Values{"cpu_percent": 40}, nil)
	h.Clock.Advance(55 * time.Second)
	view = awaitUptime(t, c, id, 60)
	if probePoint(t, view, start.Add(time.Minute))["samples"] != float64(1) {
		t.Fatal("快进不能伪造漏采样本")
	}
	n.Stop()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	view = c.Do("GET", probePath(id), nil, 200)
	if view["fresh"] != false || view["realtime"] != nil {
		t.Fatal("重启后不可把旧样本标实时")
	}
	if probePoint(t, view, start)["average"].(map[string]any)["cpu_percent"] != float64(50) {
		t.Fatal("分钟聚合未持久保存")
	}
}

// 若离线被补零、重连补报历史探针、重连重置进程运行时长或节点重启不重置时失败。
func TestProbeOfflineGapsRecoveryProcessUptimeAndTwentyFourHourWindow(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "入口 A")
	id := created["id"].(string)
	start := h.Clock.Now()
	f := &probeFixture{values: probe.Values{"cpu_percent": 25}}
	n := startProbeNode(h, created["installation_ticket"].(string), f)
	awaitUptime(t, c, id, 0)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	if view := c.Do("GET", probePath(id), nil, 200); view["fresh"] != false || view["realtime"] != nil {
		t.Fatal("断链不能继续显示实时值")
	}
	h.Clock.Advance(3 * time.Minute)
	view := c.Do("GET", probePath(id), nil, 200)
	for _, minute := range []time.Time{start.Add(time.Minute), start.Add(2 * time.Minute)} {
		p := probePoint(t, view, minute)
		if p["average"] != nil || p["maximum"] != nil {
			t.Fatalf("离线应缺测: %v", p)
		}
	}
	n.Restore()
	for range 8 {
		h.Clock.Advance(time.Second)
		time.Sleep(15 * time.Millisecond)
		if nodeRow(t, c, id)["connected"] == true {
			break
		}
	}
	view = awaitProbe(t, c, id, func(v map[string]any) bool { return v["fresh"] == true })
	if view["realtime"].(map[string]any)["uptime_seconds"].(float64) < 180 {
		t.Fatal("重连重置了进程运行时长")
	}
	if probePoint(t, view, start.Add(time.Minute))["average"] != nil {
		t.Fatal("重连补画了离线探针")
	}
	n.Restart()
	awaitUptime(t, c, id, 0)
	n.Stop()
	h.Clock.Advance(24 * time.Hour)
	login(c)
	view = c.Do("GET", probePath(id), nil, 200)
	points := view["minutes"].([]any)
	if len(points) != 1440 {
		t.Fatal("时间窗口不是 24 小时")
	}
	for _, item := range points {
		if item.(map[string]any)["average"] != nil {
			t.Fatal("24 小时之前样本未排除")
		}
	}
}

// 采集错误或非法字段不能填零，且不能阻断活性和其他有效字段；恢复后继续采样。
func TestProbeCollectorFailureAndInvalidValuesRemainMissing(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "采集失败节点")
	id := created["id"].(string)
	f := &probeFixture{values: probe.Values{"cpu_percent": 99}, err: errors.New("读取失败"), business: 4, tunnel: 2}
	startProbeNode(h, created["installation_ticket"].(string), f)
	view := awaitUptime(t, c, id, 0)
	rt := view["realtime"].(map[string]any)
	if rt["cpu_percent"] != nil || rt["business_connections"] != float64(4) {
		t.Fatalf("失败不应填零/污染有效字段: %v", rt)
	}
	f.set(probe.Values{"cpu_percent": 150, "memory_used_bytes": -1, "system_tcp_connections": 9}, nil)
	h.Clock.Advance(5 * time.Second)
	view = awaitUptime(t, c, id, 5)
	rt = view["realtime"].(map[string]any)
	if rt["cpu_percent"] != nil || rt["memory_used_bytes"] != nil || rt["system_tcp_connections"] != float64(9) {
		t.Fatalf("坏字段未隔离: %v", rt)
	}
	f.set(probe.Values{"cpu_percent": 30}, nil)
	h.Clock.Advance(5 * time.Second)
	view = awaitUptime(t, c, id, 10)
	if view["realtime"].(map[string]any)["cpu_percent"] != float64(30) {
		t.Fatal("采集未恢复")
	}
	if nodeRow(t, c, id)["last_seen_at"] != h.Clock.Now().Format(time.RFC3339Nano) {
		t.Fatal("采集错误影响活性")
	}
}

type stalledProbe struct{ first bool }

func (s *stalledProbe) Collect(ctx context.Context) (probe.Values, error) {
	if !s.first {
		s.first = true
		return probe.Values{"cpu_percent": 10}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// 采集器卡住时旧值必须过期，不能拖住独立的控制活性。
func TestProbeStalledCollectorExpiresRealtimeWithoutStoppingLiveness(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "采集停滞")
	id := created["id"].(string)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) { deps.Probe = &stalledProbe{} })
	awaitUptime(t, c, id, 0)
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] == h.Clock.Now().Format(time.RFC3339Nano) })
	view := c.Do("GET", probePath(id), nil, 200)
	if view["fresh"] != false || view["realtime"] != nil || view["sampled_at"] == nil {
		t.Fatal("卡住的采集器导致旧数据伪装实时")
	}
	h.Clock.Advance(50 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] == h.Clock.Now().Format(time.RFC3339Nano) })
	view = c.Do("GET", probePath(id), nil, 200)
	if probePoint(t, view, h.Clock.Now())["average"] != nil {
		t.Fatal("采集停滞不能继续填充分钟曲线")
	}
}

// 即便报告声称其他节点身份，探针也只能写入认证节点；撤销后不能继续污染曲线。
func TestProbeUsesAuthenticatedIdentityAndRejectsRevokedSession(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "入口 A"), addNode(c, "出口 B")
	aID, bID := a["id"].(string), b["id"].(string)
	f := &probeFixture{values: probe.Values{"cpu_percent": 20}}
	first := startProbeNode(h, a["installation_ticket"].(string), f)
	awaitUptime(t, c, aID, 0)
	other := &probeFixture{values: probe.Values{"cpu_percent": 80}}
	h.CloneNode(first, bID, func(_ *node.Config, deps *node.Dependencies) { deps.Probe, deps.Connections = other, other })
	awaitProbe(t, c, aID, func(v map[string]any) bool {
		rt, ok := v["realtime"].(map[string]any)
		return ok && rt["cpu_percent"] == float64(80)
	})
	if view := c.Do("GET", probePath(bID), nil, 200); view["realtime"] != nil {
		t.Fatal("探针身份冒充成功")
	}
	c.Do("POST", "/api/nodes/"+aID+"/token/revoke", nil, 204)
	view := c.Do("GET", probePath(aID), nil, 200)
	before := probePoint(t, view, h.Clock.Now())["samples"]
	if view["realtime"] != nil || view["fresh"] != false {
		t.Fatal("撤销后的旧样本仍标实时")
	}
	h.Clock.Advance(5 * time.Second)
	view = c.Do("GET", probePath(aID), nil, 200)
	if probePoint(t, view, h.Clock.Now())["samples"] != before {
		t.Fatal("失效会话仍能写探针")
	}
}
