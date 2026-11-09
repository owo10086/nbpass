package clusterharness_test

import (
	"context"
	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"net"
	"testing"
	"time"
)

// 若手动动作未预占节点共享额度，或解除冻结清空额度，本场景会失败。
func TestRotationQuotaCountsManualAndSurvivesReleaseAndRestart(t *testing.T) {
	f := newRotationFixture(t, false)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	assertRotationQuota(t, f, 1, 1)
	a := f.finishObservation(t)
	f.c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	awaitRotation(t, f.c, f.path, "released")
	assertRotationQuota(t, f, 1, 1)
	awaitNode(t, f.c, f.id, connected)
	// 手动绕过冷却与一小时次数限制，仍计入额度。
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "observing")
	assertRotationQuota(t, f, 2, 2)
	f.n.Disconnect()
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	assertRotationQuota(t, f, 2, 2)
	f.h.Clock.Advance(24 * time.Hour)
	login(f.c)
	assertRotationQuota(t, f, 0, 0)
}

func TestRotationPreflightCancellationReleasesQuota(t *testing.T) {
	f := newRotationFixture(t, false)
	f.queryFails.Store(true)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, f.c, f.path, "cancelled")
	assertRotationQuota(t, f, 0, 0)
	if f.calls.Load() != 0 {
		t.Fatal("换前失败仍调用商家")
	}
}

func assertRotationQuota(t *testing.T, f *rotationFixture, hour, day int) map[string]any {
	t.Helper()
	v := f.c.Do("GET", f.path, nil, 200)
	q, ok := v["quota"].(map[string]any)
	if !ok || q["used_hour"] != float64(hour) || q["used_day"] != float64(day) {
		t.Fatalf("额度应为 %d/%d: %v", hour, day, v)
	}
	return v
}

func TestRotationPolicyOneUpdatesManagedDNSAndRechecks(t *testing.T) {
	fakeDNS := clusterharness.NewDNSPod()
	fakeDNS.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "223.5.5.5", TTL: 60, Enabled: true})
	telegram := &fakeTelegram{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.DNSProvider = fakeDNS
		d.Telegram = telegram.send
	})
	c := h.Client()
	setup(c)
	login(c)
	c.Do("PUT", "/api/settings/telegram", map[string]any{"enabled": true, "bot_token": telegramToken, "chat_id": telegramChat}, 200)
	settings := c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["jitter_seconds"] = 0
	c.Do("PUT", "/api/settings/self-check", settings, 204)
	sites := &domesticSites{}
	f := rotationFixtureAt(t, h, c, false, sites.dependencies)
	gid := entryGroup(c, f.id)["id"].(string)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "123", "token": "private"}, 201)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account["id"].(string)), 200)
	c.Do("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": f.id, "ipv4": "223.5.5.5", "confirm_inbound": true}, 202)
	awaitDNSStage(t, c, gid, panel.EntryWritten)
	c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, h, c, f.id, 3, "blocked", 3)
	tickPolicy(t, f)
	a := awaitRotation(t, c, f.path, "observing")
	if a["origin"] != "automatic" {
		t.Fatalf("不是自动授权: %v", a)
	}
	sites.mode.Store(2)
	f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, f, time.Minute)
	awaitRotation(t, c, f.path, "changed")
	v := awaitRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	if v["no_improvement"] != float64(0) {
		t.Fatalf("复检恢复未清零: %v", v)
	}
	awaitDNSStage(t, c, gid, panel.EntryWritten)
	pub := publication(c, gid)
	if pub["expected_value"] != "8.8.8.8" || fakeDNS.Record("example.com", "100").Value != "8.8.8.8" {
		t.Fatalf("未按新 IP 更新: %v", pub)
	}
	assertRotationQuota(t, f, 1, 1)
	for _, kind := range []string{"ip_rotation_authorized", "ip_rotation_changed", "ip_rotation_recovered"} {
		if len(c.Do("GET", "/api/events?node_id="+f.id+"&type="+kind, nil, 200)["events"].([]any)) == 0 {
			t.Fatalf("缺少事件 %s", kind)
		}
	}
	tickPolicy(t, f)
	_, sent := telegram.snapshot()
	if len(sent) == 0 {
		t.Fatal("没有通知")
	}
}

func tickPolicy(t *testing.T, f *rotationFixture) {
	t.Helper()
	awaitControlIdle(t, f.h)
	f.h.Clock.Advance(time.Second)
}
func advanceHealthyRotation(t *testing.T, f *rotationFixture, d time.Duration) {
	t.Helper()
	for d > 0 {
		step := min(d, 10*time.Second)
		awaitControlIdle(t, f.h)
		before := nodeRow(t, f.c, f.id)["last_seen_at"]
		f.h.Clock.Advance(step)
		if step == 10*time.Second {
			awaitNode(t, f.c, f.id, func(v map[string]any) bool { return v["last_seen_at"] != before })
		}
		d -= step
	}
}

func TestRotationRecheckThreeStatesAndPause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  int32
		want  string
		count float64
	}{{"异常", 0, "no_improvement", 1}, {"未知", 1, "pending", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := selfCheckBrowser(t)
			settings := c.Do("GET", "/api/settings/self-check", nil, 200)
			settings["interval_seconds"] = 30
			settings["round_timeout_seconds"] = 30
			c.Do("PUT", "/api/settings/self-check", settings, 204)
			sites := &domesticSites{}
			sites.mode.Store(tc.mode)
			f := rotationFixtureAt(t, h, c, false, sites.dependencies)
			for attempt := 1; attempt <= 2; attempt++ {
				f.c.Do("POST", f.path+"/actions", nil, 202)
				awaitRotation(t, c, f.path, "observing")
				ip := "8.8.8.8"
				if attempt == 2 {
					ip = "1.1.1.1"
				}
				f.ip.Store(ip)
				advanceHealthyRotation(t, f, time.Minute)
				awaitRecheckRounds(t, f, 1)
				for n := 2; n <= 3; n++ {
					advanceHealthyRotation(t, f, 30*time.Second)
					awaitRecheckRounds(t, f, n)
				}
				v := awaitRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == tc.want })
				if v["no_improvement"] != float64(attempt)*tc.count || v["paused"] != (attempt == 2 && tc.count == 1) {
					t.Fatalf("复检/暂停判定: %v", v)
				}
			}
			assertRotationQuota(t, f, 2, 2)
			if tc.count == 1 {
				events := c.Do("GET", "/api/events?node_id="+f.id+"&type=ip_rotation_paused", nil, 200)["events"].([]any)
				if len(events) == 0 {
					tickPolicy(t, f)
					events = c.Do("GET", "/api/events?node_id="+f.id+"&type=ip_rotation_paused", nil, 200)["events"].([]any)
				}
				if len(events) != 1 {
					t.Fatalf("缺少暂停事件: %v", events)
				}
				gid := entryGroup(c, f.id)["id"].(string)
				c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
				c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
				c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
				awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
				advanceHealthyRotation(t, f, 30*time.Second)
				awaitSelfCheck(t, c, f.id, 2, "suspected", 2)
				advanceHealthyRotation(t, f, 30*time.Second)
				awaitSelfCheck(t, c, f.id, 3, "blocked", 3)
				f.n.Disconnect()
				h.Clock.Advance(2 * time.Hour)
				login(c)
				f.n.Restore()
				f.n.Restart()
				awaitNode(t, c, f.id, connected)
				tickPolicy(t, f)
				if f.calls.Load() != 2 {
					t.Fatal("无改善暂停仍自动执行")
				}
				c.Do("POST", f.path+"/resume", map[string]bool{"confirm": true}, 200)
				v := assertRotationQuota(t, f, 0, 2)
				if v["paused"] != false {
					t.Fatal("未解除暂停")
				}
				tickPolicy(t, f)
				awaitRotation(t, c, f.path, "observing")
				assertRotationQuota(t, f, 1, 3)
			}
		})
	}
}

func TestRotationLaterNormalSelfCheckClearsPause(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		supplemented, delayed bool
	}{{"原始正常", false, false}, {"补判正常", true, false}, {"正常补判晚于异常轮", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := selfCheckBrowser(t)
			settings := c.Do("GET", "/api/settings/self-check", nil, 200)
			settings["interval_seconds"] = 30
			settings["round_timeout_seconds"] = 30
			c.Do("PUT", "/api/settings/self-check", settings, 204)
			sites := &domesticSites{}
			f := rotationFixtureAt(t, h, c, false, sites.dependencies)
			for _, ip := range []string{"1.1.1.1", "8.8.8.8"} {
				c.Do("POST", f.path+"/actions", nil, 202)
				awaitRotation(t, c, f.path, "observing")
				f.ip.Store(ip)
				advanceHealthyRotation(t, f, time.Minute)
				awaitRecheckRounds(t, f, 1)
				for n := 2; n <= 3; n++ {
					advanceHealthyRotation(t, f, 30*time.Second)
					awaitRecheckRounds(t, f, n)
				}
			}
			if c.Do("GET", f.path, nil, 200)["paused"] != true {
				t.Fatal("未暂停")
			}
			var gate chan struct{}
			ready := make(chan struct{})
			if tc.delayed {
				gate = make(chan struct{})
				defer func() {
					if gate != nil {
						close(gate)
					}
				}()
			}
			if tc.supplemented {
				resultGate := gate
				startChecker(t, h, c, 0, 0, nil, func(_ *node.Config, d *node.Dependencies) {
					dial := d.CheckerDial
					d.CheckerDial = func(ctx context.Context, address string) (net.Conn, error) {
						conn, err := dial(ctx, address)
						if err == nil && resultGate != nil && address == "8.8.8.8:18080" {
							// TCP 已成功后暂缓交付结果，保持连接超时的真实语义。
							conn = &checkerResultGate{Conn: conn, gate: resultGate, ready: ready}
						}
						return conn, err
					}
				})
				sites.mode.Store(1)
			} else {
				sites.mode.Store(2)
			}
			c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
			if tc.delayed {
				pendingArbitration(t, c, f.id)
				select {
				case <-ready:
				case <-time.After(5 * time.Second):
					t.Fatal("检测点未完成成功连接")
				}
				sites.mode.Store(0)
				advanceHealthyRotation(t, f, 30*time.Second)
				awaitSelfCheckCount(t, c, f.id, 2)
				close(gate)
				gate = nil
				awaitRotationView(t, f, func(v map[string]any) bool { return v["paused"] == false })
				view := c.Do("GET", "/api/nodes/"+f.id+"/self-check", nil, 200)
				rounds := view["rounds"].([]any)
				if view["state"] != "suspected" || rounds[0].(map[string]any)["final_result"] != "abnormal" || rounds[1].(map[string]any)["final_result"] != "normal" {
					t.Fatalf("未保留正常补判之后的异常轮: %v", view)
				}
			} else if tc.supplemented {
				awaitArbitration(t, c, f.id, "normal")
			} else {
				awaitSelfCheck(t, c, f.id, 1, "normal", 0)
			}
			v := assertRotationQuota(t, f, 2, 2)
			if v["paused"] != false || v["no_improvement"] != float64(0) {
				t.Fatalf("正常自测未解除暂停: %v", v)
			}
		})
	}
}

type checkerResultGate struct {
	net.Conn
	gate  <-chan struct{}
	ready chan<- struct{}
}

func (c *checkerResultGate) Close() error {
	close(c.ready)
	<-c.gate
	return c.Conn.Close()
}

func awaitRecheckRounds(t *testing.T, f *rotationFixture, count int) map[string]any {
	t.Helper()
	return awaitRotationView(t, f, func(v map[string]any) bool {
		a := v["action"].(map[string]any)
		rounds, _ := a["recheck_rounds"].([]any)
		return len(rounds) == count
	})
}

func TestRotationRecheckRestartAndUnknownThenNormal(t *testing.T) {
	h, c := selfCheckBrowser(t)
	settings := c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["interval_seconds"] = 30
	settings["round_timeout_seconds"] = 30
	c.Do("PUT", "/api/settings/self-check", settings, 204)
	sites := &domesticSites{}
	sites.mode.Store(1)
	f := rotationFixtureAt(t, h, c, false, sites.dependencies)
	c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, c, f.path, "observing")
	f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, f, time.Minute)
	v := awaitRecheckRounds(t, f, 1)
	id := v["action"].(map[string]any)["id"]
	c.Do("POST", f.path+"/actions", nil, 409)
	f.n.Disconnect()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = h.Client()
	login(f.c)
	f.n.Restore()
	f.n.Restart()
	awaitNode(t, f.c, f.id, connected)
	sites.mode.Store(2)
	advanceHealthyRotation(t, f, 30*time.Second)
	v = awaitRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	a := v["action"].(map[string]any)
	if a["id"] != id || len(a["recheck_rounds"].([]any)) != 2 || f.calls.Load() != 1 || v["no_improvement"] != float64(0) {
		t.Fatalf("重启续检或任一正常即恢复错误: %v", v)
	}
}

func TestRotationSharedQuotaBlocksAutomaticUntilRollingDayExpires(t *testing.T) {
	h, c := selfCheckBrowser(t)
	sites := &domesticSites{}
	sites.mode.Store(2)
	f := rotationFixtureAt(t, h, c, false, sites.dependencies)
	firstAt := h.Clock.Now()
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"} {
		c.Do("POST", f.path+"/actions", nil, 202)
		awaitRotation(t, c, f.path, "observing")
		f.ip.Store(ip)
		advanceHealthyRotation(t, f, time.Minute)
		awaitRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	}
	gid := entryGroup(c, f.id)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	sites.mode.Store(0)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, h, c, f.id, 3, "blocked", 3)
	tickPolicy(t, f)
	assertRotationQuota(t, f, 3, 3)
	if f.calls.Load() != 3 {
		t.Fatal("冷却内自动执行了第四次")
	}
	f.n.Disconnect()
	awaitNode(t, c, f.id, func(v map[string]any) bool { return v["connected"] == false })
	h.Clock.Advance(firstAt.Add(32 * time.Minute).Sub(h.Clock.Now()))
	v := assertRotationQuota(t, f, 3, 3)
	q := v["quota"].(map[string]any)
	if q["cooldown_seconds"] != float64(0) || q["retry_after_seconds"].(float64) <= 0 {
		t.Fatalf("冷却结束仍须受滚动窗口约束: %v", q)
	}
	h.Clock.Advance(31 * time.Minute)
	v = assertRotationQuota(t, f, 0, 3)
	f.n.Restore()
	f.n.Restart()
	awaitNode(t, c, f.id, connected)
	tickPolicy(t, f)
	if f.calls.Load() != 3 {
		t.Fatal("24小时内第四次自动换 IP 未拦截")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = h.Client()
	login(f.c)
	assertRotationQuota(t, f, 0, 3)
	f.n.Disconnect()
	h.Clock.Advance(firstAt.Add(24 * time.Hour).Sub(h.Clock.Now()))
	login(f.c)
	// 恰好24小时释放首笔；后两笔仍保留。
	assertRotationQuota(t, f, 0, 2)
	if f.calls.Load() != 3 {
		t.Fatal("离线节点仍获许可")
	}
	f.n.Restore()
	f.n.Restart()
	awaitNode(t, f.c, f.id, connected)
	tickPolicy(t, f)
	awaitRotation(t, f.c, f.path, "observing")
	assertRotationQuota(t, f, 1, 3)
	if f.calls.Load() != 4 {
		t.Fatal("窗口释放后自动换 IP 未恢复")
	}
}

func TestAutomaticRotationRejectsUnsafePreflight(t *testing.T) {
	h, c := selfCheckBrowser(t)
	sites := &domesticSites{}
	f := rotationFixtureAt(t, h, c, false, sites.dependencies, func(_ *node.Config, d *node.Dependencies) {
		d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{"10.0.0.1"}, nil }
	})
	gid := entryGroup(c, f.id)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, h, c, f.id, 3, "blocked", 3)
	tickPolicy(t, f)
	awaitRotation(t, c, f.path, "cancelled")
	if f.calls.Load() != 0 {
		t.Fatal("NAT/多IP 首次自动预检仍调用了商家")
	}
	v := assertRotationQuota(t, f, 0, 0)
	if v["unsafe_network"] != true {
		t.Fatal("网络风险未持久化")
	}
}

func TestRotationUnfreezeResumesAutomaticWithoutClearingQuota(t *testing.T) {
	h, c := selfCheckBrowser(t)
	sites := &domesticSites{}
	f := rotationFixtureAt(t, h, c, false, sites.dependencies)
	c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, c, f.path, "observing")
	for i := 1; i <= 5; i++ {
		advanceHealthyRotation(t, f, time.Minute)
		awaitRotationView(t, f, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(i) })
	}
	awaitRotation(t, c, f.path, "unconfirmed")
	gid := entryGroup(c, f.id)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, h, c, f.id, 3, "blocked", 3)
	f.n.Disconnect()
	h.Clock.Advance(2 * time.Hour)
	login(c)
	f.n.Restore()
	f.n.Restart()
	awaitNode(t, c, f.id, connected)
	tickPolicy(t, f)
	if f.calls.Load() != 1 {
		t.Fatal("未解除冻结仍自动调用")
	}
	c.Do("POST", f.path+"/unfreeze", map[string]any{"action_id": a["id"]}, 202)
	awaitRotation(t, c, f.path, "released")
	assertRotationQuota(t, f, 0, 1)
	tickPolicy(t, f)
	awaitRotation(t, c, f.path, "observing")
	assertRotationQuota(t, f, 1, 2)
	if f.calls.Load() != 2 {
		t.Fatal("解除冻结后自动流程未恢复")
	}
}
