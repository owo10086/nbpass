package clusterharness_test

import (
	"fmt"
	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
	"net"
	"strings"
	"testing"
	"time"
)

func incidentRows(c *clusterharness.Client) []any {
	return c.Do("GET", "/api/incidents", nil, 200)["incidents"].([]any)
}
func awaitIncident(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, kind string) map[string]any {
	t.Helper()
	for i := 0; i < 30; i++ {
		for _, v := range incidentRows(c) {
			r := v.(map[string]any)
			if r["kind"] == kind {
				return r
			}
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("没有 %s 故障，列表=%v，概览=%v", kind, incidentRows(c), operations(t, c))
	return nil
}
func TestIncidentsOfflineSurvivesRestartAndKeepsHistory(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "离线历史节点")
	id := n["id"].(string)
	real := h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	real.Disconnect()
	awaitNode(t, c, id, func(r map[string]any) bool { return !connected(r) })
	h.Clock.Advance(95 * time.Second)
	row := awaitIncident(t, h, c, "offline")
	iid := row["id"].(string)
	if row["impact"] != "unknown" {
		t.Fatalf("控制离线宣告业务中断: %v", row)
	}
	before := c.Do("GET", "/api/incidents/"+iid+"/timeline", nil, 200)["entries"].([]any)
	if len(before) == 0 {
		t.Fatal("离线没有时间线")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	row = c.Do("GET", "/api/incidents/"+iid, nil, 200)
	if row["id"] != iid {
		t.Fatal("重启丢失事故")
	}
	if len(incidentRows(c)) != 1 {
		t.Fatal("重启创建重复事故")
	}
	c.Do("GET", "/api/incidents?limit=0", nil, 400)
	c.Do("GET", "/api/incidents/missing", nil, 404)
}

func TestIncidentsBlockedLinksEntryDecisionAndAction(t *testing.T) {
	f := newBlockedEntryRig(t, true)
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	row := awaitIncident(t, f.h, f.c, "blocked")
	awaitIncidentEntry(t, f.h, f.c, row["id"].(string), func(e map[string]any) bool { return e["stage"] == "服务商已写入" })
	if f.calls.Load() != 0 {
		t.Fatal("只读追踪发起换 IP")
	}
}

func TestIncidentsRotationAppearsInOriginalBlockedFlow(t *testing.T) {
	f := policyThree(t, true)
	f.block(t, 0)
	action := awaitStrategyRotation(t, f)
	row := awaitIncident(t, f.h, f.c, "blocked")
	found := false
	for i := 0; i < 12; i++ {
		entries := f.c.Do("GET", "/api/incidents/"+row["id"].(string)+"/timeline", nil, 200)["entries"].([]any)
		for _, v := range entries {
			if strings.Contains(v.(map[string]any)["source"].(string), action["id"].(string)) {
				found = true
			}
		}
		if found {
			break
		}
		f.h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("换 IP 没有关联原被墙事故")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("追踪改变换 IP 调用次数: %d", f.calls.Load())
	}
}

func awaitIncidentEntry(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	for i := 0; i < 30; i++ {
		for _, v := range c.Do("GET", "/api/incidents/"+id+"/timeline", nil, 200)["entries"].([]any) {
			e := v.(map[string]any)
			if ready(e) {
				return e
			}
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("故障 %s 未出现期待的处理阶段，概览=%v", id, operations(t, c))
	return nil
}

func TestIncidentsRuleFailureEndsAsConfigurationChange(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "监听故障入口")
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, n["id"].(string), connected)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	b := directBody("", occupied.Addr().(*net.TCPAddr).Port, "127.0.0.1", echoTarget(t, "I:"))
	delete(b, "entry_group_id")
	b["entry_node_id"] = n["id"]
	rule := c.Do("POST", "/api/rules", b, 201)["id"].(string)
	awaitRule(t, c, rule, "同步失败")
	incident := awaitIncident(t, h, c, "rule")
	if incident["impact"] != "abnormal" {
		t.Fatalf("监听失败没有明确影响: %v", incident)
	}
	b["enabled"] = false
	c.Do("PUT", "/api/rules/"+rule, b, 200)
	awaitIncidentEntry(t, h, c, incident["id"].(string), func(e map[string]any) bool { return e["stage"] == "配置变更结束" })
	ended := c.Do("GET", "/api/incidents/"+incident["id"].(string), nil, 200)
	if ended["closure"] != "配置变更结束" {
		t.Fatalf("停用伪装为恢复: %v", ended)
	}
	c.Do("DELETE", "/api/rules/"+rule, nil, 204)
	refs := c.Do("GET", "/api/incidents/"+incident["id"].(string), nil, 200)["refs"].([]any)
	for _, value := range refs {
		ref := value.(map[string]any)
		if ref["kind"] == "rule" && ref["exists"] != false {
			t.Fatal("已删除规则未保留历史快照")
		}
	}
}

func TestIncidentsDNSFailureTracksWatchAndNewExpectation(t *testing.T) {
	fake := clusterharness.NewDNSResolver()
	provider := clusterharness.NewDNSPod()
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = nodePasswords{}
		d.DNSWatchLookup = fake.Lookup
		d.DNSProvider = provider
	})
	c := h.Client()
	setup(c)
	login(c)
	c.Do("PUT", "/api/settings/dns-watch", map[string]any{"resolvers": []map[string]string{{"kind": "doh", "address": "https://dns.example/dns-query"}}}, 200)
	id := c.Do("POST", "/api/dns-watches", map[string]any{"domain": "incident.example", "expected_ips": []string{"8.8.8.8"}}, 201)["id"].(string)
	watchResult(t, c, id, func(r map[string]any) bool { return r["status"] == "采样失败" })
	incident := awaitIncident(t, h, c, "dns")
	awaitIncidentEntry(t, h, c, incident["id"].(string), func(e map[string]any) bool { return e["stage"] == "采样失败" })
	if incident["impact"] != "unknown" {
		t.Fatal("采样失败推断业务中断")
	}
	c.Do("PUT", "/api/dns-watches/"+id, map[string]any{"expected_ips": []string{"1.1.1.1"}}, 200)
	awaitIncidentEntry(t, h, c, incident["id"].(string), func(e map[string]any) bool { return e["stage"] == "新版本替代" })
	c.Do("GET", "/api/incidents?watch_id="+id+"&kind=dns", nil, 200)
	c.Do("DELETE", "/api/dns-watches/"+id, nil, 204)
	if provider.Writes() != 0 {
		t.Fatal("故障追踪增加 DNS 写入")
	}
}

func TestIncidentsStorageFailureDoesNotBlockEntrySwitch(t *testing.T) {
	disk := &interruptedPersistence{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { disk.base = d.Persistence; d.Persistence = disk })
	if _, err := disk.db.Exec(`CREATE TRIGGER fail_operation_incidents BEFORE INSERT ON operation_incidents BEGIN SELECT RAISE(ABORT,'故障投影写入失败'); END`); err != nil {
		t.Fatal(err)
	}
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	waitRetention(t, func() bool { return operations(t, f.c)["incident_error"] != "" }, func() { f.h.Clock.Advance(time.Second) })
	writes := f.dns.Writes()
	if _, err := disk.db.Exec("DROP TRIGGER fail_operation_incidents"); err != nil {
		t.Fatal(err)
	}
	incident := awaitIncident(t, f.h, f.c, "blocked")
	awaitIncidentEntry(t, f.h, f.c, incident["id"].(string), func(e map[string]any) bool { return e["stage"] == "服务商已写入" })
	if f.dns.Writes() != writes || f.calls.Load() != 0 {
		t.Fatal("故障记录恢复重放业务动作")
	}
}

func TestIncidentsOfflineRecurrenceAndRetention(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "反复失联")
	id := n["id"].(string)
	real := h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	real.Disconnect()
	awaitNode(t, c, id, func(r map[string]any) bool { return !connected(r) })
	first := awaitIncident(t, h, c, "offline")
	iid := first["id"].(string)
	real.Restore()
	real.Restart()
	awaitNode(t, c, id, connected)
	awaitIncidentEntry(t, h, c, iid, func(e map[string]any) bool { return e["stage"] == "控制连接恢复" })
	ended := c.Do("GET", "/api/incidents/"+iid, nil, 200)["ended_at"]
	h.Clock.Advance(6 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if c.Do("GET", "/api/incidents/"+iid, nil, 200)["ended_at"] != ended {
		t.Fatal("心跳改写原结束时间")
	}
	real.Disconnect()
	awaitNode(t, c, id, func(r map[string]any) bool { return !connected(r) })
	waitRetention(t, func() bool {
		for _, v := range incidentRows(c) {
			r := v.(map[string]any)
			if r["kind"] == "offline" && r["id"] != iid && r["ended_at"] == nil {
				return true
			}
		}
		return false
	}, func() { h.Clock.Advance(time.Second) })
	c.Do("PUT", "/api/settings/retention", map[string]int{"event_days": 1, "probe_hours": 24}, 200)
	h.Clock.Advance(25 * time.Hour)
	login(c)
	waitRetention(t, func() bool {
		for _, v := range incidentRows(c) {
			if v.(map[string]any)["id"] == iid {
				return false
			}
		}
		return true
	}, func() { h.Clock.Advance(time.Minute) })
	rows := c.Do("GET", "/api/incidents?kind=offline&status=open", nil, 200)["incidents"].([]any)
	if len(rows) != 1 {
		t.Fatalf("清理丢失活动故障或复活旧记录: %v", rows)
	}
}

func TestIncidentsScaleKeepsControlAndAutomaticSwitchWorking(t *testing.T) {
	disk := &interruptedPersistence{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { disk.base = d.Persistence; d.Persistence = disk })
	ids := []string{}
	for i := 0; i < 98; i++ {
		ids = append(ids, addNode(f.c, fmt.Sprintf("规模入口 %d", i))["id"].(string))
	}
	bodies := []map[string]any{}
	for i := 0; i < 999; i++ {
		b := directBody("", 20000+i/98, "8.8.8.8", 443)
		delete(b, "entry_group_id")
		b["entry_node_id"] = ids[i%98]
		bodies = append(bodies, b)
	}
	for start := 0; start < len(bodies); start += 500 {
		end := min(start+500, len(bodies))
		importRules(f.c, "", "create", map[string]any{"format_version": 2, "rules": bodies[start:end]}, 200)
	}
	check := func() time.Duration {
		start := time.Now()
		v := operations(t, f.c)
		counts := v["counts"].(map[string]any)
		if counts["nodes"] != float64(100) || counts["rules"] != float64(1000) {
			t.Fatalf("规模数据不完整: %v", counts)
		}
		return time.Since(start)
	}
	baseline := check()
	now := f.h.Clock.Now().UnixNano()
	if _, err := disk.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO operation_incidents(source_key,kind,title,impact,handling,started_at,updated_at,ended_at,closure,refs,reasons) SELECT 'scale:'||x,'rule','已结束样例','cleared','ended',?,?,?,'异常解除','[]','[]' FROM n`, now, now, now); err != nil {
		t.Fatal(err)
	}
	loaded := check()
	page := f.c.Do("GET", "/api/incidents?status=ended&limit=100", nil, 200)
	if len(page["incidents"].([]any)) != 100 || page["next_before"] == nil {
		t.Fatal("规模历史未分页")
	}
	f.c.Do("GET", "/api/incidents?status=ended&limit=100&before="+page["next_before"].(string), nil, 200)
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	row := awaitIncident(t, f.h, f.c, "blocked")
	awaitIncidentEntry(t, f.h, f.c, row["id"].(string), func(e map[string]any) bool { return e["stage"] == "服务商已写入" })
	for _, id := range f.ids {
		if !connected(nodeRow(t, f.c, id)) {
			t.Fatal("规模整理阻塞控制报告")
		}
	}
	if f.dns.Writes() != 2 || f.calls.Load() != 0 {
		t.Fatalf("规模展示改变自动动作: DNS=%d 换 IP=%d", f.dns.Writes(), f.calls.Load())
	}
	t.Logf("100 节点 / 1000 规则概览：历史装载前 %s，10000 条结束故障后 %s；自动切换和控制报告正常", baseline, loaded)
}
