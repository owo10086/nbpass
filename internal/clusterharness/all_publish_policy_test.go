package clusterharness_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/dnswatch"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

type allPolicyRig struct {
	h            *clusterharness.Harness
	c            *clusterharness.Client
	dns          *clusterharness.DNSPod
	telegram     *fakeTelegram
	g, account   string
	ids          []string
	nodes        []*clusterharness.Node
	sites        []*domesticSites
	ips          []*atomic.Value
	listenFailed []*atomic.Bool
}

func newAllPolicyRig(t *testing.T, count int, options ...func(*panel.Dependencies)) *allPolicyRig {
	return newAllPolicyRigWithNodes(t, count, nil, options...)
}

func newAllPolicyRigWithNodes(t *testing.T, count int, nodeOptions func(*node.Config, *node.Dependencies), options ...func(*panel.Dependencies)) *allPolicyRig {
	t.Helper()
	f := &allPolicyRig{dns: clusterharness.NewDNSPod(), telegram: &fakeTelegram{}}
	f.h, f.c = telegramBrowser(t, f.telegram, append([]func(*panel.Dependencies){func(d *panel.Dependencies) { d.DNSProvider = f.dns }}, options...)...)
	settings := f.c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["interval_seconds"], settings["jitter_seconds"], settings["round_timeout_seconds"] = 30, 0, 20
	f.c.Do("PUT", "/api/settings/self-check", settings, 204)
	for i := range count {
		created := addNode(f.c, fmt.Sprintf("成员%d", i+1))
		id := created["id"].(string)
		ip := &atomic.Value{}
		ip.Store([]string{"8.8.8.8", "1.1.1.1", "2.2.2.2"}[i])
		sites := &domesticSites{}
		listenFailed := &atomic.Bool{}
		sites.mode.Store(2)
		n := f.h.StartNode(created["installation_ticket"].(string), sites.dependencies, func(_ *node.Config, d *node.Dependencies) {
			d.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
			d.Forwarding.Listen = func(network, _ string) (net.Listener, error) {
				if listenFailed.Load() {
					return nil, errors.New("测试环境端口已占用")
				}
				return net.Listen(network, "127.0.0.1:0")
			}
			if nodeOptions != nil {
				nodeOptions(nil, d)
			}
		})
		awaitNode(t, f.c, id, connected)
		f.ids, f.nodes, f.sites, f.ips = append(f.ids, id), append(f.nodes, n), append(f.sites, sites), append(f.ips, ip)
		f.listenFailed = append(f.listenFailed, listenFailed)
		f.dns.Set("example.com", dnsprovider.Record{ID: fmt.Sprint(100 + i), Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 60, Enabled: false})
	}
	f.g = allGroup(f.c, f.ids...)
	f.account = f.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest123", "secret_key": "testSecretKey"}, 201)["id"].(string)
	for i, id := range f.ids {
		bindMember(f.c, f.g, id, f.account, fmt.Sprint(100+i), 200)
	}
	return f
}

func (f *allPolicyRig) publish(t *testing.T) {
	t.Helper()
	ips := map[string]string{}
	for i, id := range f.ids {
		ips[id] = f.ips[i].Load().(string)
	}
	publishAll(f.c, f.g, ips, 202)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
}

func allPolicyMember(t *testing.T, c *clusterharness.Client, g, id string) map[string]any {
	t.Helper()
	for _, raw := range publication(c, g)["members"].([]any) {
		m := raw.(map[string]any)
		if m["node_id"] == id {
			return m
		}
	}
	t.Fatal("缺少成员发布状态")
	return nil
}

func (f *allPolicyRig) action(index int, kind string, status int) map[string]any {
	return f.c.Do("POST", "/api/entry-groups/"+f.g+"/members/"+f.ids[index]+"/"+kind, map[string]any{"expected_version": publication(f.c, f.g)["desired_version"]}, status)
}

func (f *allPolicyRig) samples(t *testing.T, count int) {
	t.Helper()
	for range count {
		sampleEntryState(t, f.h, f.c, f.g, f.ids[0])
	}
}

func (f *allPolicyRig) awaitEnabled(t *testing.T, index int, enabled bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m := allPolicyMember(t, f.c, f.g, f.ids[index])
		if m["expected_enabled"] == enabled && m["record"].(map[string]any)["current_enabled"] == enabled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("成员启用状态未达到 %v：%v", enabled, m)
		}
		time.Sleep(time.Millisecond)
	}
	if f.dns.Record("example.com", fmt.Sprint(100+index)).Enabled != enabled {
		t.Fatal("面板确认与服务商状态不符")
	}
}

func (f *allPolicyRig) disconnect(t *testing.T, indices ...int) {
	t.Helper()
	for _, i := range indices {
		f.nodes[i].Disconnect()
		awaitNode(t, f.c, f.ids[i], func(n map[string]any) bool { return n["connected"] == false })
	}
	f.samples(t, 10)
}

// 缺失或无效的检测点证据不能当作不可达，未就绪本身也不触发摘除。
func TestAllPublishOfflineWithdrawalRequiresValidCheckerEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     int
		outcome  string
		withdraw bool
	}{
		{"确认不可达", 1, "unreachable", true}, {"仍可达", 0, "reachable", false}, {"没有检测点", -1, "no_checkers", false}, {"对照失败", 2, "unknown", false}, {"执行失败", 3, "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAllPolicyRig(t, 2)
			f.publish(t)
			writes := f.dns.Writes()
			if tc.mode >= 0 {
				startChecker(t, f.h, f.c, 0, tc.mode, nil)
			}
			f.disconnect(t, 0)
			awaitOfflineCheck(t, &offlineEntryRig{h: f.h, c: f.c, group: f.g}, tc.outcome)
			awaitControlIdle(t, f.h)
			f.h.Clock.Advance(time.Second)
			if tc.withdraw {
				f.awaitEnabled(t, 0, false)
				m := allPolicyMember(t, f.c, f.g, f.ids[0])
				if !strings.Contains(m["reason"].(string), "离线") || m["withdrawn_at"] == nil || m["manual_locked"] != false {
					t.Fatalf("缺少自动摘除信息：%v", m)
				}
				f.awaitEnabled(t, 1, true)
			} else {
				f.samples(t, 2)
				if f.dns.Writes() != writes {
					t.Fatal("没有不可达确认仍摘除")
				}
			}
		})
	}
}

// 独立上报不能因其他成员摘除而重测；既有暂停成员不得用于补足配额。
func TestAllPublishMinimumKeptWithIndependentCheckerReports(t *testing.T) {
	for _, minimum := range []int{1, 9} {
		t.Run(fmt.Sprint(minimum), func(t *testing.T) {
			f := newAllPolicyRig(t, 3)
			f.publish(t)
			f.c.Do("PUT", "/api/entry-groups/"+f.g+"/minimum-kept", map[string]int{"minimum_kept": minimum}, 200)
			startChecker(t, f.h, f.c, 0, 1, nil, allOfflineChecker(nil))
			f.disconnect(t, 0, 1, 2)
			for i := range 3 {
				f.offlineCheck(t, i, "unreachable")
			}
			awaitControlIdle(t, f.h)
			f.h.Clock.Advance(time.Second)
			waitOfflineEvents(t, f.c, "?type=entry_member_kept", minimum/9*2+1)
			awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
			var kept, paused []int
			for i := range 3 {
				if allPolicyMember(t, f.c, f.g, f.ids[i])["expected_enabled"] == true {
					kept = append(kept, i)
				} else {
					paused = append(paused, i)
				}
			}
			want := min(minimum, 3)
			if len(kept) != want {
				t.Fatalf("独立报告后应保留 %d 条，实际保留 %v、摘除 %v", want, kept, paused)
			}
			for _, i := range kept {
				f.awaitEnabled(t, i, true)
			}
			for _, i := range paused {
				f.awaitEnabled(t, i, false)
			}
			if minimum == 1 {
				f.c.Do("PUT", "/api/entry-groups/"+f.g+"/minimum-kept", map[string]int{"minimum_kept": 9}, 200)
				f.samples(t, 7)
				for _, i := range paused {
					f.awaitEnabled(t, i, false)
				}
			}
		})
	}
}

// 防止手动锁丢失、保留数错误限制手动操作，或允许清空启用集合。
func TestAllPublishManualWithdrawalLockRestoreAndMinimumSetting(t *testing.T) {
	f := newAllPolicyRig(t, 3)
	base := "/api/entry-groups/" + f.g
	f.action(1, "restore", 409)
	for _, n := range []int{0, -1} {
		f.c.Do("PUT", base+"/minimum-kept", map[string]int{"minimum_kept": n}, 400)
	}
	f.c.Do("PUT", base+"/minimum-kept", map[string]int{"minimum_kept": 9}, 200)
	if groupRow(t, f.c, f.g)["minimum_kept"] != float64(9) {
		t.Fatal("最少保留数未保存")
	}
	f.publish(t)
	f.action(2, "withdraw", 202)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	m := allPolicyMember(t, f.c, f.g, f.ids[2])
	if m["manual_locked"] != true || m["withdrawn_at"] == nil || f.dns.Record("example.com", "102").Enabled {
		t.Fatalf("手动摘除未锁定或未记录时间：%v", m)
	}
	f.action(1, "withdraw", 202)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	f.action(0, "withdraw", 409)
	// 即使没有健康证据，手动加回也应立即启用并解锁。
	f.action(2, "restore", 202)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	m = allPolicyMember(t, f.c, f.g, f.ids[2])
	if m["manual_locked"] != false || !f.dns.Record("example.com", "102").Enabled {
		t.Fatalf("手动加回没有立即启用并解锁：%v", m)
	}
	f.h.Clock.Advance(time.Second)
	waitOfflineEvents(t, f.c, "?type=entry_member_withdrawn", 2)
	waitOfflineEvents(t, f.c, "?type=entry_member_restored", 1)
	waitTelegramStatus(t, f.c, func(s map[string]any) bool { return s["pending"] == float64(0) && s["reachability"] == "reachable" })
	_, messages := f.telegram.snapshot()
	var withdrawal, restoration bool
	for _, m := range messages {
		withdrawal = withdrawal || strings.Contains(m.Text, "手动摘除")
		restoration = restoration || strings.Contains(m.Text, "手动加回")
	}
	if !withdrawal || !restoration {
		t.Fatal("摘除或加回未通知 Telegram")
	}
}

func (f *allPolicyRig) healthy(t *testing.T, index int) {
	t.Helper()
	id := f.ids[index]
	f.sites[index].mode.Store(2)
	f.c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": false}, 204)
	disabled := awaitRevision(t, f.c, id, 0)
	f.c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	revision := awaitRevision(t, f.c, id, disabled)
	// 旧轮次可能在停用前已经执行、稍后才上报；必须等本次启用配置的成功证据。
	deadline := time.Now().Add(15 * time.Second)
	for {
		v := f.c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)
		rounds := v["rounds"].([]any)
		if len(rounds) > 0 {
			latest := rounds[0].(map[string]any)
			if latest["config_revision"] == revision && latest["final_result"] == "normal" && v["state"] == "normal" && v["consecutive_abnormal"] == float64(0) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("未收到当前配置的正常自测：%v", v)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 关闭后保留真实成功证据，避免测试快进同时触发额外自测轮次。
	f.c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": false}, 204)
	awaitRevision(t, f.c, id, 0)
}

func (f *allPolicyRig) publishWithPausedSecond(t *testing.T) {
	t.Helper()
	f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": true}, 204)
	f.publish(t)
	f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": false}, 204)
	f.awaitEnabled(t, 1, false)
}

// 捕获用时间流逝或页面读取替代样本、样本中断不清零，以及手动锁失效。
func TestAllPublishRestoreRequiresSixFreshChecksAndHonorsManualLock(t *testing.T) {
	for _, interruption := range []string{"无中断", "维护", "采样间维护", "证据过期", "缺少检查", "重启", "未知地址"} {
		t.Run(interruption, func(t *testing.T) {
			f := newAllPolicyRig(t, 2)
			f.publishWithPausedSecond(t)
			f.healthy(t, 1)
			if interruption == "证据过期" {
				f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": true}, 204)
				f.samples(t, 24)
				f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": false}, 204)
			}
			f.samples(t, 5)
			f.awaitEnabled(t, 1, false)
			for range 5 {
				publication(f.c, f.g)
				entryState(t, f.c, f.g, f.ids[1])
			}
			if interruption != "无中断" {
				switch interruption {
				case "维护":
					f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": true}, 204)
					f.samples(t, 1)
					f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": false}, 204)
				case "采样间维护":
					f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": true}, 204)
					f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": false}, 204)
				case "证据过期":
					f.samples(t, 1)
					// 等待这个过期样本，恢复证据之前不得启用。
					deadline := time.Now().Add(5 * time.Second)
					for !hasEntryReason(entryState(t, f.c, f.g, f.ids[1]), "candidate_reasons", "domestic_evidence_expired") {
						if time.Now().After(deadline) {
							t.Fatal("证据没有过期")
						}
						time.Sleep(time.Millisecond)
					}
					f.healthy(t, 1)
				case "缺少检查":
					awaitControlIdle(t, f.h)
					f.h.Clock.Advance(60 * time.Second)
					deadline := time.Now().Add(5 * time.Second)
					for entryState(t, f.c, f.g, f.ids[1])["checked_at"] != f.h.Clock.Now().Format(time.RFC3339Nano) {
						if time.Now().After(deadline) {
							t.Fatal("缺样后的检查未完成")
						}
						time.Sleep(time.Millisecond)
					}
					f.samples(t, 4)
					f.awaitEnabled(t, 1, false)
					f.samples(t, 1)
					f.awaitEnabled(t, 1, true)
					return
				case "重启":
					if err := f.h.Restart(); err != nil {
						t.Fatal(err)
					}
					for _, n := range f.nodes {
						n.Restart()
					}
					for _, id := range f.ids {
						awaitNode(t, f.c, id, connected)
					}
				case "未知地址":
					f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": true}, 204)
					f.ips[1].Store("4.2.2.2")
					f.samples(t, 1)
					awaitNode(t, f.c, f.ids[1], func(n map[string]any) bool { return n["public_ipv4"] == "4.2.2.2" })
					f.c.Do("PUT", "/api/nodes/"+f.ids[1]+"/maintenance", map[string]bool{"enabled": false}, 204)
					f.samples(t, 1)
					f.awaitEnabled(t, 1, false)
					f.ips[1].Store("1.1.1.1")
					f.samples(t, 1)
					awaitNode(t, f.c, f.ids[1], func(n map[string]any) bool { return n["public_ipv4"] == "1.1.1.1" })
					f.healthy(t, 1)
				}
				f.samples(t, 5)
				f.awaitEnabled(t, 1, false)
			}
			f.samples(t, 1)
			f.awaitEnabled(t, 1, true)
			f.action(1, "withdraw", 202)
			awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
			f.samples(t, 7)
			f.awaitEnabled(t, 1, false)
			if allPolicyMember(t, f.c, f.g, f.ids[1])["manual_locked"] != true {
				t.Fatal("手动锁丢失")
			}
			f.action(1, "restore", 202)
			awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
			f.awaitEnabled(t, 1, true)
		})
	}
}

// 外部地址变化不允许把旧记录直接加回；同步后必须从新地址重新累计。
func TestAllPublishRestoreAddressMismatchRequiresSyncAndNewSamples(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publishWithPausedSecond(t)
	f.ips[1].Store("4.2.2.2")
	f.samples(t, 1)
	awaitNode(t, f.c, f.ids[1], func(n map[string]any) bool { return n["public_ipv4"] == "4.2.2.2" })
	f.healthy(t, 1)
	f.samples(t, 7)
	f.awaitEnabled(t, 1, false)
	waitOfflineEvents(t, f.c, "?type=entry_member_address_mismatch", 1)
	m := allPolicyMember(t, f.c, f.g, f.ids[1])
	if !strings.Contains(m["reason"].(string), "同步") {
		t.Fatalf("缺少同步提示：%v", m)
	}
	f.c.Do("POST", "/api/nodes/"+f.ids[1]+"/sync-address", map[string]any{"ipv4": "4.2.2.2", "confirm_inbound": true}, 200)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	if f.dns.Record("example.com", "101").Value != "4.2.2.2" {
		t.Fatal("同步没有改记录地址")
	}
	f.samples(t, 5)
	f.awaitEnabled(t, 1, false)
	f.samples(t, 1)
	f.awaitEnabled(t, 1, true)
}

func TestAllPublishMinimumShortageOnlyNotifiesAndDoesNotRestore(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publishWithPausedSecond(t)
	writes := f.dns.Writes()
	f.c.Do("PUT", "/api/entry-groups/"+f.g+"/minimum-kept", map[string]int{"minimum_kept": 2}, 200)
	f.samples(t, 1)
	waitOfflineEvents(t, f.c, "?type=entry_member_minimum_unmet", 1)
	if f.dns.Writes() != writes {
		t.Fatal("为凑最少保留数修改了记录")
	}
	f.awaitEnabled(t, 1, false)
}

func TestAllPublishRepeatedShortageNotifiesEachIncident(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publish(t)
	f.c.Do("PUT", "/api/entry-groups/"+f.g+"/minimum-kept", map[string]int{"minimum_kept": 2}, 200)
	for cycle := 1; cycle <= 2; cycle++ {
		f.action(1, "withdraw", 202)
		awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
		f.samples(t, 1)
		waitOfflineEvents(t, f.c, "?type=entry_member_minimum_unmet", cycle)
		f.action(1, "restore", 202)
		awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
		f.samples(t, 1)
	}
}

func TestAllPublishMinimumSettingStartsNewShortageIncident(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publishWithPausedSecond(t)
	for cycle := 1; cycle <= 2; cycle++ {
		f.c.Do("PUT", "/api/entry-groups/"+f.g+"/minimum-kept", map[string]int{"minimum_kept": 2}, 200)
		f.samples(t, 1)
		waitOfflineEvents(t, f.c, "?type=entry_member_minimum_unmet", cycle)
		f.c.Do("PUT", "/api/entry-groups/"+f.g+"/minimum-kept", map[string]int{"minimum_kept": 1}, 200)
		f.samples(t, 1)
	}
}

func TestAllPublishReadinessFailureResetsRestoreChecks(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publishWithPausedSecond(t)
	f.healthy(t, 1)
	f.samples(t, 5)
	f.listenFailed[1].Store(true)
	rule := f.c.Do("POST", "/api/rules", directBody(f.g, 29001, "127.0.0.1", 12345), 201)
	awaitRule(t, f.c, rule["id"].(string), "同步失败")
	f.samples(t, 1)
	f.awaitEnabled(t, 1, false)
	f.c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
	awaitRevision(t, f.c, f.ids[1], 0)
	f.samples(t, 5)
	f.awaitEnabled(t, 1, false)
	f.samples(t, 1)
	f.awaitEnabled(t, 1, true)
}

func (f *allPolicyRig) block(t *testing.T, index int) {
	t.Helper()
	id := f.ids[index]
	f.healthy(t, index)
	count := len(f.c.Do("GET", "/api/nodes/"+id+"/self-check", nil, 200)["rounds"].([]any))
	f.sites[index].mode.Store(0)
	f.c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, f.c, id, count+1, "suspected", 1)
	for n := 2; n <= 3; n++ {
		f.samples(t, 3)
		state := "suspected"
		if n == 3 {
			state = "blocked"
		}
		awaitSelfCheck(t, f.c, id, count+n, state, n)
	}
	f.samples(t, 1)
}

func TestAllPublishBlockedWithdrawalAndSingleMemberRetention(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := newAllPolicyRig(t, count)
			f.publish(t)
			f.block(t, 0)
			if count == 1 {
				f.awaitEnabled(t, 0, true)
				waitOfflineEvents(t, f.c, "?type=entry_member_kept", 1)
				f.samples(t, 6)
				waitOfflineEvents(t, f.c, "?type=entry_member_kept", 1)
			} else {
				f.awaitEnabled(t, 0, false)
				m := allPolicyMember(t, f.c, f.g, f.ids[0])
				if !strings.Contains(m["reason"].(string), "被墙") {
					t.Fatalf("摘除原因错误：%v", m)
				}
				f.healthy(t, 0)
				f.samples(t, 5)
				f.awaitEnabled(t, 0, false)
				f.samples(t, 1)
				f.awaitEnabled(t, 0, true)
			}
		})
	}
}

func TestAllPublishNotReadyDoesNotWithdraw(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publish(t)
	// 通过节点外部监听依赖制造端口冲突，不替换就绪判断。
	f.listenFailed[0].Store(true)
	body := directBody(f.g, 29001, "127.0.0.1", 12345)
	rule := f.c.Do("POST", "/api/rules", body, 201)
	awaitRule(t, f.c, rule["id"].(string), "同步失败")
	awaitRevision(t, f.c, f.ids[1], 0)
	state := sampleEntryState(t, f.h, f.c, f.g, f.ids[0])
	if state["ready"] != false {
		t.Fatalf("未制造未就绪：%v", state)
	}
	writes := f.dns.Writes()
	f.samples(t, 7)
	if f.dns.Writes() != writes {
		t.Fatal("仅未就绪触发摘除")
	}
	f.awaitEnabled(t, 0, true)
	f.awaitEnabled(t, 1, true)
}

func TestAllPublishNewBoundMemberCanRestoreAfterSixChecks(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	// 先仅发布首个成员，再把真实在线、地址已确认的第二成员加入。
	f.c.Do("DELETE", "/api/entry-groups/"+f.g+"/members/"+f.ids[1], nil, 202)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	deadline := time.Now().Add(5 * time.Second)
	for len(publication(f.c, f.g)["members"].([]any)) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("成员未移出")
		}
		time.Sleep(time.Millisecond)
	}
	f.c.Do("POST", "/api/nodes/"+f.ids[1]+"/sync-address", map[string]any{"ipv4": "1.1.1.1", "confirm_inbound": true}, 200)
	publishAll(f.c, f.g, map[string]string{f.ids[0]: "8.8.8.8"}, 202)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	f.c.Do("PUT", "/api/entry-groups/"+f.g, map[string]any{"name": "新增成员", "members": f.ids}, 200)
	r := f.dns.Record("example.com", "101")
	r.Value = "1.1.1.1"
	f.dns.Set("example.com", r)
	bindMember(f.c, f.g, f.ids[1], f.account, "101", 200)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	f.healthy(t, 1)
	f.samples(t, 5)
	f.awaitEnabled(t, 1, false)
	f.samples(t, 1)
	f.awaitEnabled(t, 1, true)
}

func TestAllPublishAutomaticRestoreTimeoutRestartReadsBeforeRetry(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publishWithPausedSecond(t)
	f.healthy(t, 1)
	var lose atomic.Bool
	lose.Store(true)
	f.dns.Status = func(context.Context) error {
		if lose.CompareAndSwap(true, false) {
			return errors.New("响应丢失")
		}
		return nil
	}
	f.samples(t, 6)
	a := awaitDNSStage(t, f.c, f.g, panel.EntryUnconfirmed)
	writes := f.dns.Writes()
	r := f.dns.Record("example.com", "101")
	r.ID = "102"
	f.dns.Set("example.com", r)
	bindMember(f.c, f.g, f.ids[1], f.account, "102", 409)
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	confirmed := awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	if confirmed["id"] != a["id"] || f.dns.Writes() != writes {
		t.Fatal("重启未先核对原动作或重发了启用")
	}
	f.awaitEnabled(t, 1, true)
	f.c.Do("POST", "/api/entry-groups/"+f.g+"/members/"+f.ids[1]+"/withdraw", map[string]int{"expected_version": 1}, 409)
}

func TestAllPublishAutomaticRestoreExpiryDuringSubmissionDoesNotWrite(t *testing.T) {
	random := &offlineActionRandom{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	defer once.Do(func() { close(random.release) })
	f := newAllPolicyRig(t, 2, func(d *panel.Dependencies) {
		random.base = d.Random
		d.Random = random
		// 完成首发传播，让提交停顿只作用于动作 ID，不被下一轮解析任务先消费。
		d.DNSWatchLookup = func(context.Context, string, dnswatch.Resolver) ([]string, error) { return []string{"8.8.8.8"}, nil }
	})
	f.publishWithPausedSecond(t)
	f.healthy(t, 1)
	f.samples(t, 5)
	before := publication(f.c, f.g)
	writes := f.dns.Writes()
	awaitControlIdle(t, f.h)
	random.block.Store(true)
	f.h.Clock.Advance(10 * time.Second)
	waitSignal(t, random.entered)
	f.h.Clock.Advance(11 * time.Second)
	once.Do(func() { close(random.release) })
	if pub := publication(f.c, f.g); pub["desired_version"] != before["desired_version"] || f.dns.Writes() != writes {
		t.Fatalf("过期候选仍提交加回：%v", pub)
	}
	f.awaitEnabled(t, 1, false)
}

func TestAllPublishMissingCheckerReportNeverWithdraws(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publish(t)
	gate := make(chan struct{})
	defer close(gate)
	startChecker(t, f.h, f.c, 0, 1, nil)
	slow := startChecker(t, f.h, f.c, 1, 0, gate)
	f.disconnect(t, 0)
	check := awaitOfflineCheck(t, &offlineEntryRig{h: f.h, c: f.c, group: f.g}, "pending")
	if len(check["checkers"].([]any)) != 2 {
		t.Fatalf("未冻结全部检测点：%v", check)
	}
	slow.Disconnect()
	f.samples(t, 6)
	awaitOfflineCheck(t, &offlineEntryRig{h: f.h, c: f.c, group: f.g}, "unknown")
	f.awaitEnabled(t, 0, true)
}

func TestAllPublishAddressSyncKeepsActualPausedStateInExpectation(t *testing.T) {
	f := newAllPolicyRig(t, 2)
	f.publish(t)
	r := f.dns.Record("example.com", "101")
	r.Enabled = false
	f.dns.Set("example.com", r)
	f.c.Do("POST", "/api/nodes/"+f.ids[1]+"/sync-address", map[string]any{"ipv4": "1.1.1.1", "confirm_inbound": true}, 200)
	awaitDNSStage(t, f.c, f.g, panel.EntryWritten)
	m := allPolicyMember(t, f.c, f.g, f.ids[1])
	if m["expected_enabled"] != false {
		t.Fatalf("同步未采用服务商实际暂停状态：%v", m)
	}
	writes := f.dns.Writes()
	f.samples(t, 32)
	f.awaitEnabled(t, 1, false)
	if f.dns.Writes() != writes {
		t.Fatal("后续审计重新启用了同步保留的暂停记录")
	}
}

func TestAllPublishAutomaticNotificationQueueFailureDoesNotBlockRestore(t *testing.T) {
	persistence := &interruptedPersistence{}
	f := newAllPolicyRig(t, 2, func(d *panel.Dependencies) { persistence.base = d.Persistence; d.Persistence = persistence })
	f.publishWithPausedSecond(t)
	f.healthy(t, 1)
	if _, err := persistence.db.Exec(`CREATE TRIGGER fail_member_notifications BEFORE INSERT ON telegram_deliveries BEGIN SELECT RAISE(ABORT,'queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	f.samples(t, 6)
	f.awaitEnabled(t, 1, true)
	f.samples(t, 1)
	waitOfflineEvents(t, f.c, "?type=entry_member_restored", 1)
	if _, err := persistence.db.Exec("DROP TRIGGER fail_member_notifications"); err != nil {
		t.Fatal(err)
	}
	f.samples(t, 1)
	// 历史通知成功且队列暂空，不能证明本次通知已入队并完成续投。
	waitTelegramStatus(t, f.c, func(s map[string]any) bool {
		_, messages := f.telegram.snapshot()
		for _, m := range messages {
			if strings.Contains(m.Text, "自动加回") {
				return s["pending"] == float64(0) && s["reachability"] == "reachable"
			}
		}
		return false
	})
}
