package clusterharness_test

import (
	"context"
	"fmt"
	"nbpass/internal/clusterharness"
	"nbpass/internal/dnswatch"
	"nbpass/internal/node"
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
	for i := 0; i < 60; i++ {
		for _, v := range c.Do("GET", "/api/incidents?kind="+kind+"&status=open", nil, 200)["incidents"].([]any) {
			r := v.(map[string]any)
			if r["kind"] == kind && r["ended_at"] == nil {
				return r
			}
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	view := operations(t, c)
	t.Fatalf("没有 %s 故障，统计=%v，整理错误=%v", kind, view["counts"], view["incident_error"])
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
	view := operations(t, c)
	t.Fatalf("故障 %s 未出现期待的处理阶段，统计=%v，整理错误=%v", id, view["counts"], view["incident_error"])
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
	var latest string
	waitRetention(t, func() bool {
		for _, v := range incidentRows(c) {
			r := v.(map[string]any)
			if r["kind"] == "dns" && r["id"] != incident["id"] {
				latest = r["id"].(string)
				return true
			}
		}
		return false
	}, func() { h.Clock.Advance(time.Second) })
	c.Do("DELETE", "/api/dns-watches/"+id, nil, 204)
	awaitIncidentEntry(t, h, c, latest, func(e map[string]any) bool { return e["stage"] == "配置变更结束" })
	if provider.Writes() != 0 {
		t.Fatal("故障追踪增加 DNS 写入")
	}
}

func TestIncidentsStorageFailureDoesNotBlockEntrySwitch(t *testing.T) {
	disk := &interruptedPersistence{}
	f := newBlockedEntryRig(t, true, func(d *panel.Dependencies) { disk.base = d.Persistence; d.Persistence = disk })
	if _, err := disk.db.Exec(`CREATE TRIGGER fail_operation_incidents BEFORE INSERT ON operation_incidents BEGIN SELECT RAISE(ABORT,'故障投影写入失败'); END; CREATE TRIGGER fail_operation_projection BEFORE UPDATE ON operation_projection BEGIN SELECT RAISE(ABORT,'故障投影提交失败'); END`); err != nil {
		t.Fatal(err)
	}
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	waitRetention(t, func() bool { return operations(t, f.c)["incident_error"] != "" }, func() { f.h.Clock.Advance(time.Second) })
	writes := f.dns.Writes()
	if _, err := disk.db.Exec("DROP TRIGGER fail_operation_incidents; DROP TRIGGER fail_operation_projection"); err != nil {
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
	// 带真实关联形状的历史：时间线、已完成动作及其事件均达到一万条。
	if _, err := disk.db.Exec(`INSERT INTO operation_timeline(incident_id,fact_key,stage,message,source,version,observed_at) SELECT sequence,'scale','历史阶段','已结束的历史事实','既有动作',1,? FROM operation_incidents WHERE source_key LIKE 'scale:%'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.db.Exec(`INSERT INTO rotation_actions(id,node_id,old_ip,new_ip,state,recheck_status,created_at,expires_at,updated_at,secret_name) SELECT 'scale-action:'||sequence,?,'8.8.8.8','1.1.1.1','changed','recovered',?,?,?,'' FROM operation_incidents WHERE source_key LIKE 'scale:%'`, ids[0], now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.db.Exec(`INSERT INTO operation_rotation_links(rotation_id,source_key) SELECT 'scale-action:'||sequence,source_key FROM operation_incidents WHERE source_key LIKE 'scale:%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.db.Exec(`INSERT INTO events(node_id,source,event_id,type,occurred_at,received_at,config_revision,message) SELECT ?,'panel','rotation-notice:scale-action:'||sequence||':changed','ip_rotation_changed',?,?,0,'历史动作已结束' FROM operation_incidents WHERE source_key LIKE 'scale:%'`, ids[0], now, now); err != nil {
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

func TestIncidentsStandaloneRotationRemainsOpenAcrossProjection(t *testing.T) {
	f := newBlockedEntryRig(t, true)
	f.c.Do("POST", "/api/nodes/"+f.ids[0]+"/ip-rotation/actions", nil, 202)
	incident := awaitIncident(t, f.h, f.c, "blocked")
	id := incident["id"].(string)
	for i := 0; i < 3; i++ {
		f.h.Clock.Advance(6 * time.Second)
		time.Sleep(20 * time.Millisecond)
		row := f.c.Do("GET", "/api/incidents/"+id, nil, 200)
		if row["ended_at"] != nil {
			t.Fatalf("进行中的独立动作被错误结束: %v", row)
		}
	}
	if f.calls.Load() != 1 {
		t.Fatal("展示重复调用商家")
	}
}

func TestIncidentsQueriesRequireAuthenticationAndValidateFilters(t *testing.T) {
	h, c := nodeBrowser(t)
	for _, path := range []string{"/api/overview", "/api/incidents", "/api/incidents/1", "/api/incidents/1/timeline"} {
		h.Client().Do("GET", path, nil, 401)
	}
	for _, query := range []string{"limit=101", "before=-1", "kind=invalid", "status=invalid", "from=bad", "from=2030-01-02T00:00:00Z&to=2030-01-01T00:00:00Z"} {
		c.Do("GET", "/api/incidents?"+query, nil, 400)
	}
	c.Do("GET", "/api/incidents/missing/timeline", nil, 404)
}

func TestIncidentsNoCandidateRecoveryCompletesAfterActualPropagation(t *testing.T) {
	lookup := func(_ context.Context, _ string, _ dnswatch.Resolver) ([]string, error) {
		return []string{"4.2.2.2"}, nil
	}
	f := newBlockedEntryRig(t, false, func(d *panel.Dependencies) { d.DNSWatchLookup = lookup })
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "switch_then_rotate"}, 200)
	checker := addNode(f.c, "传播验收检测点")
	cid := checker["id"].(string)
	f.c.Do("PUT", "/api/nodes/"+cid+"/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 204)
	f.h.StartNode(checker["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) { d.DNSWatchLookup = lookup })
	awaitNode(t, f.c, cid, connected)
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	row := awaitIncident(t, f.h, f.c, "blocked")
	id := row["id"].(string)
	f.sites[0].mode.Store(2)
	f.ip[0].Store("4.2.2.2")
	strategySamples(t, f, 6)
	strategyRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	f.awaitPublication(t, f.ids[0], 2)
	waitRetention(t, func() bool {
		f.h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
		v := f.c.Do("GET", "/api/incidents/"+id, nil, 200)
		return v["closure"] == "自动恢复完成"
	}, func() { strategySamples(t, f, 1) })
	if f.calls.Load() != 1 || f.dns.Writes() != 2 {
		t.Fatal("传播追踪增加外部动作")
	}
}

func TestIncidentsPolicyChangeEndsOriginalRecoveryBranch(t *testing.T) {
	f := policyThree(t, true)
	f.block(t, 0)
	awaitStrategyRotation(t, f)
	row := awaitIncident(t, f.h, f.c, "blocked")
	id := row["id"].(string)
	f.c.Do("PUT", "/api/entry-groups/"+f.group+"/blocked-policy", map[string]string{"policy": "disabled"}, 200)
	awaitIncidentEntry(t, f.h, f.c, id, func(e map[string]any) bool { return e["stage"] == "配置变更结束" })
	current := f.c.Do("GET", "/api/incidents/"+id, nil, 200)
	if current["closure"] != "配置变更结束" {
		t.Fatalf("策略改变后原流程仍等待: %v", current)
	}
}
