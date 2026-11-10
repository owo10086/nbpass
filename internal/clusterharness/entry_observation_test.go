package clusterharness_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/controlpb"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

func setObservation(c *clusterharness.Client, group string, enabled bool) {
	c.Do("PUT", "/api/entry-groups/"+group+"/observation", map[string]bool{"enabled": enabled}, 200)
}

func observationActions(c *clusterharness.Client, group string) []any {
	return c.Do("GET", "/api/entry-groups/"+group+"/observation", nil, 200)["actions"].([]any)
}

func requireObservation(t *testing.T, c *clusterharness.Client, group, kind string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, raw := range observationActions(c, group) {
			if raw.(map[string]any)["kind"] == kind {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("缺少本来会做的动作 %s：%v", kind, observationActions(c, group))
}

// 删除提交拦截会改变期望版本及真实服务商记录；关闭后的健康状态不应重放旧摘除。
func TestObservationAllPublishWithdrawalAndNoReplay(t *testing.T) {
	r := newAllRotationRig(t, 2)
	setObservation(r.c, r.g, true)
	before := publication(r.c, r.g)["desired_version"]
	writes := r.dns.Writes()
	r.block(t)
	requireObservation(t, r.c, r.g, "auto_withdraw")
	requireObservation(t, r.c, r.g, "rotate_ip")
	if r.calls.Load() != 0 || r.dns.Writes() != writes || publication(r.c, r.g)["desired_version"] != before || !r.dns.Record("example.com", "100").Enabled {
		t.Fatal("观测期间仍执行自动摘除或换 IP")
	}
	if r.c.Do("GET", r.path, nil, 200)["quota"].(map[string]any)["used_day"] != float64(0) {
		t.Fatal("观测占用换 IP 额度")
	}
	r.samples(t, 3)
	count := len(observationActions(r.c, r.g))
	r.samples(t, 3)
	if len(observationActions(r.c, r.g)) != count {
		t.Fatal("同一判断反复记录和通知")
	}
	r.sites[0].mode.Store(2)
	r.samples(t, 12)
	setObservation(r.c, r.g, false)
	r.samples(t, 3)
	if r.calls.Load() != 0 || r.dns.Writes() != writes {
		t.Fatal("关闭观测后重放历史动作")
	}
	_, messages := r.telegram.snapshot()
	found := false
	for _, m := range messages {
		found = found || strings.Contains(m.Text, "观测中") && strings.Contains(m.Text, "本来会")
	}
	if !found {
		t.Fatal("缺少观测 Telegram 通知")
	}
}

func TestObservationAllPublishRestoreAndManualActions(t *testing.T) {
	r := newAllPolicyRig(t, 2)
	setObservation(r.c, r.g, true)
	r.publish(t) // 首次发布属于手动授权。
	r.action(0, "withdraw", 202)
	r.awaitEnabled(t, 0, false)
	r.action(0, "restore", 202)
	r.awaitEnabled(t, 0, true)
	setObservation(r.c, r.g, false)
	startChecker(t, r.h, r.c, 0, 1, nil)
	r.disconnect(t, 0)
	r.awaitEnabled(t, 0, false)
	setObservation(r.c, r.g, true)
	r.nodes[0].Restart()
	awaitNode(t, r.c, r.ids[0], connected)
	r.healthy(t, 0)
	r.samples(t, 6)
	requireObservation(t, r.c, r.g, "auto_restore")
	if r.dns.Record("example.com", "100").Enabled {
		t.Fatal("观测中自动加回")
	}
	setObservation(r.c, r.g, false)
	r.samples(t, 6)
	r.awaitEnabled(t, 0, true)
}

func TestObservationPrimaryOfflineSwitchAndManualSwitch(t *testing.T) {
	f := offlineEntry(t, true)
	setObservation(f.c, f.group, true)
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.disconnect(t)
	awaitOfflineCheck(t, f, "unreachable")
	f.advance(t, 3)
	requireObservation(t, f.c, f.group, "switch")
	if f.dns.Writes() != 1 || publication(f.c, f.group)["desired_version"] != float64(1) {
		t.Fatal("观测中仍自动入口切换")
	}
	manualSwitch(f.c, f.group, f.b, "1.1.1.1", 1, 202)
	awaitDNSStage(t, f.c, f.group, panel.EntryWritten)
	if f.dns.Record("example.com", "100").Value != "1.1.1.1" {
		t.Fatal("观测阻止手动切换")
	}
}

func TestObservationSharedNodeStillWithdrawsOtherGroup(t *testing.T) {
	r := newAllRotationRig(t, 2)
	observed := entryGroup(r.c, r.ids[0])["id"].(string)
	setObservation(r.c, observed, true) // 未发布的主备组同样否决节点自动换 IP。
	r.block(t)
	r.awaitEnabled(t, 0, false)
	requireObservation(t, r.c, observed, "rotate_ip")
	if r.calls.Load() != 0 || r.c.Do("GET", r.path, nil, 200)["quota"].(map[string]any)["used_day"] != float64(0) {
		t.Fatal("非观测组用途绕过共享节点观测限制")
	}
}

func TestObservationOverridesSingleEntryAndExitPurposes(t *testing.T) {
	for _, scenario := range []struct {
		purpose   string
		observing bool
	}{{"single", true}, {"exit", true}, {"single", false}} {
		purpose := scenario.purpose
		t.Run(purpose+map[bool]string{true: "观测中", false: "无入口组"}[scenario.observing], func(t *testing.T) {
			r := newEntryNodeRig(t)
			f := r.f
			group := ""
			if scenario.observing {
				group = entryGroup(f.c, f.id)["id"].(string)
				setObservation(f.c, group, true)
			}
			if purpose == "single" {
				f.c.Do("POST", "/api/rules", singleEntryBody(f.id, f.h.ReserveTCPPort(), "example.com", 443), 201)
			} else {
				f.c.Do("PUT", r.base+"/tunnel", map[string]any{"address": "223.5.5.5", "listen_port": f.h.ReserveTCPPort(), "transport": "wss"}, 200)
				f.c.Do("PUT", r.base+"/exit-blocked", map[string]bool{"enabled": true}, 200)
			}
			awaitRevision(t, f.c, f.id, 0)
			f.c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
			f.c.Do("PUT", r.base+"/self-check", map[string]bool{"enabled": true}, 204)
			awaitSelfCheck(t, f.c, f.id, 1, "suspected", 1)
			advanceSelfCheck(t, f.h, f.c, f.id, 2, "suspected", 2)
			advanceSelfCheck(t, f.h, f.c, f.id, 3, "blocked", 3)
			advanceHealthyRotation(t, f, 30*time.Second)
			if !scenario.observing {
				awaitRotation(t, f.c, f.path, "observing")
				if f.calls.Load() != 1 {
					t.Fatal("无观测组的单节点入口未自动换 IP")
				}
				return
			}
			requireObservation(t, f.c, group, "rotate_ip")
			if f.calls.Load() != 0 || f.c.Do("GET", f.path, nil, 200)["quota"].(map[string]any)["used_day"] != float64(0) {
				t.Fatal("出口或单节点用途绕过观测限制")
			}
		})
	}
}

func TestObservationDNSDriftOnlyRecordsBothPublicationModes(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "主备", true: "全部发布"}[all], func(t *testing.T) {
			var c *clusterharness.Client
			var dns *clusterharness.DNSPod
			var group string
			var advance func()
			if all {
				r := newAllPolicyRig(t, 2)
				r.publish(t)
				c, dns, group = r.c, r.dns, r.g
				advance = func() { r.samples(t, 31) }
			} else {
				f := offlineEntry(t, false)
				c, dns, group = f.c, f.dns, f.group
				advance = func() { f.advance(t, 310) }
			}
			setObservation(c, group, true)
			before, writes := publication(c, group)["desired_version"], dns.Writes()
			record := dns.Record("example.com", "100")
			record.Value = "4.2.2.2"
			dns.Set("example.com", record)
			advance()
			requireObservation(t, c, group, "writeback")
			if dns.Record("example.com", "100").Value != "4.2.2.2" || dns.Writes() != writes || publication(c, group)["desired_version"] != before {
				t.Fatal("观测中仍写回偏差或改变版本")
			}
		})
	}
}

func TestObservationCancelsQueuedDNSAcrossRestart(t *testing.T) {
	gate := &observationDNSGate{}
	r := newAllRotationRig(t, 2, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	gate.unavailable.Store(true)
	writes := r.dns.Writes()
	r.block(t)
	awaitDNSStage(t, r.c, r.g, panel.EntryDesired)
	setObservation(r.c, r.g, true)
	awaitDNSStage(t, r.c, r.g, panel.EntrySuperseded)
	if allPolicyMember(t, r.c, r.g, r.ids[0])["expected_enabled"] != true {
		t.Fatal("未发出的摘除未恢复原期望")
	}
	gate.unavailable.Store(false)
	if err := r.h.Restart(); err != nil {
		t.Fatal(err)
	}
	login(r.c)
	for i, n := range r.nodes {
		n.Restart()
		awaitNode(t, r.c, r.ids[i], connected)
	}
	r.samples(t, 3)
	if r.dns.Writes() != writes || r.calls.Load() != 0 || !r.dns.Record("example.com", "100").Enabled {
		t.Fatal("开启或重启后续跑已作废的 DNS 动作")
	}
	requireObservation(t, r.c, r.g, "auto_withdraw")
}

type observationDNSGate struct {
	dnsprovider.Provider
	unavailable atomic.Bool
	hold        atomic.Bool
	entered     chan struct{}
	release     chan struct{}
}

func (d *observationDNSGate) ReadRecord(ctx context.Context, auth dnsprovider.Credentials, key dnsprovider.Key) (dnsprovider.ReadResult, error) {
	if d.unavailable.Load() {
		return dnsprovider.ReadResult{}, errors.New("DNSPod 暂不可读")
	}
	actual, err := d.Provider.ReadRecord(ctx, auth, key)
	if err == nil && d.hold.CompareAndSwap(true, false) {
		close(d.entered)
		select {
		case <-d.release:
		case <-ctx.Done():
			return dnsprovider.ReadResult{}, ctx.Err()
		}
	}
	return actual, err
}

func TestObservationEnabledAfterCoordinatorReadStillPreventsWrite(t *testing.T) {
	gate := &observationDNSGate{entered: make(chan struct{}), release: make(chan struct{})}
	r := newAllRotationRig(t, 2, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	gate.unavailable.Store(true)
	writes := r.dns.Writes()
	r.block(t)
	awaitDNSStage(t, r.c, r.g, panel.EntryDesired)
	gate.hold.Store(true)
	gate.unavailable.Store(false)
	var released atomic.Bool
	release := func() {
		if released.CompareAndSwap(false, true) {
			close(gate.release)
		}
	}
	defer release()
	r.samples(t, 1)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("协调器未进入外部读回")
	}
	setObservation(r.c, r.g, true)
	release()
	r.samples(t, 1)
	awaitDNSStage(t, r.c, r.g, panel.EntrySuperseded)
	if r.dns.Writes() != writes || !r.dns.Record("example.com", "100").Enabled {
		t.Fatal("最后写入前未复核观测开关")
	}
}

func TestObservationAlreadyCalledDNSReconcilesWithoutRepeating(t *testing.T) {
	gate := &allRotationDNSGate{}
	r := newAllRotationRig(t, 2, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	gate.blocked.Store(true)
	r.dns.Status = func(context.Context) error { return errors.New("暂停响应丢失") }
	r.block(t)
	awaitDNSStage(t, r.c, r.g, panel.EntryUnconfirmed)
	writes := r.dns.Writes()
	setObservation(r.c, r.g, true)
	gate.blocked.Store(false)
	r.samples(t, 2)
	awaitDNSStage(t, r.c, r.g, panel.EntryWritten)
	if r.dns.Writes() != writes || r.dns.Record("example.com", "100").Enabled || r.calls.Load() != 0 {
		t.Fatal("把已调用动作当作取消成功，或重复写入/换 IP")
	}
}

// 安排“落盘后、首次投递前崩溃”的持久状态；开关、额度、重启结果均经 HTTP 核对。
func TestObservationUnsentPermitIsCancelledAndQuotaReleased(t *testing.T) {
	r := newAllRotationRig(t, 2)
	r.nodes[0].Disconnect()
	awaitNode(t, r.c, r.ids[0], func(v map[string]any) bool { return v["connected"] == false })
	db, err := (platform.SQLite{Path: r.h.Dir + "/panel.db"}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := r.h.Clock.Now()
	_, err = db.Exec(`INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name,origin)
 SELECT 'observation-unsent',s.node_id,r.public_ipv4,'authorized',?,?,?,s.secret_name,'automatic'
 FROM rotation_settings s JOIN node_regions r ON r.node_id=s.node_id WHERE s.node_id=?`, now.UnixNano(), now.Add(2*time.Minute).UnixNano(), now.UnixNano(), r.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES('observation-unsent',?,?)", r.ids[0], now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if r.c.Do("GET", r.path, nil, 200)["quota"].(map[string]any)["used_day"] != float64(1) {
		t.Fatal("夹具未占用许可额度")
	}
	setObservation(r.c, r.g, true)
	v := r.c.Do("GET", r.path, nil, 200)
	if v["action"].(map[string]any)["state"] != "cancelled" || v["quota"].(map[string]any)["used_day"] != float64(0) {
		t.Fatal("未投递许可未作废或额度未释放", v)
	}
	if err = r.h.Restart(); err != nil {
		t.Fatal(err)
	}
	login(r.c)
	for i, n := range r.nodes {
		n.Restart()
		awaitNode(t, r.c, r.ids[i], connected)
	}
	r.samples(t, 3)
	if r.calls.Load() != 0 {
		t.Fatal("重启续发被作废的许可")
	}
}

func TestObservationCancelledPrimaryRestoresCurrentExpectation(t *testing.T) {
	gate := &observationDNSGate{}
	f := offlineEntry(t, true, func(d *panel.Dependencies) { gate.Provider = d.DNSProvider; d.DNSProvider = gate })
	gate.unavailable.Store(true)
	startChecker(t, f.h, f.c, 0, 1, nil)
	f.disconnect(t)
	f.advance(t, 10)
	awaitDNSStage(t, f.c, f.group, panel.EntryDesired)
	setObservation(f.c, f.group, true)
	awaitDNSStage(t, f.c, f.group, panel.EntrySuperseded)
	if pub := publication(f.c, f.group); pub["active_node_id"] != f.a || pub["expected_value"] != "8.8.8.8" {
		t.Fatal("作废切换后仍保留未执行目标", pub)
	}
	watch := f.c.Do("GET", "/api/entry-groups/"+f.group+"/dns-watch", nil, 200)
	if strings.Join(anyStrings(watch["expected_ips"]), ",") != "8.8.8.8" {
		t.Fatal("作废切换后监控期望未回退", watch)
	}
	gate.unavailable.Store(false)
	f.active.Restart()
	awaitNode(t, f.c, f.a, connected)
	setObservation(f.c, f.group, false)
	f.advance(t, 60)
	if f.dns.Writes() != 1 || f.dns.Record("example.com", "100").Value != "8.8.8.8" {
		t.Fatal("关闭后重放被作废的切换")
	}
}

func TestObservationAPIDefaultValidationAndAuthentication(t *testing.T) {
	f := newAllPolicyRig(t, 1)
	path := "/api/entry-groups/" + f.g + "/observation"
	if f.c.Do("GET", path, nil, 200)["enabled"] != false {
		t.Fatal("新组未默认关闭观测")
	}
	f.c.Do("PUT", path, map[string]any{}, 400)
	f.c.Do("PUT", "/api/entry-groups/missing/observation", map[string]bool{"enabled": true}, 404)
	f.c.Do("GET", "/api/entry-groups/missing/observation", nil, 404)
	f.c.Do("POST", "/api/auth/logout", nil, 204)
	f.c.Do("PUT", path, map[string]bool{"enabled": true}, 401)
	f.c.Do("GET", path, nil, 401)
}

func TestObservationManualAddressSyncPreservesPausedMember(t *testing.T) {
	r := newAllPolicyRig(t, 2)
	r.publish(t)
	setObservation(r.c, r.g, true)
	r.action(0, "withdraw", 202)
	r.awaitEnabled(t, 0, false)
	r.ips[0].Store("4.2.2.2")
	r.samples(t, 1)
	r.c.Do("POST", "/api/nodes/"+r.ids[0]+"/sync-address", map[string]any{"ipv4": "4.2.2.2", "confirm_inbound": true}, 200)
	awaitDNSStage(t, r.c, r.g, panel.EntryWritten)
	if got := r.dns.Record("example.com", "100"); got.Value != "4.2.2.2" || got.Enabled {
		t.Fatal("观测阻止手动同步或误启用已暂停记录", got)
	}
}

// 命令已准备，但前面的普通配置尚未发出：不算“可能已执行”。
// 同时覆盖取消未投递许可后，健康成员不再被旧换 IP 复检屏障挡住。
func TestObservationCancelsPermitWhileEarlierControlMessageIsBlocked(t *testing.T) {
	r := newAllRotationRig(t, 2)
	r.c.Do("PUT", r.path+"/automatic", map[string]bool{"enabled": false}, 200)
	r.block(t)
	r.awaitEnabled(t, 0, false)
	entered, release := make(chan struct{}), make(chan struct{})
	var held, released atomic.Bool
	open := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer open()
	r.h.BeforeControlSend(func(ctx context.Context, node string, m *controlpb.PanelMessage) error {
		if node != r.ids[0] || m.RotationCommand != nil || m.Snapshot == nil || held.Load() {
			return nil
		}
		view := r.c.Do("GET", r.path, nil, 200)
		a, ok := view["action"].(map[string]any)
		if !ok || a["state"] != "authorized" || !held.CompareAndSwap(false, true) {
			return nil
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	r.c.Do("PUT", r.path+"/automatic", map[string]bool{"enabled": true}, 200)
	// 许可授权可能位于最后一次采样之后；此时只推进短时钟，不等待被故意堵住的控制流。
	for range 5 {
		select {
		case <-entered:
			goto blocked
		default:
		}
		r.h.Clock.Advance(time.Second)
		select {
		case <-entered:
			goto blocked
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("没有在换 IP 命令之前截住普通配置")
blocked:
	setObservation(r.c, r.g, true)
	view := r.c.Do("GET", r.path, nil, 200)
	if view["action"].(map[string]any)["state"] != "cancelled" || view["quota"].(map[string]any)["used_day"] != float64(0) {
		t.Fatal("尚未发送的命令被误算成已投递", view)
	}
	open()
	r.h.BeforeControlSend(nil)
	r.samples(t, 2)
	if r.calls.Load() != 0 {
		t.Fatal("观测开启后仍首次发送已准备的自动许可")
	}
	r.healthy(t, 0)
	setObservation(r.c, r.g, false)
	r.samples(t, 6)
	r.awaitEnabled(t, 0, true)
}

// 崩溃快照：自动加回已改值、尚未调用启用；重启只能核对改值并作废启用。
func TestObservationPartiallyCalledRestoreDoesNotBlockManualActions(t *testing.T) {
	r := newAllPolicyRig(t, 2)
	r.publish(t)
	r.action(0, "withdraw", 202)
	r.awaitEnabled(t, 0, false)
	setObservation(r.c, r.g, true)
	db, err := (platform.SQLite{Path: r.h.Dir + "/panel.db"}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,write_started,minimum_enabled)
 SELECT 'observation-half-restore',group_id,desired_version+1,'auto_restore',?,'','',300,'结果未确认',?,1,1 FROM entry_publications WHERE group_id=?`, r.ids[0], r.h.Clock.Now().UnixNano(), r.g)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO entry_action_records(action_id,managed_record_id,binding_generation,provider,domain,record_id,old_value,new_value,old_ttl,new_ttl,old_enabled,new_enabled,stage,write_started,value_confirmed,explicit_enable,status_started)
 SELECT 'observation-half-restore',id,binding_generation,provider,domain,record_id,current_value,current_value,300,60,0,1,'结果未确认',1,1,1,0 FROM managed_records WHERE group_id=? AND node_id=?`, r.g, r.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE entry_publications SET desired_version=desired_version+1,current_action_id='observation-half-restore',written_at=NULL WHERE group_id=?`, r.g); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE entry_member_publications SET expected_enabled=1,manual_locked=0,reason='自动加回' WHERE group_id=? AND node_id=?`, r.g, r.ids[0]); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	writes := r.dns.Writes()
	if err = r.h.Restart(); err != nil {
		t.Fatal(err)
	}
	login(r.c)
	awaitDNSStage(t, r.c, r.g, panel.EntryPartial)
	if r.dns.Writes() != writes || r.dns.Record("example.com", "100").Enabled {
		t.Fatal("观测期间继续了未调用的启用步骤")
	}
	if allPolicyMember(t, r.c, r.g, r.ids[0])["expected_enabled"] != false {
		t.Fatal("未调用的启用仍留在成员期望")
	}
	for i, n := range r.nodes {
		n.Restart()
		awaitNode(t, r.c, r.ids[i], connected)
	}
	r.action(0, "restore", 202)
	r.awaitEnabled(t, 0, true)
}

func TestObservationPrimaryFailback(t *testing.T) {
	f := newFailbackRig(t)
	setObservation(f.c, f.group, true)
	f.resume()
	f.recoverPreferred(t)
	f.samples(t, 18)
	requireObservation(t, f.c, f.group, "failback")
	f.remainsOnStandby(t)
	setObservation(f.c, f.group, false)
	f.samples(t, 6)
	f.returnedToPreferred(t)
}

// 已下发的换 IP 在观测中仍收尾，地址同步必须保持暂停；手动换 IP 同样放行。
func TestObservationDispatchedRotationFinishesAddressSync(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "已下发自动许可", true: "观测中手动许可"}[manual], func(t *testing.T) {
			r := newAllRotationRig(t, 2)
			if manual {
				setObservation(r.c, r.g, true)
				r.c.Do("POST", r.path+"/actions", nil, 202)
				awaitRotation(t, r.c, r.path, "observing")
			} else {
				r.block(t)
				r.observing(t)
				setObservation(r.c, r.g, true)
			}
			enabled := r.dns.Record("example.com", "100").Enabled
			r.ips[0].Store("4.2.2.2")
			r.samples(t, 6)
			awaitRotation(t, r.c, r.path, "changed")
			awaitDNSStage(t, r.c, r.g, panel.EntryWritten)
			if got := r.dns.Record("example.com", "100"); got.Value != "4.2.2.2" || got.Enabled != enabled || r.calls.Load() != 1 {
				t.Fatal("已授权换 IP 未正常同步或改变启用状态", got)
			}
		})
	}
}
