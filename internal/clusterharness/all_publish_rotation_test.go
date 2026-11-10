package clusterharness_test

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"nbpass/internal/rotation"
)

type allRotationRig struct {
	*allPolicyRig
	calls atomic.Int32
	fail  atomic.Bool
	path  string
}

func newAllRotationRig(t *testing.T, count int, options ...func(*panel.Dependencies)) *allRotationRig {
	t.Helper()
	r := &allRotationRig{}
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.calls.Add(1)
		if r.fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	t.Cleanup(vendor.Close)
	roots := x509.NewCertPool()
	roots.AddCert(vendor.Certificate())
	r.allPolicyRig = newAllPolicyRigWithNodes(t, count, func(_ *node.Config, d *node.Dependencies) {
		d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, d.PublicIPv4}
		d.RotationLocalIPv4 = func(ctx context.Context) ([]string, error) { ip, err := d.PublicIPv4(ctx); return []string{ip}, err }
		d.RotationHTTP = (rotation.HTTPExecutor{RootCAs: roots}).Do
	}, options...)
	r.path = "/api/nodes/" + r.ids[0] + "/ip-rotation"
	r.c.Do("PUT", r.path, map[string]any{"url": vendor.URL + "/change", "method": "POST", "private_endpoints": []string{vendor.URL + "/change"}}, 200)
	r.c.Do("PUT", r.path+"/automatic", map[string]bool{"enabled": true}, 200)
	settings := r.c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["sites"] = []string{"one.test", "two.test", "three.test", "four.test"}
	r.c.Do("PUT", "/api/settings/self-check", settings, 204)
	r.c.Do("PUT", "/api/nodes/"+r.ids[0]+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, r.c, r.ids[0], 1, "normal", 0)
	r.publish(t)
	return r
}

func (r *allRotationRig) block(t *testing.T) {
	t.Helper()
	r.sites[0].mode.Store(0)
	r.samples(t, 3)
	awaitSelfCheck(t, r.c, r.ids[0], 2, "suspected", 1)
	r.samples(t, 3)
	awaitSelfCheck(t, r.c, r.ids[0], 3, "suspected", 2)
	r.samples(t, 3)
	awaitSelfCheck(t, r.c, r.ids[0], 4, "blocked", 3)
	r.samples(t, 1)
}

func (r *allRotationRig) view(t *testing.T, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	return awaitRotationView(t, &rotationFixture{h: r.h, c: r.c, id: r.ids[0], path: r.path}, ready)
}

type allRotationDNSGate struct {
	dnsprovider.Provider
	blocked atomic.Bool
}

func (d *allRotationDNSGate) ReadRecord(ctx context.Context, auth dnsprovider.Credentials, key dnsprovider.Key) (dnsprovider.ReadResult, error) {
	r, err := d.Provider.ReadRecord(ctx, auth, key)
	if err == nil && key.RecordID == "100" && !r.Record.Enabled && d.blocked.Load() {
		return dnsprovider.ReadResult{}, errors.New("暂停已执行但尚不可核对")
	}
	return r, err
}

func TestAllPublishRotationWaitsForWithdrawalConfirmation(t *testing.T) {
	gate := &allRotationDNSGate{}
	r := newAllRotationRig(t, 2, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	gate.blocked.Store(true)
	r.dns.Status = func(context.Context) error { return errors.New("暂停响应丢失") }
	r.block(t)
	awaitDNSStage(t, r.c, r.g, panel.EntryUnconfirmed)
	r.samples(t, 2)
	if r.calls.Load() != 0 || r.c.Do("GET", r.path, nil, 200)["quota"].(map[string]any)["used_day"] != float64(0) {
		t.Fatal("摘除未确认就调用换 IP 或占用额度")
	}
	gate.blocked.Store(false)
	r.samples(t, 2)
	r.observing(t)
	if r.calls.Load() != 1 || r.dns.Record("example.com", "100").Enabled {
		t.Fatal("未先摘除再唯一换 IP")
	}
}

// 改地址必须保持暂停，复检和六次真实候选样本都不能省略。
func TestAllPublishRotationUpdatesPausedAddressThenRestoresAfterSixChecks(t *testing.T) {
	r := newAllRotationRig(t, 2)
	r.block(t)
	r.observing(t)
	r.awaitEnabled(t, 0, false)
	r.ips[0].Store("4.2.2.2")
	r.samples(t, 6)
	awaitRotation(t, r.c, r.path, "changed")
	awaitDNSStage(t, r.c, r.g, panel.EntryWritten)
	if got := r.dns.Record("example.com", "100"); got.Value != "4.2.2.2" || got.Enabled {
		t.Fatal("新地址确认未保持摘除", got)
	}
	r.sites[0].mode.Store(2)
	r.samples(t, 3)
	r.view(t, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	// 维护中断清空此前样本，明确验证新一轮五次不够、六次才加回。
	r.c.Do("PUT", "/api/nodes/"+r.ids[0]+"/maintenance", map[string]bool{"enabled": true}, 204)
	r.samples(t, 1)
	r.c.Do("PUT", "/api/nodes/"+r.ids[0]+"/maintenance", map[string]bool{"enabled": false}, 204)
	r.samples(t, 5)
	if r.dns.Record("example.com", "100").Enabled {
		t.Fatal("不足六次检查就启用")
	}
	r.samples(t, 1)
	r.awaitEnabled(t, 0, true)
	if r.calls.Load() != 1 {
		t.Fatal("同一事故重复调用换 IP")
	}
}

func TestAllPublishRotationPendingRecheckCannotRestoreFromPeriodicSelfCheck(t *testing.T) {
	r := newAllRotationRig(t, 2)
	r.block(t)
	r.observing(t)
	r.sites[0].mode.Store(1)
	r.ips[0].Store("4.2.2.2")
	r.samples(t, 15)
	r.view(t, func(v map[string]any) bool { return v["recheck_status"] == "pending" })
	r.sites[0].mode.Store(2)
	r.samples(t, 12)
	if got := r.dns.Record("example.com", "100"); got.Value != "4.2.2.2" || got.Enabled {
		t.Fatal("周期自测绕过本次复检待确认", got)
	}
	if r.calls.Load() != 1 {
		t.Fatal("待确认时重复换 IP")
	}
	r.notice(t, "待确认")
}

func TestAllPublishRotationDisabledOnlyWithdrawsAndNotifies(t *testing.T) {
	r := newAllRotationRig(t, 2)
	r.c.Do("PUT", r.path+"/automatic", map[string]bool{"enabled": false}, 200)
	r.block(t)
	r.awaitEnabled(t, 0, false)
	r.samples(t, 2)
	if r.calls.Load() != 0 || r.c.Do("GET", r.path, nil, 200)["action"] != nil {
		t.Fatal("关闭自动换 IP 仍调用接口")
	}
	r.notice(t, "未开启")
	r.samples(t, 2)
	waitTelegramStatus(t, r.c, func(v map[string]any) bool { return v["pending"] == float64(0) && v["reachability"] == "reachable" })
	_, messages := r.telegram.snapshot()
	found := false
	for _, m := range messages {
		found = found || strings.Contains(m.Text, "未开启")
	}
	if !found {
		t.Fatal("未发送关闭换 IP 的 Telegram 通知")
	}
}

// 冻结假时钟只轮询 HTTP 会让下一秒策略永远不运行；按真实策略时钟推进并核对活性。
func (r *allRotationRig) observing(t *testing.T) {
	t.Helper()
	for range 6 {
		v := r.c.Do("GET", r.path, nil, 200)
		if a, ok := v["action"].(map[string]any); ok && a["state"] == "observing" {
			return
		}
		r.samples(t, 1)
	}
	awaitRotation(t, r.c, r.path, "observing")
}

func (r *allRotationRig) notice(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events := r.c.Do("GET", "/api/events?type=entry_member_rotation", nil, 200)["events"].([]any)
		for _, raw := range events {
			if strings.Contains(raw.(map[string]any)["message"].(string), text) {
				return
			}
		}
		r.samples(t, 1)
	}
	t.Fatalf("缺少全部发布换 IP 通知：%s", text)
}

func TestAllPublishRotationSingleMemberKeepsPublishingAndReportsImpact(t *testing.T) {
	r := newAllRotationRig(t, 1)
	r.block(t)
	r.observing(t)
	if !r.dns.Record("example.com", "100").Enabled {
		t.Fatal("单成员被自动摘除")
	}
	r.notice(t, "因最少保留数仍发布地址")
	r.ips[0].Store("4.2.2.2")
	r.samples(t, 15)
	r.view(t, func(v map[string]any) bool { return v["recheck_status"] == "no_improvement" })
	awaitDNSStage(t, r.c, r.g, panel.EntryWritten)
	if got := r.dns.Record("example.com", "100"); got.Value != "4.2.2.2" || !got.Enabled {
		t.Fatal("保留成员换地址未保持启用", got)
	}
	r.notice(t, "无改善")
	if r.calls.Load() != 1 {
		t.Fatal("无改善绕过额度重复调用")
	}
}

func TestAllPublishRotationRestrictionsAndFreezeKeepWithdrawal(t *testing.T) {
	for _, scenario := range []string{"额度受限", "接口失败", "地址未确认冻结", "复检无改善"} {
		t.Run(scenario, func(t *testing.T) {
			r := newAllRotationRig(t, 2)
			if scenario == "额度受限" {
				// 手动动作消耗同一份额度，真实确认完成后再制造被墙。
				r.c.Do("POST", r.path+"/actions", nil, 202)
				awaitRotation(t, r.c, r.path, "observing")
				r.ips[0].Store("4.2.2.2")
				r.samples(t, 9)
				r.view(t, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
			}
			if scenario == "接口失败" {
				r.fail.Store(true)
			}
			// 每次都按当前真实轮次确认三次异常，避免既有手动动作增加周期自测序号。
			last := r.c.Do("GET", "/api/nodes/"+r.ids[0]+"/self-check", nil, 200)["rounds"].([]any)[0].(map[string]any)["sequence"].(float64)
			r.sites[0].mode.Store(0)
			for i := 1; i <= 3; i++ {
				r.samples(t, 3)
				awaitSelfCheck(t, r.c, r.ids[0], int(last)+i, map[bool]string{true: "blocked", false: "suspected"}[i == 3], i)
			}
			r.samples(t, 2)
			r.awaitEnabled(t, 0, false)
			if scenario == "额度受限" {
				r.notice(t, "受限")
				if r.calls.Load() != 1 {
					t.Fatal("额度受限仍调用商家")
				}
				return
			}
			if scenario == "接口失败" {
				awaitRotation(t, r.c, r.path, "unconfirmed")
			} else {
				r.observing(t)
			}
			if scenario == "复检无改善" {
				r.ips[0].Store("4.2.2.2")
				r.samples(t, 15)
				r.view(t, func(v map[string]any) bool { return v["recheck_status"] == "no_improvement" })
				r.notice(t, "无改善")
			} else {
				// 接口只调用一次；节点按分钟观察五次，未看到新地址就完成冻结。
				for attempt := 1; attempt <= 5; attempt++ {
					r.samples(t, 6)
					r.view(t, func(v map[string]any) bool { return v["action"].(map[string]any)["attempts"] == float64(attempt) })
				}
				r.samples(t, 6)
				awaitRotation(t, r.c, r.path, "unconfirmed")
				r.notice(t, "冻结")
				r.c.Do("POST", r.path+"/actions", nil, 409)
				if r.dns.Record("example.com", "100").Value != "8.8.8.8" {
					t.Fatal("失败或未确认就更新 DNS")
				}
				if r.calls.Load() != 1 {
					t.Fatal("冻结后继续调用商家", r.calls.Load())
				}
			}
			r.awaitEnabled(t, 0, false)
			r.samples(t, 2)
			r.awaitEnabled(t, 0, false)
		})
	}
}

func TestAllPublishRotationSharesPrimaryAndTwoAllGroupBarriersAcrossRestart(t *testing.T) {
	gate := &allRotationDNSGate{}
	r := newAllRotationRig(t, 2, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	r.healthy(t, 1)
	primary := r.c.Do("POST", "/api/entry-groups", map[string]any{"name": "共享主备组", "members": r.ids}, 201)["id"].(string)
	rule := r.c.Do("POST", "/api/rules", directBody(primary, r.h.ReserveTCPPort(), "127.0.0.1", 12345), 201)
	awaitRule(t, r.c, rule["id"].(string), "正常")
	r.dns.Set("example.com", dnsprovider.Record{ID: "300", Name: "primary", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 60, Enabled: true})
	b := binding(r.account)
	b["record_id"] = "300"
	r.c.Do("PUT", "/api/entry-groups/"+primary+"/managed-record", b, 200)
	firstPublish(r.c, primary, r.ids[0], 202)
	awaitDNSStage(t, r.c, primary, panel.EntryWritten)
	r.c.Do("PUT", "/api/entry-groups/"+primary+"/blocked-policy", map[string]string{"policy": "switch_then_rotate"}, 200)
	kept := allGroup(r.c, r.ids[0])
	r.dns.Set("example.com", dnsprovider.Record{ID: "200", Name: "kept", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 60, Enabled: false})
	bindMember(r.c, kept, r.ids[0], r.account, "200", 200)
	publishAll(r.c, kept, map[string]string{r.ids[0]: "8.8.8.8"}, 202)
	awaitDNSStage(t, r.c, kept, panel.EntryWritten)
	// 同时验证节点级记录收尾；无需单节点规则也可以绑定并首次发布。
	r.dns.Set("example.com", dnsprovider.Record{ID: "400", Name: "node", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 60, Enabled: false})
	b = binding(r.account)
	b["record_id"] = "400"
	base := "/api/nodes/" + r.ids[0]
	r.c.Do("PUT", base+"/managed-record", b, 200)
	r.c.Do("POST", base+"/publish", map[string]any{"expected_version": 0, "confirm_inbound": true}, 202)
	deadline := time.Now().Add(5 * time.Second)
	for r.c.Do("GET", base+"/publication", nil, 200)["published"] != true {
		if time.Now().After(deadline) {
			t.Fatal("节点记录未发布")
		}
		time.Sleep(time.Millisecond)
	}
	unpublished := allGroup(r.c, r.ids...)
	gate.blocked.Store(true)
	r.dns.Status = func(context.Context) error { return errors.New("暂停响应丢失") }
	r.block(t)
	awaitDNSStage(t, r.c, primary, panel.EntryWritten)
	if publication(r.c, primary)["active_node_id"] != r.ids[1] {
		t.Fatal("主备组未切离")
	}
	awaitDNSStage(t, r.c, r.g, panel.EntryUnconfirmed)
	r.samples(t, 2)
	if r.calls.Load() != 0 {
		t.Fatal("全部发布成员尚未确认暂停就越过主备切离屏障")
	}
	if err := r.h.Restart(); err != nil {
		t.Fatal(err)
	}
	login(r.c)
	for i, n := range r.nodes {
		n.Restart()
		awaitNode(t, r.c, r.ids[i], connected)
	}
	r.samples(t, 2)
	if r.calls.Load() != 0 {
		t.Fatal("重启丢失全部发布切离屏障")
	}
	gate.blocked.Store(false)
	r.observing(t)
	if r.calls.Load() != 1 || r.dns.Record("example.com", "100").Enabled || !r.dns.Record("example.com", "200").Enabled {
		t.Fatal("各组未先摘除或保留再唯一换 IP")
	}
	r.ips[0].Store("4.2.2.2")
	r.samples(t, 6)
	awaitRotation(t, r.c, r.path, "changed")
	awaitDNSStage(t, r.c, r.g, panel.EntryWritten)
	awaitDNSStage(t, r.c, kept, panel.EntryWritten)
	for _, record := range []string{"100", "200", "400"} {
		got := r.dns.Record("example.com", record)
		if got.Value != "4.2.2.2" || got.Enabled != (record != "100") {
			t.Fatal("全归属地址收尾未保持各自状态", got)
		}
	}
	if r.dns.Record("example.com", "300").Value != "1.1.1.1" {
		t.Fatal("主备组已切离却被节点新地址覆盖")
	}
	r.sites[0].mode.Store(2)
	r.samples(t, 18)
	r.view(t, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	r.awaitEnabled(t, 0, true)
	awaitDNSStage(t, r.c, primary, panel.EntryWritten)
	if publication(r.c, primary)["active_node_id"] != r.ids[0] || r.dns.Record("example.com", "300").Value != "4.2.2.2" {
		t.Fatal("主备组未按既有策略回切")
	}
	if publication(r.c, unpublished)["first_published"] != false || r.calls.Load() != 1 {
		t.Fatal("跨组恢复重复换 IP 或自动首次发布")
	}
}
