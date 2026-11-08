package clusterharness_test

import (
	"context"
	"errors"
	"fmt"
	"nbpass/internal/node"
	"nbpass/internal/timing"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

// 配置入口缺失、未校验四个站点或不持久化节点开关时失败。
func TestSelfCheckSettingsAndNodeSwitch(t *testing.T) {
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{} })
	c := h.Client()
	setup(c)
	login(c)
	settings := c.Do("GET", "/api/settings/self-check", nil, 200)
	if settings["interval_seconds"] != float64(120) || len(settings["sites"].([]any)) != 4 {
		t.Fatalf("默认自测参数: %v", settings)
	}
	created := addNode(c, "自测开关")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/self-check"
	if c.Do("GET", path, nil, 200)["enabled"] != false {
		t.Fatal("自测应默认关闭")
	}
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	settings["sites"] = []string{"one.test", "two.test", "three.test", "four.test"}
	c.Do("PUT", "/api/settings/self-check", settings, 204)
	c.Do("PUT", "/api/settings/self-check", map[string]any{"sites": []string{"one.test"}}, 400)
	for key, value := range map[string]any{"connect_timeout_seconds": 6, "resolve_timeout_seconds": 11, "resolve_attempts": 3, "round_timeout_seconds": 61, "jitter_seconds": 60, "interval_seconds": 0} {
		bad := map[string]any{}
		for k, v := range settings {
			bad[k] = v
		}
		bad[key] = value
		c.Do("PUT", "/api/settings/self-check", bad, 400)
	}
	for _, sites := range [][]string{{"one.test", "ONE.test", "three.test", "four.test"}, {"https://one.test", "two.test", "three.test", "four.test"}, {"1.1.1.1", "two.test", "three.test", "four.test"}} {
		bad := map[string]any{}
		for k, v := range settings {
			bad[k] = v
		}
		bad["sites"] = sites
		c.Do("PUT", "/api/settings/self-check", bad, 400)
	}
	response := c.Request("PUT", path, map[string]bool{"enabled": false}, map[string]string{"Origin": "https://evil.test"})
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("自测修改未拒绝跨源")
	}
	c.Do("GET", path+"?limit=0", nil, 400)
	c.Do("GET", path+"?before=bad", nil, 400)

	c.Do("PUT", path, map[string]any{}, 400)
	c.Do("PUT", "/api/nodes/missing/self-check", map[string]bool{"enabled": true}, 404)
	h.Client().Do("GET", path, nil, 401)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	view := c.Do("GET", path, nil, 200)
	if view["enabled"] != true || view["state"] != "unknown" {
		t.Fatalf("开关与未知状态: %v", view)
	}
	if c.Do("GET", "/api/settings/self-check", nil, 200)["sites"].([]any)[0] != "one.test" {
		t.Fatal("自测站点未持久化")
	}
}

// 连续异常、未知中断、一次正常恢复均须由真实节点结果驱动。
func TestSelfCheckRoundsDetermineBlockedAndRecover(t *testing.T) {
	fake := &domesticSites{}
	h, c := selfCheckBrowser(t)
	created := addNode(c, "自测判墙")
	id := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	h.StartNode(created["installation_ticket"].(string), fake.dependencies)
	awaitSelfCheck(t, c, id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, id, 2, "suspected", 2)
	fake.mode.Store(1) // DNS 失败优先于三个 TCP 失败。
	advanceSelfCheck(t, h, c, id, 3, "suspected", 0)
	fake.mode.Store(0)
	advanceSelfCheck(t, h, c, id, 4, "suspected", 1)
	advanceSelfCheck(t, h, c, id, 5, "suspected", 2)
	advanceSelfCheck(t, h, c, id, 6, "blocked", 3)
	fake.mode.Store(1)
	advanceSelfCheck(t, h, c, id, 7, "blocked", 0)
	fake.mode.Store(2)
	view := advanceSelfCheck(t, h, c, id, 8, "normal", 0)
	rounds := view["rounds"].([]any)
	if rounds[0].(map[string]any)["source"] != "self_check" || rounds[0].(map[string]any)["evidence_source"] != "self_check" || rounds[1].(map[string]any)["result"] != "unknown" {
		t.Fatalf("原始证据: %v", rounds)
	}
	for _, kind := range []string{"node_blocked", "node_unblocked"} {
		events := c.Do("GET", "/api/events?node_id="+id+"&type="+kind, nil, 200)["events"].([]any)
		if len(events) != 1 {
			t.Fatalf("%s 未恰好记录一次: %v", kind, events)
		}
	}
}

func selfCheckBrowser(t *testing.T) (*clusterharness.Harness, *clusterharness.Client) {
	t.Helper()
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{} })
	c := h.Client()
	setup(c)
	login(c)
	s := c.Do("GET", "/api/settings/self-check", nil, 200)
	s["jitter_seconds"] = 0
	s["sites"] = []string{"one.test", "two.test", "three.test", "four.test"}
	c.Do("PUT", "/api/settings/self-check", s, 204)
	return h, c
}

type domesticSites struct{ mode atomic.Int32 }

func (f *domesticSites) dependencies(_ *node.Config, d *node.Dependencies) {
	d.SelfCheckLookup = func(_ context.Context, host string) ([]string, error) {
		if f.mode.Load() == 1 && host == "one.test" {
			return nil, errors.New("假解析失败")
		}
		return []string{"203.0.113.1"}, nil
	}
	d.SelfCheckDial = func(_ context.Context, address string) (net.Conn, error) {
		if address != "203.0.113.1:443" {
			return nil, errors.New("错误的自测地址")
		}
		if f.mode.Load() == 2 {
			a, b := net.Pipe()
			b.Close()
			return a, nil
		}
		return nil, errors.New("假国内站点不可达")
	}
}
func awaitSelfCheck(t *testing.T, c *clusterharness.Client, id string, count int, state string, consecutive int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var view map[string]any
	for time.Now().Before(deadline) {
		view = c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
		if len(view["rounds"].([]any)) >= count {
			if view["state"] != state || view["consecutive_abnormal"] != float64(consecutive) {
				t.Fatalf("轮 %d 判定: %v", count, view)
			}
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("未收到第 %d 轮: %v", count, view)
	return nil
}
func advanceSelfCheck(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, count int, state string, consecutive int) map[string]any {
	t.Helper()
	for range 12 {
		awaitControlIdle(t, h)
		before := nodeRow(t, c, id)["last_seen_at"]
		h.Clock.Advance(10 * time.Second)
		awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] != before })
	}
	return awaitSelfCheck(t, c, id, count, state, consecutive)
}

// 控制链路离线及进程重建不能停掉自测，重连后应按顺序补报而不重复判墙。
func TestSelfCheckOfflineRestartBackfillsRounds(t *testing.T) {
	h, c := selfCheckBrowser(t)
	fake := &domesticSites{}
	created := addNode(c, "离线自测")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/self-check"
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	n := h.StartNode(created["installation_ticket"].(string), fake.dependencies)
	awaitSelfCheck(t, c, id, 1, "suspected", 1)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	// 等待本地真实网络执行与加密写盘，业务观察仍只读 HTTP。
	for range 12 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	n.Restart()
	for range 12 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(c.Do("GET", path, nil, 200)["rounds"].([]any)); got != 1 {
		t.Fatalf("断链时出现上报: %d", got)
	}
	n.Restore()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
		view := c.Do("GET", path, nil, 200)
		if len(view["rounds"].([]any)) >= 3 {
			break
		}
	}
	view := awaitSelfCheck(t, c, id, 3, "blocked", 3)
	for _, value := range view["rounds"].([]any) {
		r := value.(map[string]any)
		if r["finished_at"] == nil || r["sites"] == nil {
			t.Fatalf("缺少离线原始证据: %v", r)
		}
	}
	n.Restart()
	for range 3 {
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitNode(t, c, id, connected)
	if got := len(c.Do("GET", path, nil, 200)["rounds"].([]any)); got != 3 {
		t.Fatalf("重启重复上报: %d", got)
	}
	if got := len(c.Do("GET", "/api/events?node_id="+id+"&type=node_blocked", nil, 200)["events"].([]any)); got != 1 {
		t.Fatalf("重复判墙事件: %d", got)
	}
}

// 五秒连接期限、十秒解析期限及最多两次解析均由假时钟驱动，最终证据通过 HTTP 验收。
func TestSelfCheckTimeoutBudgets(t *testing.T) {
	for _, kind := range []string{"connect", "resolve", "round"} {
		t.Run(kind, func(t *testing.T) {
			h, c := selfCheckBrowser(t)
			created := addNode(c, kind)
			id := created["id"].(string)
			path := "/api/nodes/" + id + "/self-check"
			if kind == "round" {
				s := c.Do("GET", "/api/settings/self-check", nil, 200)
				s["round_timeout_seconds"] = 3
				c.Do("PUT", "/api/settings/self-check", s, 204)
			}
			c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
			entered := make(chan struct{}, 16)
			h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
				d.SelfCheckLookup = func(ctx context.Context, _ string) ([]string, error) {
					if kind == "resolve" {
						entered <- struct{}{}
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return []string{"203.0.113.1"}, nil
				}
				d.SelfCheckDial = func(ctx context.Context, _ string) (net.Conn, error) {
					entered <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				}
			})
			waitFour := func() {
				t.Helper()
				for range 4 {
					select {
					case <-entered:
					case <-time.After(5 * time.Second):
						t.Fatal("自测未并行开始四个站点")
					}
				}
			}
			waitFour()
			budget := 5
			if kind == "resolve" {
				budget = 10
			}
			if kind == "round" {
				budget = 3
			}
			h.Clock.Advance(time.Duration(budget-1) * time.Second)
			if len(c.Do("GET", path, nil, 200)["rounds"].([]any)) != 0 {
				t.Fatal("自测提前超时")
			}
			h.Clock.Advance(time.Second)
			if kind == "resolve" {
				waitFour()
				h.Clock.Advance(10 * time.Second)
			}
			state, count := "suspected", 1
			if kind != "connect" {
				state, count = "unknown", 0
			}
			// 解析测试跨过发送响应期限，继续驱动重连时钟；不改变生产超时。
			for i := 0; i < 15; i++ {
				view := c.Do("GET", path, nil, 200)
				if len(view["rounds"].([]any)) > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
				h.Clock.Advance(time.Second)
			}
			view := awaitSelfCheck(t, c, id, 1, state, count)
			round := view["rounds"].([]any)[0].(map[string]any)
			started, _ := time.Parse(time.RFC3339Nano, round["started_at"].(string))
			finished, _ := time.Parse(time.RFC3339Nano, round["finished_at"].(string))
			if finished.Sub(started) > 60*time.Second {
				t.Fatalf("单轮超过 60 秒: %v", round)
			}
			for _, raw := range round["sites"].([]any) {
				site := raw.(map[string]any)
				if kind == "resolve" && site["resolve_attempts"] != float64(2) {
					t.Fatalf("解析次数错误: %v", site)
				}
				if kind == "connect" {
					conns := site["connections"].([]any)
					if conns[0].(map[string]any)["duration_ms"] != float64(5000) {
						t.Fatalf("连接未在五秒取消: %v", site)
					}
				}
			}
		})
	}
}

func TestSelfCheckSingleNormalClearsSuspectedAndNotifications(t *testing.T) {
	telegram := &fakeTelegram{}
	h, c := telegramBrowser(t, telegram)
	s := c.Do("GET", "/api/settings/self-check", nil, 200)
	s["jitter_seconds"] = 0
	c.Do("PUT", "/api/settings/self-check", s, 204)
	created := addNode(c, "恢复通知")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/self-check"
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	fake := &domesticSites{}
	h.StartNode(created["installation_ticket"].(string), fake.dependencies)
	awaitSelfCheck(t, c, id, 1, "suspected", 1)
	fake.mode.Store(2)
	advanceSelfCheck(t, h, c, id, 2, "normal", 0)
	fake.mode.Store(0)
	advanceSelfCheck(t, h, c, id, 3, "suspected", 1)
	advanceSelfCheck(t, h, c, id, 4, "suspected", 2)
	advanceSelfCheck(t, h, c, id, 5, "blocked", 3)
	fake.mode.Store(2)
	advanceSelfCheck(t, h, c, id, 6, "normal", 0)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, sent := telegram.snapshot()
		if len(sent) >= 3 {
			break
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	_, sent := telegram.snapshot()
	blocked, recovered := 0, 0
	for _, m := range sent {
		if strings.Contains(m.Text, "认定被墙") {
			blocked++
		}
		if strings.Contains(m.Text, "已解除") {
			recovered++
		}
	}
	if blocked != 1 || recovered != 2 {
		t.Fatalf("被墙/解除通知未独立去重: %v", sent)
	}
}

// 仅转发配置升级不能让进行中的自测结束后立即多跑一轮、加速判墙。
func TestSelfCheckScheduleIgnoresUnrelatedSnapshotRevision(t *testing.T) {
	h, c := selfCheckBrowser(t)
	created := addNode(c, "调度隔离")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/self-check"
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	entered := make(chan struct{}, 16)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.SelfCheckLookup = func(context.Context, string) ([]string, error) { return []string{"203.0.113.1"}, nil }
		d.SelfCheckDial = func(ctx context.Context, _ string) (net.Conn, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	for range 4 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("首轮未开始")
		}
	}
	group := entryGroup(c, id)["id"].(string)
	c.Do("POST", "/api/rules", directBody(group, h.ReserveTCPPort(), "8.8.8.8", 443), 201)
	awaitRevision(t, c, id, 1)
	h.Clock.Advance(5 * time.Second)
	awaitSelfCheck(t, c, id, 1, "suspected", 1)
	// 若错误地与完整快照版本绑定，此处立即出现下一轮网络调用。
	select {
	case <-entered:
		t.Fatal("无关配置更新导致立即多跑自测")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSelfCheckThreeFailedSitesThreshold(t *testing.T) {
	h, c := selfCheckBrowser(t)
	created := addNode(c, "三站点阈值")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/self-check"
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	var failures atomic.Int32
	failures.Store(2)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.SelfCheckLookup = func(_ context.Context, host string) ([]string, error) {
			indexes := map[string]string{"one.test": "1", "two.test": "2", "three.test": "3", "four.test": "4"}
			return []string{"203.0.113." + indexes[host]}, nil
		}
		d.SelfCheckDial = func(_ context.Context, address string) (net.Conn, error) {
			for i := int32(1); i <= failures.Load(); i++ {
				if address == fmt.Sprintf("203.0.113.%d:443", i) {
					return nil, errors.New("假国内站点不可达")
				}
			}
			a, b := net.Pipe()
			b.Close()
			return a, nil
		}
	})
	awaitSelfCheck(t, c, id, 1, "normal", 0)
	failures.Store(3)
	advanceSelfCheck(t, h, c, id, 2, "suspected", 1)
	failures.Store(2)
	advanceSelfCheck(t, h, c, id, 3, "normal", 0)
}

func TestSelfCheckDisabledNodeStopsSampling(t *testing.T) {
	h, c := selfCheckBrowser(t)
	created := addNode(c, "关闭自测")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/self-check"
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	fake := &domesticSites{}
	h.StartNode(created["installation_ticket"].(string), fake.dependencies)
	awaitSelfCheck(t, c, id, 1, "suspected", 1)
	previous := nodeRow(t, c, id)["desired_revision"].(float64)
	c.Do("PUT", path, map[string]bool{"enabled": false}, 204)
	awaitRevision(t, c, id, previous)
	for range 12 {
		awaitControlIdle(t, h)
		before := nodeRow(t, c, id)["last_seen_at"]
		h.Clock.Advance(10 * time.Second)
		awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] != before })
	}
	view := c.Do("GET", path, nil, 200)
	if len(view["rounds"].([]any)) != 1 || view["enabled"] != false {
		t.Fatalf("关闭自测未停止采样或丢失历史: %v", view)
	}
}

// 计算相对期限后、计时器注册前时钟跳进，不能漏掉已经到期的自测。
func TestSelfCheckDoesNotMissDeadlineDuringTimerRegistration(t *testing.T) {
	h, c := selfCheckBrowser(t)
	created := addNode(c, "计时器注册窗口")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/self-check"
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	jumped := make(chan struct{})
	fake := &domesticSites{}
	h.StartNode(created["installation_ticket"].(string), func(config *node.Config, d *node.Dependencies) {
		fake.dependencies(config, d)
		d.Clock = &advancingSelfCheckClock{Clock: d.Clock, advance: func() { h.Clock.Advance(120 * time.Second); close(jumped) }}
	})
	select {
	case <-jumped:
	case <-time.After(5 * time.Second):
		t.Fatal("未进入计时器注册窗口")
	}
	for range 20 {
		view := c.Do("GET", path, nil, 200)
		if len(view["rounds"].([]any)) >= 2 {
			return
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("计时器注册窗口漏掉已到期自测")
}

type advancingSelfCheckClock struct {
	timing.Clock
	once    sync.Once
	advance func()
}

func (c *advancingSelfCheckClock) NewTimer(d time.Duration) timing.Timer {
	if d == 120*time.Second {
		c.once.Do(c.advance)
	}
	return c.Clock.NewTimer(d)
}

func awaitControlIdle(t *testing.T, h *clusterharness.Harness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Clock.WaitForControlIdle(ctx); err != nil {
		t.Fatalf("在途控制消息尚未处理完成: %v", err)
	}
}
