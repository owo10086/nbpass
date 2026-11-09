package clusterharness_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/eventlog"
	"nbpass/internal/probe"
)

const retentionPath = "/api/settings/retention"

func retentionBody(days, hours int) map[string]any {
	return map[string]any{"event_days": days, "probe_hours": hours}
}

// 默认值、校验、认证或持久化缺失时失败；无效提交不能部分覆盖设置。
func TestRetentionSettingsValidationAuthenticationAndPersistence(t *testing.T) {
	h, c := nodeBrowser(t)
	h.Client().Do("GET", retentionPath, nil, 401)
	h.Client().Do("PUT", retentionPath, retentionBody(7, 2), 401)
	v := c.Do("GET", retentionPath, nil, 200)
	if v["event_days"] != float64(90) || v["probe_hours"] != float64(24) {
		t.Fatalf("默认保留期错误: %v", v)
	}
	r := c.Request("PUT", retentionPath, retentionBody(7, 2), map[string]string{"Origin": "https://other.example"})
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatal("跨源修改未被拒绝")
	}
	c.Do("PUT", retentionPath, retentionBody(7, 2), 200)
	for _, invalid := range []any{
		retentionBody(0, 2), retentionBody(-1, 2), retentionBody(3651, 2),
		retentionBody(7, 0), retentionBody(7, 8761),
		map[string]any{"event_days": 7}, map[string]any{"event_days": nil, "probe_hours": 2},
		map[string]any{"event_days": 1.5, "probe_hours": 2},
		map[string]any{"event_days": 7, "probe_hours": 2, "unknown": true},
	} {
		c.Do("PUT", retentionPath, invalid, 400)
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	v = c.Do("GET", retentionPath, nil, 200)
	if v["event_days"] != float64(7) || v["probe_hours"] != float64(2) {
		t.Fatalf("设置未持久化或被无效请求覆盖: %v", v)
	}
}

func recordRetentionEvent(t *testing.T, h *clusterharness.Harness, id string) {
	t.Helper()
	if err := h.Panel.RecordEvent(context.Background(), "", eventlog.Event{ID: id, Type: "retention_fixture", Message: id}); err != nil {
		t.Fatal(err)
	}
}

func waitRetention(t *testing.T, ready func() bool, advance func()) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		if advance != nil {
			advance()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("历史清理未收敛")
}

// 默认 90 天不能提前删除；超过窗口后由后台真实清理。
func TestRetentionDefaultEventsExpireAfterClockAdvance(t *testing.T) {
	h, c := nodeBrowser(t)
	recordRetentionEvent(t, h, "default-old")
	h.Clock.Advance(89 * 24 * time.Hour)
	login(c)
	if len(eventRows(c, "?type=retention_fixture")) != 1 {
		t.Fatal("默认保留期内事件被清理")
	}
	h.Clock.Advance(25 * time.Hour)
	login(c)
	waitRetention(t, func() bool { return len(eventRows(c, "?type=retention_fixture")) == 0 }, func() { h.Clock.Advance(time.Minute) })
}

// 缩短曲线保留期须实际删除，再延长也不能复现已删除样本；当前样本仍保留。
func TestRetentionProbeHistoryIsDeletedWithoutRemovingRecentSamples(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "保留期探针")
	id := created["id"].(string)
	start := h.Clock.Now()
	n := startProbeNode(h, created["installation_ticket"].(string), &probeFixture{values: probe.Values{"cpu_percent": 20}})
	awaitUptime(t, c, id, 0)
	n.Stop()
	h.Clock.Advance(2 * time.Hour)
	n.Restart()
	awaitUptime(t, c, id, 0)
	if probePoint(t, c.Do("GET", probePath(id), nil, 200), start)["samples"] != float64(1) {
		t.Fatal("清理前旧样本应存在")
	}
	c.Do("PUT", retentionPath, retentionBody(90, 1), 200)
	waitRetention(t, func() bool {
		return probePoint(t, c.Do("GET", probePath(id), nil, 200), start)["samples"] == float64(0)
	}, nil)
	c.Do("PUT", retentionPath, retentionBody(90, 24), 200)
	v := c.Do("GET", probePath(id), nil, 200)
	if probePoint(t, v, start)["average"] != nil || probePoint(t, v, h.Clock.Now())["average"].(map[string]any)["cpu_percent"] != float64(20) {
		t.Fatal("历史未真正清除或新样本被误删")
	}
}

// 大于两批的历史必须分批删除；批次间 HTTP 写入必须可完成，新事件不可误删。
func TestRetentionBatchesAllowHTTPWritesWhileBacklogRemains(t *testing.T) {
	h, c := nodeBrowser(t)
	c.HTTP.Timeout = 2 * time.Second
	for i := range 600 {
		recordRetentionEvent(t, h, fmt.Sprintf("backlog-%d", i))
	}
	h.Clock.Advance(25 * time.Hour)
	login(c)
	c.Do("PUT", retentionPath, retentionBody(1, 24), 200)
	waitRetention(t, func() bool { return len(eventRows(c, "?type=retention_fixture&limit=1000")) < 600 }, nil)
	remaining := len(eventRows(c, "?type=retention_fixture&limit=1000"))
	if remaining == 0 {
		t.Fatal("未在批次间让出执行，整段历史被一次清空")
	}
	c.Do("PUT", "/api/secrets/retention-write", map[string]string{"value": "清理期间可写"}, 200)
	addNode(c, "清理期间新增节点")
	recordRetentionEvent(t, h, "keep-new")
	waitRetention(t, func() bool {
		rows := eventRows(c, "?type=retention_fixture&limit=1000")
		return len(rows) == 1 && rows[0].(map[string]any)["event_id"] == "keep-new"
	}, func() { h.Clock.Advance(10 * time.Millisecond) })
}

// 清理不得改变节点配置高水位、累计业务流量，以及真实进行中动作和占用额度。
func TestRetentionPreservesRotationActionQuotaConfigAndTraffic(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "计量入口")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	port := h.ReserveTCPPort()
	rid := c.Do("POST", "/api/rules", directBody(entryGroup(c, id)["id"].(string), port, "127.0.0.1", echoTarget(t, "A:")), 201)["id"].(string)
	awaitRevision(t, c, id, 0)
	tcpExchange(t, port, "A:").Close()
	awaitTraffic(t, h, c, rid, 4, 6, float64(0))
	n.Stop()
	recordRetentionEvent(t, h, "old-safety")
	h.Clock.Advance(25 * time.Hour)
	login(c)
	f := rotationFixtureAt(t, h, c, false)
	c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, c, f.path, "observing")
	before := c.Do("GET", f.path, nil, 200)
	versions := nodeRow(t, c, id)
	traffic := ruleTraffic(t, c, rid)
	if before["quota"].(map[string]any)["used_day"] != float64(1) {
		t.Fatal("测试未占用换 IP 额度")
	}
	c.Do("PUT", retentionPath, retentionBody(1, 1), 200)
	waitRetention(t, func() bool { return len(eventRows(c, "?type=retention_fixture")) == 0 }, nil)
	after := c.Do("GET", f.path, nil, 200)
	if !reflect.DeepEqual(before["action"], after["action"]) || !reflect.DeepEqual(before["quota"], after["quota"]) {
		t.Fatalf("清理改变了进行中动作或额度: before=%v after=%v", before, after)
	}
	current := nodeRow(t, c, id)
	for _, field := range []string{"desired_revision", "applied_revision", "attempted_revision", "applied_digest"} {
		if versions[field] != current[field] {
			t.Fatalf("清理改变了配置状态 %s", field)
		}
	}
	if !reflect.DeepEqual(traffic, ruleTraffic(t, c, rid)) {
		t.Fatal("清理改变了规则累计流量")
	}
	c.Do("POST", f.path+"/actions", nil, 409)
}
