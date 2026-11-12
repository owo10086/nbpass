package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"nbpass/internal/forwarding"
	"strings"
	"testing"
	"time"
)

func TestChainIncidentsReferencesAndConfigurationBoundary(t *testing.T) {
	p, ids, rule := chainConfirmFixture(t)
	ctx := context.Background()
	view, e := p.operationsSnapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(view.Rules) != 1 || len(view.Rules[0].PathNodes) != 3 || len(view.Rules[0].ChainEdges) != 2 {
		t.Fatal("missing complete chain projection", view.Rules)
	}
	for _, node := range ids {
		refs := nodeIncidentRefs(view, node)
		found := false
		for _, ref := range refs {
			found = found || ref.Kind == "rule" && ref.ID == rule
		}
		if !found {
			t.Fatal("intermediate/terminal cannot find affected rule", node, refs)
		}
	}
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	var key string
	if e = p.db.QueryRow("SELECT count(*),source_key FROM operation_incidents WHERE kind='rule' AND ended_at IS NULL").Scan(&count, &key); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	var before int
	if e = p.db.QueryRow("SELECT count(*) FROM operation_timeline").Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	var after int
	p.db.QueryRow("SELECT count(*) FROM operation_timeline").Scan(&after)
	if after != before {
		t.Fatal("projection duplicated timeline")
	}
	if e = p.commitForwarding(ctx, func(tx *sql.Tx) error {
		_, e := tx.Exec("UPDATE tunnel_exits SET public_port=25001 WHERE node_id=?", ids[2])
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	var closure string
	if e = p.db.QueryRow("SELECT closure FROM operation_incidents WHERE source_key=?", key).Scan(&closure); e != nil {
		t.Fatal(e)
	}
	if closure != "新版本替代" {
		t.Fatal("same chain ID address change lost history boundary", closure)
	}
	if e = p.commitForwarding(ctx, func(tx *sql.Tx) error {
		_, e := tx.Exec("UPDATE forward_rules SET enabled=0 WHERE id=?", rule)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	if e = p.db.QueryRow("SELECT closure FROM operation_incidents WHERE kind='rule' ORDER BY sequence DESC LIMIT 1").Scan(&closure); e != nil || closure != "配置变更结束" {
		t.Fatal(closure, e)
	}
}

func TestChainIncidentsSharedNodeReferencesReadFailureRestartAndDeletion(t *testing.T) {
	p, ids, rid := chainConfirmFixture(t)
	ctx := context.Background()
	rules, e := loadRules(ctx, p.db)
	if e != nil {
		t.Fatal(e)
	}
	shared := rules[0]
	shared.ID = "second-shared"
	shared.ListenPort++
	unrelated := shared
	unrelated.ID = "unrelated-direct"
	unrelated.ListenPort++
	unrelated.Kind = forwarding.Direct
	unrelated.ChainID = ""
	unrelated.ExitGroupID = ""
	if e = p.commitForwarding(ctx, func(tx *sql.Tx) error {
		if e := storeRule(ctx, tx, shared, true); e != nil {
			return e
		}
		return storeRule(ctx, tx, unrelated, true)
	}); e != nil {
		t.Fatal(e)
	}
	view, e := p.operationsSnapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, node := range ids[1:] {
		found := map[string]bool{}
		for _, ref := range nodeIncidentRefs(view, node) {
			if ref.Kind == "rule" {
				found[ref.ID] = true
			}
		}
		if !found[rid] || !found[shared.ID] || found[unrelated.ID] {
			t.Fatal("shared intermediate/terminal associations incorrect", found)
		}
	}
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = p.db.QueryRow("SELECT count(*) FROM operation_incidents WHERE kind='rule' AND ended_at IS NULL").Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
	var data []byte
	var encrypted bool
	if e = p.db.QueryRow("SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", ids[1]).Scan(&data, &encrypted); e != nil {
		t.Fatal(e)
	}
	if _, e = p.db.Exec("UPDATE node_snapshots SET snapshot=x'ff',encrypted=0 WHERE node_id=?", ids[1]); e != nil {
		t.Fatal(e)
	}
	if e = p.projectIncidents(ctx); e == nil {
		t.Fatal("unreadable complete snapshot did not fail projection")
	}
	var still int
	p.db.QueryRow("SELECT count(*) FROM operation_incidents WHERE kind='rule' AND ended_at IS NULL").Scan(&still)
	if still != count {
		t.Fatal("read failure closed incidents")
	}
	if _, e = p.db.Exec("UPDATE node_snapshots SET snapshot=?,encrypted=? WHERE node_id=?", data, encrypted, ids[1]); e != nil {
		t.Fatal(e)
	}
	var before int
	p.db.QueryRow("SELECT count(*) FROM operation_timeline").Scan(&before)
	deps := p.deps
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	p, e = Open(ctx, Config{PublicOrigin: "https://panel.test"}, deps)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	var after int
	p.db.QueryRow("SELECT count(*) FROM operation_timeline").Scan(&after)
	if after != before {
		t.Fatal("restart duplicated chain history")
	}
	if e = p.commitForwarding(ctx, func(tx *sql.Tx) error {
		_, e := tx.Exec("DELETE FROM forward_rules WHERE chain_id=?", shared.ChainID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e = p.projectIncidents(ctx); e != nil {
		t.Fatal(e)
	}
	p.db.QueryRow("SELECT count(*) FROM operation_incidents WHERE kind='rule' AND closure='配置变更结束'").Scan(&count)
	if count != 2 {
		t.Fatal("deleted references lost historical endings", count)
	}
	if _, e = p.db.Exec("DELETE FROM forward_chains WHERE id=?", shared.ChainID); e != nil {
		t.Fatal(e)
	}
	var refs string
	if e = p.db.QueryRow("SELECT refs FROM operation_incidents WHERE kind='rule' ORDER BY sequence LIMIT 1").Scan(&refs); e != nil {
		t.Fatal(e)
	}
	for _, node := range ids {
		if !strings.Contains(refs, node) {
			t.Fatal("deleted chain lost node reference snapshot", refs)
		}
	}
}

func TestChainIncidentsTimelinePreservesEverySharedSuccessorEdge(t *testing.T) {
	ctx := context.Background()
	deps := diskDependencies(t.TempDir())
	db, err := deps.Persistence.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	if err = (&Panel{db: db, deps: deps}).migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	rule := operationRule{ID: "rule", Name: "shared C", ChainID: "chain", Configuration: "config", Enabled: true, Status: "abnormal"}
	for _, from := range []string{"b1", "b2"} {
		edge := &chainEdgeEvidence{RuleID: "rule", ChainID: "chain", ChainFingerprint: "fp", FromNodeID: from, ToNodeID: "c", AddressID: "ix", EndpointConfigDigest: "digest", HopIndex: 2, Generation: 7, GrantGeneration: 7, State: "edge_unhealthy", ObservedAt: &now}
		failure := &forwarding.ChainFailureObservation{Sequence: 1, RuleID: "rule", ChainID: "chain", Class: "downstream_next_hop", ObservedAt: now, Edge: forwarding.ChainFailure{FromNodeID: from, ToNodeID: "c", AddressID: "ix", EndpointConfigDigest: "digest", HopIndex: 2, RuleGeneration: 7, GrantGeneration: 7, ChainFingerprint: "fp"}}
		rule.Reasons = append(rule.Reasons, operationReason{Code: "chain_edge_unknown", NodeID: "c", Source: "链路径", Message: "edge unavailable", Chain: edge, At: &now}, operationReason{Code: "chain_flow_failed", NodeID: "a", Source: "链式建流", Message: "forwarded failure", Failure: failure, At: &now})
	}
	save := func() {
		t.Helper()
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		for _, o := range collectRuleIncidents(operationsView{Rules: []operationRule{rule}}) {
			if e = saveIncidentObservation(ctx, tx, o, now); e != nil {
				t.Fatal(e)
			}
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if e := db.QueryRow("SELECT count(*) FROM operation_timeline WHERE stage='规则证据异常'").Scan(&n); e != nil || n != want {
			t.Fatal("lost or duplicated actual edge facts", n, want, e)
		}
	}
	save()
	count(4)
	// All four independent edge facts must retain their actual persisted identity.
	seen := map[string]int{}
	if err = operationRows(ctx, db, "SELECT message FROM operation_timeline WHERE stage='规则证据异常'", func(rows *sql.Rows) error {
		var message string
		if e := rows.Scan(&message); e != nil {
			return e
		}
		var reason operationReason
		if e := json.Unmarshal([]byte(message), &reason); e != nil {
			return e
		}
		from := ""
		if reason.Chain != nil {
			from = reason.Chain.FromNodeID
		} else if reason.Failure != nil {
			from = reason.Failure.Edge.FromNodeID
		}
		seen[from]++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen["b1"] != 2 || seen["b2"] != 2 {
		t.Fatal("shared successor collapsed predecessor facts", seen)
	}
	save()
	count(4)
	// Re-sampling the same probe outcome changes time, not its fact identity.
	later := now.Add(time.Second)
	rule.Reasons[0].At = &later
	rule.Reasons[0].Chain.ObservedAt = &later
	save()
	count(4)
	rule.Reasons[0].Chain.EndpointConfigDigest = "new-address"
	save()
	count(5)
	rule.Reasons[1].Failure.Class = "configuration_authorization"
	save()
	count(6)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = deps.Persistence.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	save()
	count(6)
}
