package clusterharness_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
)

// 真实节点必须先成功连接本轮对照，再检测固定目标；三态和原始证据只从 HTTP 读取。
func TestCheckerArbitrationThreeStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		modes []int
		want  string
		count int
	}{
		{"任一成功", []int{1, 0}, "normal", 0},
		{"全部有效失败", []int{1, 1}, "abnormal", 1},
		{"无有效结果", []int{2, 2}, "unknown", 0},
		{"部分无效", []int{1, 2}, "abnormal", 1},
		{"执行错误", []int{1, 3}, "unknown", 0},
		{"本地资源错误", []int{1, 4}, "unknown", 0},
		{"成功优先于错误", []int{0, 3}, "normal", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := selfCheckBrowser(t)
			for i, mode := range tc.modes {
				startChecker(t, h, c, i, mode, nil)
			}
			target := addNode(c, "被测节点")
			id := target["id"].(string)
			c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
			fake := &domesticSites{}
			fake.mode.Store(1)
			h.StartNode(target["installation_ticket"].(string), func(cfg *node.Config, d *node.Dependencies) {
				fake.dependencies(cfg, d)
				d.PublicIPv4 = func(context.Context) (string, error) { return "8.8.8.8", nil }
			})
			view := awaitArbitration(t, c, id, tc.want)
			round := view["rounds"].([]any)[0].(map[string]any)
			if round["result"] != "unknown" || view["consecutive_abnormal"] != float64(tc.count) {
				t.Fatalf("补判破坏原始结果/计数: %v", view)
			}
			arbitration := round["arbitration"].(map[string]any)
			if arbitration["target_ip"] != "8.8.8.8" || arbitration["target_port"] != float64(18080) || len(arbitration["checkers"].([]any)) != 2 {
				t.Fatalf("未冻结任务: %v", arbitration)
			}
			if tc.want != "unknown" && round["evidence_source"] != "domestic_checker" {
				t.Fatalf("证据来源: %v", round)
			}
		})
	}
}

func startChecker(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, index, mode int, gate <-chan struct{}) *clusterharness.Node {
	t.Helper()
	created := addNode(c, "国内检测点")
	id := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.CheckerLookup = func(context.Context, string) ([]string, error) { return []string{"203.0.113.9"}, nil }
		d.CheckerDial = func(ctx context.Context, address string) (net.Conn, error) {
			if address == "203.0.113.9:443" {
				if mode == 2 {
					return nil, errors.New("对照不通")
				}
				return checkerPipe(), nil
			}
			if address != "8.8.8.8:18080" {
				return nil, errors.New("任务目标错误")
			}
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if mode == 4 {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.EMFILE}
			}
			if mode == 3 {
				return nil, errors.New("执行器内部错误")
			}
			if mode == 1 {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("目标拒绝连接")}
			}
			return checkerPipe(), nil
		}
	})
	awaitNode(t, c, id, connected)
	return n
}
func checkerPipe() net.Conn { a, b := net.Pipe(); b.Close(); return a }
func awaitArbitration(t *testing.T, c *clusterharness.Client, id, want string) map[string]any {
	t.Helper()
	var view map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view = c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
		if rs := view["rounds"].([]any); len(rs) > 0 {
			r := rs[0].(map[string]any)
			if a, ok := r["arbitration"].(map[string]any); ok && a["status"] == "completed" {
				if r["final_result"] != want {
					t.Fatalf("最终结果应为 %s: %v", want, view)
				}
				return view
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("补判未结束: %v", view)
	return nil
}

// 未知轮不能跨过缺失证据累计；后续完整补判异常也应进入同一连续轮计数。
func TestCheckerArbitrationConsecutiveRounds(t *testing.T) {
	h, c := selfCheckBrowser(t)
	startChecker(t, h, c, 0, 1, nil)
	created := addNode(c, "连续补判")
	id := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	fake := &domesticSites{}
	fake.mode.Store(1)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	h.StartNode(created["installation_ticket"].(string), func(cfg *node.Config, d *node.Dependencies) {
		fake.dependencies(cfg, d)
		d.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
	})
	awaitArbitration(t, c, id, "abnormal")
	for round := 2; round <= 3; round++ {
		for range 12 {
			before := nodeRow(t, c, id)["last_seen_at"]
			h.Clock.Advance(10 * time.Second)
			awaitNode(t, c, id, func(r map[string]any) bool { return r["last_seen_at"] != before })
		}
		awaitSelfCheckCount(t, c, id, round)
		awaitArbitration(t, c, id, "abnormal")
	}
	view := c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
	if view["state"] != "blocked" || view["consecutive_abnormal"] != float64(3) {
		t.Fatalf("补判未累计判墙: %v", view)
	}
	checkerID := view["rounds"].([]any)[0].(map[string]any)["arbitration"].(map[string]any)["checkers"].([]any)[0].(map[string]any)["checker_id"].(string)
	c.Do("PUT", "/api/nodes/"+checkerID+"/checker", map[string]bool{"enabled": false}, 204)
	advanceCheckerClock(t, h, c, id, 120)
	awaitSelfCheckCount(t, c, id, 4)
	view = awaitArbitration(t, c, id, "unknown")
	if view["state"] != "blocked" || view["consecutive_abnormal"] != float64(0) {
		t.Fatalf("未知轮未中断或错误恢复: %v", view)
	}
	c.Do("PUT", "/api/nodes/"+checkerID+"/checker", map[string]bool{"enabled": true}, 204)
	advanceCheckerClock(t, h, c, id, 120)
	awaitSelfCheckCount(t, c, id, 5)
	view = awaitArbitration(t, c, id, "abnormal")
	if view["consecutive_abnormal"] != float64(1) {
		t.Fatalf("未知轮后重新计数错误: %v", view)
	}
}

func pendingArbitration(t *testing.T, c *clusterharness.Client, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v := c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
		if rs := v["rounds"].([]any); len(rs) > 0 {
			if a, ok := rs[0].(map[string]any)["arbitration"].(map[string]any); ok {
				return a
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("未安排补判")
	return nil
}
func startUnknownTarget(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, ip *atomic.Value) (string, *clusterharness.Node) {
	t.Helper()
	created := addNode(c, "未知自测目标")
	id := created["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	fake := &domesticSites{}
	fake.mode.Store(1)
	n := h.StartNode(created["installation_ticket"].(string), func(cfg *node.Config, d *node.Dependencies) {
		fake.dependencies(cfg, d)
		d.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
	})
	return id, n
}
func advanceCheckerClock(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, seconds int) {
	t.Helper()
	for range seconds / 10 {
		// HTTP 活性已落库不代表所有节点已消费响应；快进前等待在途
		// 控制消息结束，避免把健康检测点的 15 秒确认期限一起触发。
		awaitControlIdle(t, h)
		before := nodeRow(t, c, id)["last_seen_at"]
		h.Clock.Advance(10 * time.Second)
		awaitNode(t, c, id, func(v map[string]any) bool { return v["last_seen_at"] != before })
	}
}

// 固定集合中一项缺报，关闭角色、后来加入的新检测点都不能缩小/扩大本轮集合。
func TestCheckerArbitrationMissingAndFrozenMembership(t *testing.T) {
	h, c := selfCheckBrowser(t)
	gate := make(chan struct{})
	defer close(gate)
	startChecker(t, h, c, 0, 1, nil)
	slow := startChecker(t, h, c, 1, 0, gate)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	id, _ := startUnknownTarget(t, h, c, &ip)
	a := pendingArbitration(t, c, id)
	if len(a["checkers"].([]any)) != 2 {
		t.Fatalf("初始集合: %v", a)
	}
	slowID := a["checkers"].([]any)[0].(map[string]any)["checker_id"].(string)
	c.Do("PUT", "/api/nodes/"+slowID+"/checker", map[string]bool{"enabled": false}, 204)
	startChecker(t, h, c, 2, 0, nil)
	slow.Disconnect()
	advanceCheckerClock(t, h, c, id, 60)
	v := awaitArbitration(t, c, id, "unknown")
	a = v["rounds"].([]any)[0].(map[string]any)["arbitration"].(map[string]any)
	if len(a["checkers"].([]any)) != 2 || v["consecutive_abnormal"] != float64(0) {
		t.Fatalf("集合变化把缺报补成失败: %v", v)
	}
}

// 地址一度变化再变回，也不能接受已经失效的旧任务；端口/配置变化同样失效。
func TestCheckerArbitrationInvalidatesChangedTarget(t *testing.T) {
	for _, change := range []string{"ip", "port", "revision"} {
		t.Run(change, func(t *testing.T) {
			h, c := selfCheckBrowser(t)
			gate := make(chan struct{})
			defer close(gate)
			startChecker(t, h, c, 0, 0, gate)
			var ip atomic.Value
			ip.Store("8.8.8.8")
			id, target := startUnknownTarget(t, h, c, &ip)
			pendingArbitration(t, c, id)
			switch change {
			case "ip":
				ip.Store("1.1.1.1")
				target.Restart()
				awaitNode(t, c, id, func(row map[string]any) bool { return row["public_ipv4"] == "1.1.1.1" })
				ip.Store("8.8.8.8")
				target.Restart()
				awaitNode(t, c, id, func(row map[string]any) bool { return row["public_ipv4"] == "8.8.8.8" })
			case "port":
				c.Do("PUT", "/api/nodes/"+id+"/listener-ports", map[string]int{"tunnel_listen_port": 18443, "check_port": 18081}, 200)
			case "revision":
				entryGroup(c, id)
				// 开关变化生成新快照，旧任务不能改变当前判断。
				c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": false}, 204)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				v := c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
				r := v["rounds"].([]any)[0].(map[string]any)
				a := r["arbitration"].(map[string]any)
				if a["status"] == "invalidated" {
					if r["final_result"] != "unknown" {
						t.Fatal("旧目标结果生效")
					}
					return
				}
				h.Clock.Advance(time.Second)
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatal("旧目标任务未失效")
		})
	}
}

func TestCheckerArbitrationNoCoverageAndNeverSelfChecks(t *testing.T) {
	h, c := selfCheckBrowser(t)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	id, n := startUnknownTarget(t, h, c, &ip)
	// 让被测节点自身具有国内检测点资格，后续未知轮仍不可分配给自己。
	c.Do("PUT", "/api/nodes/"+id+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	v := awaitArbitration(t, c, id, "unknown")
	if len(v["rounds"].([]any)[0].(map[string]any)["arbitration"].(map[string]any)["checkers"].([]any)) != 0 {
		t.Fatal("无覆盖却生成检测点")
	}
	advanceCheckerClock(t, h, c, id, 120)
	v = awaitArbitration(t, c, id, "unknown")
	if len(v["rounds"].([]any)[0].(map[string]any)["arbitration"].(map[string]any)["checkers"].([]any)) != 0 {
		t.Fatal("给自己分配检测任务")
	}
	n.Restart()
}

// 完成/确认的任务必须释放在途槽，长时间运行不能在第九轮停止补判。
func TestCheckerArbitrationReleasesCompletedTasks(t *testing.T) {
	h, c := selfCheckBrowser(t)
	startChecker(t, h, c, 0, 1, nil)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	id, _ := startUnknownTarget(t, h, c, &ip)
	awaitArbitration(t, c, id, "abnormal")
	for round := 2; round <= 10; round++ {
		advanceCheckerClock(t, h, c, id, 120)
		awaitSelfCheckCount(t, c, id, round)
		awaitArbitration(t, c, id, "abnormal")
	}
}

// 面板重启保留本轮集合、任务 ID 和绝对截止时间，不能另起一轮延长补判。
func TestCheckerArbitrationSurvivesPanelRestart(t *testing.T) {
	h, c := selfCheckBrowser(t)
	gate := make(chan struct{})
	startChecker(t, h, c, 0, 1, gate)
	var ip atomic.Value
	ip.Store("8.8.8.8")
	id, _ := startUnknownTarget(t, h, c, &ip)
	before := pendingArbitration(t, c, id)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	after := pendingArbitration(t, c, id)
	if before["deadline"] != after["deadline"] || before["checkers"].([]any)[0].(map[string]any)["id"] != after["checkers"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("重启改变任务: %v -> %v", before, after)
	}
	close(gate)
	for range 5 {
		h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	awaitArbitration(t, c, id, "abnormal")
}

// 自测参数变更不等于恢复；新配置下未知轮补判也必须保留既有被墙状态。
func TestCheckerUnknownAfterSettingsChangePreservesBlocked(t *testing.T) {
	h, c := selfCheckBrowser(t)
	startChecker(t, h, c, 0, 1, nil)
	created := addNode(c, "配置变化后的未知")
	id := created["id"].(string)
	fake := &domesticSites{}
	c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	h.StartNode(created["installation_ticket"].(string), func(cfg *node.Config, d *node.Dependencies) {
		fake.dependencies(cfg, d)
		d.PublicIPv4 = func(context.Context) (string, error) { return "8.8.8.8", nil }
	})
	awaitSelfCheck(t, c, id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, id, 2, "suspected", 2)
	advanceSelfCheck(t, h, c, id, 3, "blocked", 3)
	// 执行错误保留未知，触发补判汇总对历史状态的重放。
	for _, v := range c.Do("GET", "/api/nodes", nil, 200)["nodes"].([]any) {
		row := v.(map[string]any)
		if row["id"] != id {
			c.Do("PUT", "/api/nodes/"+row["id"].(string)+"/checker", map[string]bool{"enabled": false}, 204)
		}
	}
	startChecker(t, h, c, 1, 3, nil)
	fake.mode.Store(1)
	s := c.Do("GET", "/api/settings/self-check", nil, 200)
	s["connect_timeout_seconds"] = 4
	c.Do("PUT", "/api/settings/self-check", s, 204)
	view := awaitArbitration(t, c, id, "unknown")
	if view["state"] != "blocked" || view["consecutive_abnormal"] != float64(0) {
		t.Fatalf("未知轮错误清除旧状态: %v", view)
	}
}

func awaitSelfCheckCount(t *testing.T, c *clusterharness.Client, id string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)["rounds"].([]any)) >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("未收到第%d轮", count)
}
