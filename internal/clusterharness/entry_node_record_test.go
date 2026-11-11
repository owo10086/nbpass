package clusterharness_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

type entryNodeRig struct {
	f             *rotationFixture
	dns           *clusterharness.DNSPod
	account, base string
	sites         *domesticSites
}

func newEntryNodeRig(t *testing.T) *entryNodeRig {
	t.Helper()
	r := &entryNodeRig{dns: clusterharness.NewDNSPod(), sites: &domesticSites{}}
	r.dns.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: false})
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = r.dns })
	c := h.Client()
	setup(c)
	login(c)
	s := c.Do("GET", "/api/settings/self-check", nil, 200)
	s["jitter_seconds"] = 0
	c.Do("PUT", "/api/settings/self-check", s, 204)
	r.f = rotationFixtureAt(t, h, c, false, r.sites.dependencies)
	r.base = "/api/nodes/" + r.f.id
	r.account = c.Do("POST", "/api/dns-accounts", map[string]string{"name": "节点 DNSPod", "secret_id": "AKIDtest123", "secret_key": "testSecretKey"}, 201)["id"].(string)
	return r
}

func (r *entryNodeRig) sync(ip string, status int) map[string]any {
	return r.f.c.Do("POST", r.base+"/sync-address", map[string]any{"ipv4": ip, "confirm_inbound": true}, status)
}
func (r *entryNodeRig) bind(id string, status int) map[string]any {
	b := binding(r.account)
	b["record_id"] = id
	return r.f.c.Do("PUT", r.base+"/managed-record", b, status)
}
func (r *entryNodeRig) publish(version int, status int) {
	r.f.c.Do("POST", r.base+"/publish", map[string]any{"expected_version": version, "confirm_inbound": true}, status)
}
func (r *entryNodeRig) publication() map[string]any {
	return r.f.c.Do("GET", r.base+"/publication", nil, 200)
}
func (r *entryNodeRig) await(t *testing.T, stage string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var v map[string]any
	for time.Now().Before(deadline) {
		v = r.publication()
		if a, ok := v["action"].(map[string]any); ok && a["stage"] == stage {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("节点记录动作未进入 %s：%v", stage, v)
	return nil
}

// 删除绑定唯一性、明确启用步骤或在途保护，都会使本场景失败。
func TestEntryNodeRecordBindingPublishAndPendingProtection(t *testing.T) {
	r := newEntryNodeRig(t)
	c := r.f.c
	r.f.h.Client().Do("PUT", r.base+"/managed-record", binding(r.account), 401)
	r.bind("100", 200)
	r.publish(0, 409) // 上报不能代替管理员确认地址。
	r.sync("223.5.5.5", 200)
	r.await(t, panel.EntryWritten)
	other := addNode(c, "其他归属")["id"].(string)
	c.Do("PUT", "/api/nodes/"+other+"/managed-record", binding(r.account), 409)
	group := entryGroup(c, other)["id"].(string)
	remote := r.dns.Record("example.com", "100")
	remote.Enabled = true
	r.dns.Set("example.com", remote)
	c.Do("PUT", "/api/entry-groups/"+group+"/managed-record", binding(r.account), 409)
	remote.Enabled = false
	r.dns.Set("example.com", remote)
	var lost atomic.Bool
	lost.Store(true)
	r.dns.Status = func(context.Context) error { return errors.New("启用响应超时") }
	r.dns.Read = func(context.Context) error {
		if lost.Load() && r.dns.Record("example.com", "100").Enabled {
			return errors.New("暂时不能读回")
		}
		return nil
	}
	r.dns.Write = func(context.Context) error {
		if r.dns.Record("example.com", "100").Enabled {
			t.Error("改地址时提前启用了暂停记录")
		}
		return nil
	}
	r.publish(int(r.publication()["desired_version"].(float64)), 202)
	v := r.await(t, panel.EntryUnconfirmed)
	if v["written_at"] != nil || r.dns.Record("example.com", "100").Value != "223.5.5.5" {
		t.Fatal("未知启用被标成完成", v)
	}
	r.bind("100", 409)
	c.Do("DELETE", r.base+"/managed-record", nil, 409)
	c.Do("DELETE", r.base, nil, 409)
	lost.Store(false)
	advanceHealthyRotation(t, r.f, 10*time.Second)
	r.await(t, panel.EntryWritten)
	if r.dns.Writes() != 2 {
		t.Fatal("核对时重复写或未显式启用", r.dns.Calls())
	}
	r.publish(0, 409)
	old := r.dns.Record("example.com", "100")
	r.dns.Set("example.com", dnsprovider.Record{ID: "101", Name: "replacement", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 60, Enabled: false})
	b := r.bind("101", 200)
	if !strings.Contains(strings.Join(anyStrings(b["warnings"]), " "), "旧") || r.dns.Record("example.com", "100") != old {
		t.Fatal("更换记录缺少提示或修改旧记录", b)
	}
	if r.publication()["written_at"] != nil {
		t.Fatal("新绑定沿用旧记录的发布完成")
	}
	unbound := c.Do("DELETE", r.base+"/managed-record", nil, 200)
	if !strings.Contains(strings.Join(anyStrings(unbound["warnings"]), " "), "原状") || r.dns.Writes() != 2 {
		t.Fatal(unbound)
	}
}

func TestEntryNodeRecordEnableRejectionDoesNotCompleteOrRetry(t *testing.T) {
	r := newEntryNodeRig(t)
	r.bind("100", 200)
	r.sync("223.5.5.5", 200)
	r.await(t, panel.EntryWritten)
	r.dns.EnableError = "500025"
	r.publish(int(r.publication()["desired_version"].(float64)), 202)
	v := r.await(t, panel.EntryFailed)
	advanceHealthyRotation(t, r.f, 30*time.Second)
	if v["written_at"] != nil || r.dns.Record("example.com", "100").Enabled || r.dns.Writes() != 2 {
		t.Fatal(v, r.dns.Calls())
	}
}

// 首次发布自身必须写值、保持暂停，然后显式启用；不能借同步动作掩盖顺序错误。
func TestEntryNodeFirstPublishWritesPausedValueBeforeExplicitEnable(t *testing.T) {
	r := newEntryNodeRig(t)
	r.sync("223.5.5.5", 200)
	r.bind("100", 200)
	r.dns.Write = func(context.Context) error {
		got := r.dns.Record("example.com", "100")
		if got.Value != "223.5.5.5" || got.Enabled {
			t.Error("首次发布改值未保持暂停", got)
		}
		return nil
	}
	r.publish(0, 202)
	v := r.await(t, panel.EntryWritten)
	if v["published"] != true || !r.dns.Record("example.com", "100").Enabled || r.dns.Writes() != 2 {
		t.Fatal("首次发布未完成明确启用", v, r.dns.Calls())
	}
	if n := nodeRow(t, r.f.c, r.f.id); n["managed_record"].(map[string]any)["hostname"] != "entry.example.com" || n["record_status"] != panel.EntryWritten {
		t.Fatal("节点列表没有展示记录与发布状态", n)
	}
}

func TestEntryNodeSyncPreservesProviderStateAfterExternalPause(t *testing.T) {
	r := newEntryNodeRig(t)
	r.sync("223.5.5.5", 200)
	r.bind("100", 200)
	r.publish(0, 202)
	r.await(t, panel.EntryWritten)
	remote := r.dns.Record("example.com", "100")
	remote.Enabled = false
	r.dns.Set("example.com", remote)
	r.f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, r.f, 10*time.Second)
	r.sync("8.8.8.8", 200)
	r.await(t, panel.EntryWritten)
	if got := r.dns.Record("example.com", "100"); got.Enabled || got.Value != "8.8.8.8" {
		t.Fatal("同步依照旧缓存误启用了服务商暂停记录", got)
	}
}

func TestEntryNodeFailedAndUnconfirmedRotationLeaveDNSUntouched(t *testing.T) {
	r := newEntryNodeRig(t)
	r.sync("223.5.5.5", 200)
	r.bind("100", 200)
	r.publish(0, 202)
	r.await(t, panel.EntryWritten)
	before := r.dns.Record("example.com", "100")
	writes := r.dns.Writes()
	r.f.queryFails.Store(true)
	r.f.c.Do("POST", r.f.path+"/actions", nil, 202)
	awaitRotation(t, r.f.c, r.f.path, "cancelled")
	r.f.queryFails.Store(false)
	r.f.c.Do("POST", r.f.path+"/actions", nil, 202)
	awaitRotation(t, r.f.c, r.f.path, "observing")
	r.f.finishObservation(t)
	if r.dns.Record("example.com", "100") != before || r.dns.Writes() != writes || nodeRow(t, r.f.c, r.f.id)["confirmed_ipv4"] != "223.5.5.5" {
		t.Fatal("失败或未确认换 IP 改变了受管记录")
	}
}

func TestEntryNodeRecordAPIsRequireAdminAndExplicitConfirmation(t *testing.T) {
	r := newEntryNodeRig(t)
	for _, route := range []struct{ method, suffix string }{{"GET", "/publication"}, {"GET", "/managed-record"}, {"PUT", "/managed-record"}, {"DELETE", "/managed-record"}, {"GET", "/dns-watch"}, {"POST", "/publish"}, {"POST", "/sync-address"}} {
		r.f.h.Client().Do(route.method, r.base+route.suffix, nil, 401)
	}
	r.f.c.Do("POST", r.base+"/sync-address", map[string]any{"ipv4": "223.5.5.5"}, 400)
	r.f.c.Do("POST", r.base+"/sync-address", map[string]any{"ipv4": "10.0.0.1", "confirm_inbound": true}, 400)
	r.f.c.Do("POST", r.base+"/publish", map[string]any{"expected_version": 0}, 400)
	r.f.c.Do("GET", "/api/nodes/missing/publication", nil, 404)
}

func TestEntryNodeWatchSameDomainGroupCannotTakeOwnership(t *testing.T) {
	r := newEntryNodeRig(t)
	r.sync("223.5.5.5", 200)
	r.bind("100", 200)
	before := r.f.c.Do("GET", r.base+"/dns-watch", nil, 200)
	group := entryGroup(r.f.c, r.f.id)["id"].(string)
	r.dns.Set("example.com", dnsprovider.Record{ID: "101", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 60, Enabled: true})
	b := binding(r.account)
	b["record_id"] = "101"
	r.f.c.Do("PUT", "/api/entry-groups/"+group+"/managed-record", b, 200)
	r.f.c.Do("POST", "/api/entry-groups/"+group+"/publish", map[string]any{"active_node_id": r.f.id, "ipv4": "8.8.8.8", "confirm_inbound": true}, 202)
	awaitDNSStage(t, r.f.c, group, panel.EntryWritten)
	after := r.f.c.Do("GET", r.base+"/dns-watch", nil, 200)
	if after["id"] != before["id"] || strings.Join(anyStrings(after["expected_ips"]), ",") != "223.5.5.5" || entryWatch(r.f.c, group)["id"] == before["id"] {
		t.Fatal("同名组夺取节点监控或覆盖其期望", before, after)
	}
}

func TestEntryNodeRebindInvalidatesStalePublicationVersion(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "首次发布前换绑", true: "发布后换绑"}[published], func(t *testing.T) {
			r := newEntryNodeRig(t)
			r.sync("223.5.5.5", 200)
			r.bind("100", 200)
			if published {
				r.publish(0, 202)
				r.await(t, panel.EntryWritten)
			}
			stale := int(r.publication()["desired_version"].(float64))
			r.dns.Set("example.com", dnsprovider.Record{ID: "101", Name: "replacement", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 600, Enabled: false})
			r.bind("101", 200)
			r.publish(stale, 409)
			if got := r.dns.Record("example.com", "101"); got.Value != "8.8.8.8" || got.Enabled {
				t.Fatal("旧页面发布了新绑定", got)
			}
		})
	}
}

func TestEntryNodePendingAddressSyncProtectsGroupAndMembership(t *testing.T) {
	for _, removeGroup := range []bool{true, false} {
		t.Run(map[bool]string{true: "删除组", false: "移除成员"}[removeGroup], func(t *testing.T) {
			r := newEntryNodeRig(t)
			r.sync("223.5.5.5", 200)
			group := seedPublishedMember(t, r, "201", false)
			other := addNode(r.f.c, "保留成员")["id"].(string)
			r.f.c.Do("PUT", "/api/entry-groups/"+group, map[string]any{"name": "待办保护", "members": []string{r.f.id, other}}, 200)
			db, err := (platform.SQLite{Path: r.f.h.Dir + "/panel.db"}).Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`INSERT INTO node_address_sync_jobs(managed_record_id,binding_generation,node_id,address,kind) VALUES('member:201',1,?,'223.5.5.5','address_update')`, r.f.id)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if removeGroup {
				r.f.c.Do("DELETE", "/api/entry-groups/"+group, nil, 409)
			} else {
				r.f.c.Do("PUT", "/api/entry-groups/"+group, map[string]any{"name": "不能丢弃收尾", "members": []string{other}}, 409)
			}
			if r.dns.Record("example.com", "201").Value != "223.5.5.5" || r.dns.Writes() != 0 {
				t.Fatal("保护失败改了远端记录")
			}
		})
	}
}

// 普通上报只能通知；同步必须沿用已暂停或已启用的状态，并更新自动监控期望。
func TestEntryNodeRecordExternalAddressNeedsManualSyncAndWatch(t *testing.T) {
	r := newEntryNodeRig(t)
	r.bind("100", 200)
	r.sync("223.5.5.5", 200)
	r.f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, r.f, 30*time.Second)
	if nodeRow(t, r.f.c, r.f.id)["confirmed_ipv4"] != "223.5.5.5" || r.dns.Writes() != 1 {
		t.Fatal("外部变化自动改了确认地址或 DNS")
	}
	events := r.f.c.Do("GET", "/api/events?node_id="+r.f.id+"&type=entry_action", nil, 200)["events"].([]any)
	found := false
	for _, e := range events {
		found = found || strings.Contains(e.(map[string]any)["message"].(string), "上报地址变化")
	}
	if !found {
		t.Fatal("未通知地址偏差")
	}
	r.sync("223.5.5.5", 409)
	r.sync("8.8.8.8", 200)
	r.await(t, panel.EntryWritten)
	if got := r.dns.Record("example.com", "100"); got.Value != "8.8.8.8" || got.Enabled {
		t.Fatal("同步误启用暂停记录", got)
	}
	w := r.f.c.Do("GET", r.base+"/dns-watch", nil, 200)
	if ips := anyStrings(w["expected_ips"]); len(ips) != 1 || ips[0] != "8.8.8.8" {
		t.Fatal(w)
	}
	r.f.c.Do("PUT", "/api/dns-watches/"+w["id"].(string), map[string]any{"expected_ips": []string{"4.2.2.2"}}, 409)
	r.f.c.Do("DELETE", "/api/dns-watches/"+w["id"].(string), nil, 409)
	r.publish(int(r.publication()["desired_version"].(float64)), 202)
	r.await(t, panel.EntryWritten)
	r.f.ip.Store("4.2.2.2")
	advanceHealthyRotation(t, r.f, 10*time.Second)
	r.sync("4.2.2.2", 200)
	r.await(t, panel.EntryWritten)
	if got := r.dns.Record("example.com", "100"); got.Value != "4.2.2.2" || !got.Enabled {
		t.Fatal(got)
	}
}

func TestEntryNodeRecordControlledRotationOnlyUpdatesAfterConfirmation(t *testing.T) {
	r := newEntryNodeRig(t)
	r.bind("100", 200)
	r.sync("223.5.5.5", 200)
	r.await(t, panel.EntryWritten)
	r.publish(int(r.publication()["desired_version"].(float64)), 202)
	r.await(t, panel.EntryWritten)
	before := r.dns.Writes()
	r.f.c.Do("POST", r.f.path+"/actions", nil, 202)
	awaitRotation(t, r.f.c, r.f.path, "observing")
	if r.dns.Writes() != before {
		t.Fatal("新地址未确认就写 DNS")
	}
	r.f.second.Store("1.1.1.1")
	r.f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, r.f, time.Minute)
	if r.dns.Writes() != before {
		t.Fatal("查询源不一致就写 DNS")
	}
	r.f.second.Store("")
	advanceHealthyRotation(t, r.f, time.Minute)
	awaitRotation(t, r.f.c, r.f.path, "changed")
	r.await(t, panel.EntryWritten)
	if got := r.dns.Record("example.com", "100"); got.Value != "8.8.8.8" || !got.Enabled || r.f.calls.Load() != 1 {
		t.Fatal(got)
	}
}

func TestEntryNodeRecordDeletePreservesRemoteAndWarns(t *testing.T) {
	r := newEntryNodeRig(t)
	r.bind("100", 200)
	before := r.dns.Record("example.com", "100")
	refs := r.f.c.Do("GET", r.base+"/references", nil, 200)
	if !strings.Contains(strings.Join(anyStrings(refs["warnings"]), " "), "原状") {
		t.Fatal("删除确认缺少旧记录提示", refs)
	}
	r.f.c.Do("DELETE", r.base, nil, 204)
	if r.dns.Record("example.com", "100") != before || r.dns.Writes() != 0 {
		t.Fatal("删除节点改了远端记录")
	}
}

func TestEntryNodeSingleRuleAutomaticRotationPurpose(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "关闭", true: "开启"}[enabled], func(t *testing.T) {
			r := newEntryNodeRig(t)
			f := r.f
			body := singleEntryBody(f.id, f.h.ReserveTCPPort(), "example.com", 443)
			f.c.Do("POST", "/api/rules", body, 201)
			awaitRevision(t, f.c, f.id, 0)
			f.c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": enabled}, 200)
			f.c.Do("PUT", r.base+"/self-check", map[string]bool{"enabled": true}, 204)
			awaitSelfCheck(t, f.c, f.id, 1, "suspected", 1)
			advanceSelfCheck(t, f.h, f.c, f.id, 2, "suspected", 2)
			advanceSelfCheck(t, f.h, f.c, f.id, 3, "blocked", 3)
			advanceHealthyRotation(t, f, 30*time.Second)
			if enabled {
				awaitRotation(t, f.c, f.path, "observing")
				if f.calls.Load() != 1 {
					t.Fatal("单节点用途未申请或重复申请")
				}
			} else if f.calls.Load() != 0 {
				t.Fatal("关闭换 IP 仍调用商家")
			}
		})
	}
}

// 票 04 的成员绑定 HTTP API 尚未合入；这里只装入票 01 已定义的已发布备份夹具。
// 后续动作和断言均通过真实节点、HTTP API 与假外部服务完成，不断言数据库结构。
func seedPublishedMember(t *testing.T, r *entryNodeRig, record string, enabled bool) string {
	t.Helper()
	group := entryGroup(r.f.c, r.f.id)["id"].(string)
	db, err := (platform.SQLite{Path: r.f.h.Dir + "/panel.db"}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO managed_records(id,owner_kind,group_id,node_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,current_enabled,warnings) VALUES(?,'member',?,?,?,'dnspod','example.com',?,'entry','默认','0','223.5.5.5',60,?,'[]')`, "member:"+record, group, r.f.id, r.account, record, enabled)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO entry_publications(group_id,desired_version) VALUES(?,1)", group); err != nil {
		t.Fatal(err)
	}
	r.dns.Set("example.com", dnsprovider.Record{ID: record, Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "223.5.5.5", TTL: 60, Enabled: enabled})
	return group
}

func TestEntryNodeSyncWithoutNodeRecordCoversAllPublishedMembers(t *testing.T) {
	r := newEntryNodeRig(t)
	r.sync("223.5.5.5", 200)
	seedPublishedMember(t, r, "201", false)
	seedPublishedMember(t, r, "202", true)
	r.f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, r.f, 10*time.Second)
	r.sync("8.8.8.8", 200)
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && (r.dns.Record("example.com", "201").Value != "8.8.8.8" || r.dns.Record("example.com", "202").Value != "8.8.8.8") {
		time.Sleep(5 * time.Millisecond)
	}
	for _, id := range []string{"201", "202"} {
		got := r.dns.Record("example.com", id)
		if got.Value != "8.8.8.8" || got.Enabled != (id == "202") {
			t.Fatal("未覆盖全部成员或改变状态", got)
		}
	}
}

func TestEntryNodeMemberSyncDoesNotDiscardAnotherMembersPendingAction(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{false: "手动同步拒绝组内在途", true: "受控换IP排队收尾"}[controlled], func(t *testing.T) {
			r := newEntryNodeRig(t)
			r.sync("223.5.5.5", 200)
			group := seedPublishedMember(t, r, "201", false)
			other := addNode(r.f.c, "同组另一成员")
			id := other["id"].(string)
			otherNode := r.f.h.StartNode(other["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
				d.PublicIPv4 = func(context.Context) (string, error) { return "1.1.1.1", nil }
			})
			awaitNode(t, r.f.c, id, connected)
			r.f.c.Do("PUT", "/api/entry-groups/"+group, map[string]any{"name": "并发成员", "members": []string{r.f.id, id}}, 200)
			db, err := (platform.SQLite{Path: r.f.h.Dir + "/panel.db"}).Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`INSERT INTO managed_records(id,owner_kind,group_id,node_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,current_enabled,warnings) VALUES('other-member','member',?,?,?,'dnspod','example.com','202','entry','默认','0','223.5.5.5',60,1,'[]')`, group, id, r.account)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			r.dns.Set("example.com", dnsprovider.Record{ID: "202", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "223.5.5.5", TTL: 60, Enabled: true})
			var unavailable atomic.Bool
			unavailable.Store(true)
			r.dns.Read = func(context.Context) error {
				if unavailable.Load() {
					return errors.New("服务商暂不可读")
				}
				return nil
			}
			r.f.c.Do("POST", "/api/nodes/"+id+"/sync-address", map[string]any{"ipv4": "1.1.1.1", "confirm_inbound": true}, 200)
			if controlled {
				r.f.c.Do("POST", r.f.path+"/actions", nil, 202)
				awaitRotation(t, r.f.c, r.f.path, "observing")
			}
			r.f.ip.Store("8.8.8.8")
			advanceHealthyRotation(t, r.f, time.Minute)
			if controlled {
				awaitRotation(t, r.f.c, r.f.path, "changed")
				if nodeRow(t, r.f.c, r.f.id)["confirmed_ipv4"] != "8.8.8.8" {
					t.Fatal("在途 DNS 阻塞了换 IP 地址确认")
				}
				if err := r.f.h.Restart(); err != nil {
					t.Fatal(err)
				}
				login(r.f.c)
				r.f.n.Restart()
				otherNode.Restart()
				awaitNode(t, r.f.c, r.f.id, connected)
				awaitNode(t, r.f.c, id, connected)
			} else {
				r.sync("8.8.8.8", 409)
				if nodeRow(t, r.f.c, r.f.id)["confirmed_ipv4"] != "223.5.5.5" {
					t.Fatal("手动同步失败未回滚确认地址")
				}
			}
			unavailable.Store(false)
			advanceHealthyRotation(t, r.f, 10*time.Second)
			awaitDNSStage(t, r.f.c, group, panel.EntryWritten)
			if !controlled {
				r.sync("8.8.8.8", 200)
			}
			advanceHealthyRotation(t, r.f, 10*time.Second)
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) && r.dns.Record("example.com", "201").Value != "8.8.8.8" {
				advanceHealthyRotation(t, r.f, 10*time.Second)
				time.Sleep(5 * time.Millisecond)
			}
			if a, b := r.dns.Record("example.com", "201"), r.dns.Record("example.com", "202"); a.Value != "8.8.8.8" || a.Enabled || b.Value != "1.1.1.1" || !b.Enabled {
				t.Fatal("地址同步丢失同组其他记录期望", a, b)
			}
		})
	}
}

func TestEntryNodeRestoreReconcilesUnknownFirstPublish(t *testing.T) {
	r := newEntryNodeRig(t)
	r.sync("223.5.5.5", 200)
	r.bind("100", 200)
	var unavailable atomic.Bool
	unavailable.Store(true)
	r.dns.Status = func(context.Context) error { return errors.New("启用响应丢失") }
	r.dns.Read = func(context.Context) error {
		if unavailable.Load() && r.dns.Record("example.com", "100").Enabled {
			return errors.New("暂不可读")
		}
		return nil
	}
	r.publish(0, 202)
	r.await(t, panel.EntryUnconfirmed)
	backup := filepath.Join(r.f.h.Dir, "unknown-publish.db")
	if err := r.f.h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	r.f.n.Disconnect()
	if err := r.f.h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	login(r.f.c)
	r.f.n.Restore()
	r.f.n.Restart()
	awaitRevision(t, r.f.c, r.f.id, 0)
	unavailable.Store(false)
	r.f.c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	if v := r.publication(); v["published"] != true || v["written_at"] == nil || r.dns.Writes() != 2 {
		t.Fatal("恢复核对实际启用后仍未发布完成或重复写入", v, r.dns.Calls())
	}
}

func TestEntryNodeRestoreControlledAddressUpdatesWatchExpectation(t *testing.T) {
	r := newEntryNodeRig(t)
	r.sync("223.5.5.5", 200)
	r.bind("100", 200)
	r.publish(0, 202)
	r.await(t, panel.EntryWritten)
	backup := filepath.Join(r.f.h.Dir, "before-rotation.db")
	if err := r.f.h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	advanceHealthyRotation(t, r.f, 10*time.Second)
	r.f.c.Do("POST", r.f.path+"/actions", nil, 202)
	awaitRotation(t, r.f.c, r.f.path, "observing")
	r.f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, r.f, time.Minute)
	awaitRotation(t, r.f.c, r.f.path, "changed")
	r.await(t, panel.EntryWritten)
	r.f.n.Disconnect()
	if err := r.f.h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	login(r.f.c)
	r.f.n.Restore()
	r.f.n.Restart()
	awaitRevision(t, r.f.c, r.f.id, 0)
	r.f.c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	w := r.f.c.Do("GET", r.base+"/dns-watch", nil, 200)
	if nodeRow(t, r.f.c, r.f.id)["confirmed_ipv4"] != "8.8.8.8" || strings.Join(anyStrings(w["expected_ips"]), ",") != "8.8.8.8" {
		t.Fatal("恢复新地址后解析监控仍期望旧地址", w)
	}
}
