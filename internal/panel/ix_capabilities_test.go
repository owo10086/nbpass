package panel

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

func TestCapabilityRequirementsKeepAddressCapabilitiesIndependentAndCandidateOrder(t *testing.T) {
	caps := capabilitySet{
		"a": {name: "入口", capabilities: map[string]bool{compatibility.IXAddressDialing: true}},
		"b": {name: "主成员", capabilities: map[string]bool{compatibility.OutboundIPv4: true}},
		"c": {name: "备用成员", capabilities: map[string]bool{}},
	}
	public := capabilityRequirements{Required: []capabilityRequirement{{NodeID: "a", Capabilities: []string{compatibility.IXAddressDialing}}}, Hops: [][]capabilityCandidate{{
		{NodeID: "b", AddressID: "public", Required: []capabilityRequirement{{NodeID: "b", Capabilities: []string{compatibility.OutboundIPv4}}}},
		{NodeID: "c", AddressID: "ix-c", Required: []capabilityRequirement{{NodeID: "c", AddressID: "ix-c", Capabilities: []string{compatibility.OutboundIPv4}}}},
	}}}
	plan := caps.evaluate(public)
	if plan.Blocked || len(plan.Blockers) != 1 || plan.Blockers[0].NodeID != "c" || plan.Blockers[0].AddressID != "ix-c" || len(plan.Hops[0]) != 1 || plan.Hops[0][0].NodeID != "b" {
		t.Fatalf("备用不足误阻塞或丢失归属: %+v", plan)
	}
	public.Required = append(public.Required, capabilityRequirement{NodeID: "a", AddressID: "ix", Capabilities: []string{compatibility.IXMemberIdentity}})
	if !caps.evaluate(public).Blocked {
		t.Fatal("拨号能力被当作成员身份能力")
	}
	public.Required = public.Required[:1]
	caps["c"].capabilities[compatibility.OutboundIPv4] = true
	plan = caps.evaluate(public)
	if len(plan.Blockers) != 0 || plan.Hops[0][0].NodeID != "b" || plan.Hops[0][1].NodeID != "c" {
		t.Fatal("恢复能力后改变候选顺序", plan)
	}
	public.Hops = append(public.Hops, []capabilityCandidate{{NodeID: "absent", Required: []capabilityRequirement{{NodeID: "absent", Capabilities: []string{compatibility.ForwardChains}}}}})
	if !caps.evaluate(public).Blocked {
		t.Fatal("跳过没有可用成员的一组")
	}
	if got := caps.missing("a", []string{"unknown", compatibility.IXMemberIdentity}); !reflect.DeepEqual(got, []string{"unknown", compatibility.IXMemberIdentity}) {
		t.Fatal(got)
	}
}

func TestSavedCapabilitiesRequireCurrentAdmittedInstanceAndValidRanges(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	defer p.Close()
	if _, err := p.db.Exec("INSERT INTO nodes(id,name,created_at,process_instance) VALUES('n','节点',0,'current')"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		instance   string
		tunnelMin  uint32
		stateWrite uint32
		want       bool
	}{{"retired", 1, 2, false}, {"current", 2, 2, false}, {"current", 1, 1, false}, {"current", 1, 2, true}} {
		c := nodeCompatibility{ProcessInstance: tc.instance, ControlProtocol: 2, Node: compatibility.Current(), Capabilities: []string{compatibility.IXAddressDialing}}
		c.Node.Capabilities = []string{compatibility.IXAddressDialing}
		c.Node.TunnelMin, c.Node.TunnelMax = tc.tunnelMin, tc.tunnelMin
		c.Node.StateWrite = tc.stateWrite
		tx, err := p.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := p.saveNodeCompatibility(context.Background(), tx, "n", c); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		caps, err := loadCapabilitySet(context.Background(), p.db)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(caps.missing("n", []string{compatibility.IXAddressDialing})) == 0; got != tc.want {
			t.Fatalf("报告范围/实例归属未约束有效能力: %+v", tc)
		}
	}
}

func TestCapabilityBlockersSurviveOfflineButDisabledRulesUseRevocation(t *testing.T) {
	rules := []forwarding.Rule{{ID: "blocked", EntryNodeID: "a", Enabled: true}, {ID: "backup", EntryNodeID: "a", Enabled: true}, {ID: "disabled", EntryNodeID: "a"}, {ID: "ordinary", EntryNodeID: "a", Enabled: true}}
	nodes := map[string]*syncNode{"a": {name: "入口", desired: 4, reported: 4, digest: "d", reportedDigest: "d", revocationsApplied: true, results: map[string]ruleResult{}}}
	views := buildRuleViews(rules, nodes, nil)
	blocker := configurationBlocker{NodeID: "b", NodeName: "备用", AddressID: "ix", Capability: compatibility.IXMemberIdentity, Reason: "需升级服务器"}
	plans := map[[2]string]capabilityPlan{{"a", "blocked"}: {Blocked: true, Blockers: []configurationBlocker{blocker}}, {"a", "backup"}: {Blockers: []configurationBlocker{blocker}}, {"a", "disabled"}: {Blocked: true, Blockers: []configurationBlocker{blocker}}}
	attachCapabilitySync(views, plans)
	if views[0].SyncStatus != syncCapabilityBlocked || views[1].SyncStatus != syncCapabilityUnconfirmed || views[2].SyncStatus != "待确认" || views[3].SyncStatus != "待确认" {
		t.Fatalf("能力原因被离线/其他规则覆盖: %+v", views)
	}
	if len(views[0].ConfigurationBlockers) != 1 || len(views[2].ConfigurationBlockers) != 0 {
		t.Fatal("缺失 blocker 或停用规则仍需能力")
	}
	for _, statuses := range [][]string{{"正常", syncCapabilityDegraded}, {"待确认", syncCapabilityDegraded}} {
		if aggregateSync(statuses) != syncCapabilityDegraded {
			t.Fatal("汇总丢失备用能力不足", statuses)
		}
	}
	if aggregateSync([]string{syncCapabilityDegraded, syncCapabilityBlocked}) != syncCapabilityBlocked {
		t.Fatal("汇总丢失整条阻塞")
	}
	nodes["a"].connected = true
	nodes["a"].results["backup"] = ruleResult{applied: 4}
	views = buildRuleViews(rules, nodes, nil)
	attachCapabilitySync(views, plans)
	if views[2].SyncStatus != "正常" {
		t.Fatal("停用规则未按撤销确认显示正常")
	}
	if views[1].SyncStatus != syncCapabilityDegraded {
		t.Fatal("已生效规则丢失备用不足状态")
	}
	delete(nodes["a"].results, "backup")
	pending := buildRuleViews(rules, nodes, nil)
	attachCapabilitySync(pending, plans)
	if pending[1].SyncStatus != syncCapabilityPending {
		t.Fatal("未应用规则被误报可运行", pending[1])
	}
	nodes["a"].results["backup"] = ruleResult{err: "监听端口已占用"}
	failed := buildRuleViews(rules, nodes, nil)
	attachCapabilitySync(failed, plans)
	if failed[1].SyncStatus != "同步失败" || len(failed[1].ConfigurationBlockers) != 1 || aggregateSync([]string{"同步失败", syncCapabilityDegraded}) != "同步失败" {
		t.Fatal("备用不足掩盖了实际应用失败", failed[1])
	}
	data, _ := json.Marshal(views[0])
	var body map[string]any
	json.Unmarshal(data, &body)
	if len(body["configuration_blockers"].([]any)) != 1 {
		t.Fatal("blocker 未进入规则 API")
	}
}

func TestCapabilityChangeSavesBeforeCompilationAndClearsBothSidesConfirmations(t *testing.T) {
	p, entry, exit, token, snapshot := tunnelAuthorizationProtocolFixture(t)
	rule := snapshot.TunnelServer.Grants[0].Rule
	ctx := context.Background()
	// 同进程重新准入，基础 flag 不变，仅协商集合改变。
	if _, err := p.db.Exec("UPDATE nodes SET process_instance='same' WHERE id=?", exit); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO tunnel_authorization_confirmations(exit_id,entry_id,rule_id,generation) VALUES(?,?,?,?)", exit, entry, rule.Id, rule.AuthorizationGeneration); err != nil {
		t.Fatal(err)
	}
	// 编译会更新授权指纹；触发器证明本事务编译前已经保存本次声明。
	if _, err := p.db.Exec(`CREATE TEMP TRIGGER require_current_capabilities BEFORE UPDATE ON rule_authorizations BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM node_compatibility WHERE node_id='` + exit + `') THEN RAISE(ABORT,'先编译后保存能力') END; END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE rule_authorizations SET fingerprint='force-recompile' WHERE rule_id=?", rule.Id); err == nil {
		t.Fatal("测试触发器未生效")
	}
	if _, err := p.db.Exec("DROP TRIGGER require_current_capabilities"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE rule_authorizations SET fingerprint='force-recompile' WHERE rule_id=?", rule.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`CREATE TEMP TRIGGER require_current_capabilities BEFORE UPDATE ON rule_authorizations BEGIN
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM node_compatibility WHERE node_id='` + exit + `') THEN RAISE(ABORT,'先编译后保存能力') END; END;`); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1)}
	first := &controlpb.NodeReport{ProtocolVersion: 2, ProcessInstance: "same", Compatibility: compatibility.Current(), TunnelAuthorizationVersion: 1}
	if err := p.admitControl(ctx, exit, token, session, first); err != nil {
		t.Fatal("本次声明未在编译前生效", err)
	}
	var confirmations int
	if err := p.db.QueryRow("SELECT count(*) FROM tunnel_authorization_confirmations WHERE exit_id=? OR entry_id=?", exit, exit).Scan(&confirmations); err != nil || confirmations != 0 {
		t.Fatal("能力变化复用旧确认", confirmations, err)
	}
	late := &controlSession{done: make(chan struct{}), instance: "retired"}
	if _, err := p.acceptReport(ctx, exit, token, late, first); err == nil {
		t.Fatal("旧会话仍可补报")
	}
}

func TestEntryCapabilityChangeRequiresNewGrantGenerationEvenWithSameRuleContent(t *testing.T) {
	p, entry, exit, _, snapshot := tunnelAuthorizationProtocolFixture(t)
	rule := snapshot.TunnelServer.Grants[0].Rule
	_, token, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET process_instance='same',token_hash=?,exit_failover_version=1 WHERE id=?", token, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO tunnel_authorization_confirmations(exit_id,entry_id,rule_id,generation) VALUES(?,?,?,?)", exit, entry, rule.Id, rule.AuthorizationGeneration); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1)}
	if err := p.admitControl(context.Background(), entry, token, session, &controlpb.NodeReport{ProtocolVersion: 2, ProcessInstance: "same", Compatibility: compatibility.Current(), TunnelAuthorizationVersion: 1, ExitFailoverVersion: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := p.controlMessage(context.Background(), exit)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot.TunnelServer.Grants[0].Rule.AuthorizationGeneration; got <= rule.AuthorizationGeneration {
		t.Fatalf("清除确认后没有新代次触发持久应用: old=%d new=%d", rule.AuthorizationGeneration, got)
	}
}
