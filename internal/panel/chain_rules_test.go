package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"net/netip"
	"strings"
	"testing"
)

func TestChainRulesHTTPReferencesAndImport(t *testing.T) {
	p, request := forwardChainsAPI(t)
	nodes := []string{}
	for i := 0; i < 4; i++ {
		id := request("POST", "/api/nodes", map[string]any{"name": fmt.Sprint(i)}, 201)["id"].(string)
		nodes = append(nodes, id)
		request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 24000 + i, "transport": "wss"}, 200)
	}
	groups := []string{}
	for i := 1; i < 4; i++ {
		groups = append(groups, request("POST", "/api/exit-groups", map[string]any{"name": fmt.Sprint(i), "members": nodes[i : i+1], "strategy": "primary_backup"}, 201)["id"].(string))
	}
	chain := request("POST", "/api/forward-chains", map[string]any{"name": "链", "exit_group_ids": groups[:2]}, 201)["id"].(string)
	body := map[string]any{"name": "链端口", "entry_node_id": nodes[0], "kind": "tunnel", "chain_id": chain, "listen_port": 27000, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true, "allow_private": false}
	rule := request("POST", "/api/rules", body, 201)
	id := rule["id"].(string)
	if rule["chain_id"] != chain {
		t.Fatal(rule)
	}
	list := request("GET", "/api/rules", nil, 200)["rules"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["path"] == nil {
		t.Fatal(list)
	}
	chains := request("GET", "/api/forward-chains", nil, 200)["chains"].([]any)
	if chains[0].(map[string]any)["rule_count"] != float64(1) {
		t.Fatal(chains)
	}
	request("DELETE", "/api/forward-chains/"+chain, nil, 409)
	request("POST", "/api/rules/batch", map[string]any{"action": "disable", "ids": []string{id}}, 200)
	request("DELETE", "/api/forward-chains/"+chain, nil, 409)
	refs := request("GET", "/api/nodes/"+nodes[2]+"/references", nil, 200)
	if !strings.Contains(fmt.Sprint(refs), id) {
		t.Fatal(refs)
	}
	body["entry_node_id"] = nodes[2]
	request("PUT", "/api/rules/"+id, body, 400)
	body["entry_node_id"] = nodes[0]
	for _, v := range []int{1, 2, 3} {
		code := 400
		if v == 3 {
			code = 200
		}
		body["enabled"] = false
		doc := map[string]any{"mode": "create", "document": map[string]any{"format_version": v, "rules": []any{body}}}
		request("POST", "/api/rules/import/preflight", doc, code)
		request("POST", "/api/rules/import", doc, code)
	}
	if request("GET", "/api/rules/export", nil, 200)["format_version"] != float64(3) {
		t.Fatal("export version")
	}
	var count int
	if err := p.db.QueryRow("SELECT count(*) FROM forward_rules WHERE chain_id=?", chain).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	body["chain_id"] = ""
	body["exit_group_id"] = groups[0]
	request("PUT", "/api/rules/"+id, body, 200)
	body["kind"] = "direct"
	delete(body, "exit_group_id")
	request("PUT", "/api/rules/"+id, body, 200)
}

func TestChainRulesMigration51PreservesAllDependencies(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:50] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO nodes(id,name,created_at) VALUES('n','旧节点',1)`,
		`INSERT INTO forward_rules(id,name,entry_node_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled) VALUES('r','旧规则','n','direct',23456,'8.8.8.8','ipv4',443,0,1)`,
		`INSERT INTO rule_traffic_totals VALUES('r',123,456)`,
		`INSERT INTO rule_traffic_cursors VALUES('n','旧进程','r',0,2,123,456)`,
		`INSERT INTO rule_traffic_realtime VALUES('n','r','旧进程',2,1,0,0)`,
		`INSERT INTO rule_traffic_sources VALUES('n','r')`,
		`INSERT INTO rule_authorizations VALUES('r',3,'旧授权指纹')`,
		`INSERT INTO tunnel_authorization_confirmations VALUES('n','n','r',3)`,
		`INSERT INTO forward_chains VALUES('c','链')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tables := []string{"forward_rules", "rule_traffic_totals", "rule_traffic_cursors", "rule_traffic_realtime", "rule_traffic_sources", "rule_authorizations", "tunnel_authorization_confirmations"}
	before := map[string]string{}
	dump := func(q queryer, table string) string {
		rows, err := q.QueryContext(context.Background(), "SELECT * FROM "+table)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		out := []any{}
		for rows.Next() {
			values := make([]any, len(cols))
			args := make([]any, len(cols))
			for i := range values {
				args[i] = &values[i]
			}
			if err = rows.Scan(args...); err != nil {
				t.Fatal(err)
			}
			if table == "forward_rules" && len(values) > 12 {
				values = values[:12]
			}
			out = append(out, values)
		}
		data, _ := json.Marshal(out)
		return string(data)
	}
	for _, table := range tables {
		before[table] = dump(db, table)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	var version int
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal(version, err)
	}
	for _, table := range tables {
		if after := dump(p.db, table); after != before[table] {
			t.Fatal(table, before[table], after)
		}
	}
	var chain sql.NullString
	if err = p.db.QueryRow("SELECT chain_id FROM forward_rules WHERE id='r'").Scan(&chain); err != nil || chain.Valid {
		t.Fatal(chain, err)
	}
	for _, q := range []string{`UPDATE forward_rules SET chain_id='c' WHERE id='r'`, `UPDATE forward_rules SET kind='tunnel' WHERE id='r'`, `UPDATE forward_rules SET kind='tunnel',exit_group_id='g',chain_id='c' WHERE id='r'`, `UPDATE forward_rules SET kind='tunnel',chain_id='missing' WHERE id='r'`, `UPDATE forward_rules SET chain_id='' WHERE id='r'`} {
		if _, err = p.db.Exec(q); err == nil {
			t.Fatal("invalid XOR/FK accepted", q)
		}
	}
	for _, values := range []string{"'direct','g',NULL", "'direct','','c'", "'tunnel','',NULL", "'tunnel','g','c'"} {
		q := "INSERT INTO forward_rules(id,name,entry_node_id,listen_port,target_address,target_family,target_port,allow_private,enabled,kind,exit_group_id,chain_id) VALUES('bad','bad','n',23457,'8.8.8.8','ipv4',443,0,1," + values + ")"
		if _, err = p.db.Exec(q); err == nil {
			t.Fatal("invalid insert accepted", values)
		}
	}
	if _, err = p.db.Exec(`UPDATE forward_rules SET kind='tunnel',chain_id='c' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec(`INSERT INTO node_tunnel_grant_results VALUES('n','n','r',4,9,'error')`); err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec(`DELETE FROM forward_chains WHERE id='c'`); err == nil {
		t.Fatal("referenced chain deleted")
	}
	rows, err := p.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("broken FK")
	}
	rows.Close()
	if _, err = p.db.Exec(`DELETE FROM forward_rules WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range append(tables[1:], "node_tunnel_grant_results") {
		var count int
		if err = p.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}

func TestChainRulesAdjacentSecretsAndFingerprint(t *testing.T) {
	p, request := forwardChainsAPI(t)
	ctx := context.Background()
	ids := []string{}
	for i := 0; i < 6; i++ {
		id := request("POST", "/api/nodes", map[string]any{"name": fmt.Sprint(i)}, 201)["id"].(string)
		ids = append(ids, id)
		if i > 1 {
			request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 24000 + i, "transport": "wss"}, 200)
		}
	}
	group := func(name string, members []string) string {
		return request("POST", "/api/exit-groups", map[string]any{"name": name, "members": members, "strategy": "primary_backup"}, 201)["id"].(string)
	}
	first, last := group("中间", ids[2:4]), group("末跳", ids[4:6])
	entry := request("POST", "/api/entry-groups", map[string]any{"name": "入口", "members": ids[:2]}, 201)["id"].(string)
	chain := request("POST", "/api/forward-chains", map[string]any{"name": "链", "exit_group_ids": []string{first, last}}, 201)["id"].(string)
	body := map[string]any{"name": "链端口", "entry_group_id": entry, "kind": "tunnel", "chain_id": chain, "listen_port": 27000, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true, "allow_private": false}
	rid := request("POST", "/api/rules", body, 201)["id"].(string)
	for _, id := range ids {
		m, err := p.controlMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.GetSnapshot().GetRules()) > 0 || len(m.GetSnapshot().GetTunnelServer().GetGrants()) > 0 {
			t.Fatal("unadvertised chain released", m)
		}
	}
	if err := p.commitForwarding(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE nodes SET process_instance='fixture',tunnel_authorization_version=1,exit_failover_version=1"); err != nil {
			return err
		}
		for _, id := range ids {
			c := nodeCompatibility{ProcessInstance: "fixture", ControlProtocol: 2, Node: compatibility.Current()}
			c.Capabilities = append([]string{}, c.Node.Capabilities...)
			if err := p.saveNodeCompatibility(ctx, tx, id, c); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// 06c confirms the terminal layer before installing upstream grants.
	for _, id := range ids[4:6] {
		m, e := p.controlMessage(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		tx, e := p.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		_, e = p.confirmTunnelAuthorizations(ctx, tx, id, chainReport(m.Snapshot), m.Snapshot.Revision, m.Snapshot.Digest)
		if e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	var pairs int
	if err := p.db.QueryRow("SELECT count(*) FROM tunnel_pairs").Scan(&pairs); err != nil || pairs != 8 {
		t.Fatal("non-adjacent pair", pairs, err)
	}
	var fp string
	var generation uint64
	for i, id := range ids {
		m, err := p.controlMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		s := m.Snapshot
		if i < 2 {
			if s.TunnelServer != nil || len(s.ExitGroups) != 1 {
				t.Fatal("entry secrets", s)
			}
			continue
		}
		if len(s.Rules) != 0 || len(s.TunnelServer.Grants) != 2 || len(s.TunnelServer.Peers) != 2 {
			t.Fatal("relay listener/grants", s)
		}
		for _, g := range s.TunnelServer.Grants {
			if g.Rule.ChainId != chain || g.Rule.AuthorizationGeneration == 0 || g.Rule.ListenPort != 0 {
				t.Fatal(g)
			}
			if fp == "" {
				fp = g.Rule.GetChainAuthorization().GetFingerprint()
				generation = g.Rule.AuthorizationGeneration
			}
			if fp == "" || fp != g.Rule.GetChainAuthorization().GetFingerprint() || generation != g.Rule.AuthorizationGeneration {
				t.Fatal("fingerprint/generation inconsistent", g)
			}
			if i < 4 {
				if g.NextExitGroupId != last || g.Rule.ExitGroupId != last || g.HopIndex != 1 || len(g.AuthorizedNextNodeIds) != 2 || len(s.ExitGroups) != 1 || len(g.Rule.ChainAuthorization.Edges) != 3 {
					t.Fatal("relay authority", g.HopIndex, len(g.Rule.ChainAuthorization.Edges))
				}
			} else {
				if g.NextExitGroupId != "" || g.Rule.ExitGroupId != "" || g.HopIndex != 2 || len(g.AuthorizedNextNodeIds) != 0 || len(s.ExitGroups) != 0 || len(g.Rule.ChainAuthorization.Edges) != 1 {
					t.Fatal("terminal authority", g, s)
				}
			}
		}
	}
	check := func(wantChange bool) {
		t.Helper()
		if err := p.commitForwarding(ctx, func(*sql.Tx) error { return nil }); err != nil {
			t.Fatal(err)
		}
		var nextFP string
		var nextGen uint64
		if err := p.db.QueryRow("SELECT generation,fingerprint FROM rule_authorizations WHERE rule_id=?", rid).Scan(&nextGen, &nextFP); err != nil {
			t.Fatal(err)
		}
		if wantChange {
			if nextFP == fp || nextGen <= generation {
				t.Fatal("unchanged authorization", nextFP, nextGen)
			}
		} else if fp != nextFP || generation != nextGen {
			t.Fatal("unstable authorization", nextFP, nextGen)
		}
		fp, generation = nextFP, nextGen
	}
	check(false)
	if _, err := p.db.Exec("UPDATE tunnel_exits SET public_port=24443 WHERE node_id=?", ids[5]); err != nil {
		t.Fatal(err)
	}
	check(true)
	check(false)
	if _, err := p.db.Exec("UPDATE tunnel_exits SET address='192.0.2.55' WHERE node_id=?", ids[5]); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := p.db.Exec("UPDATE forward_rules SET target_port=8443 WHERE id=?", rid); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := p.db.Exec("UPDATE exit_group_members SET position=position+10 WHERE group_id=?", last); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE exit_group_members SET position=CASE node_id WHEN ? THEN 1 ELSE 0 END WHERE group_id=?", ids[4], last); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := p.db.Exec("UPDATE nodes SET outbound_ipv4='127.0.0.1' WHERE id=?", ids[5]); err != nil {
		t.Fatal(err)
	}
	check(true)
	request("PUT", "/api/forward-chains/"+chain, map[string]any{"name": "链", "exit_group_ids": []string{last, first}}, 200)
	check(true)
}

func TestChainCapabilityPlanKeepsUsablePublicPath(t *testing.T) {
	c := capabilitySet{}
	for _, id := range []string{"a", "b1", "b2", "c1", "c2"} {
		c[id] = nodeCapabilities{name: id, capabilities: map[string]bool{compatibility.ForwardChains: true, "tunnel_authorization": true, "exit_failover": true}}
	}
	c["b2"].capabilities[compatibility.IXAddressDialing] = true
	groups := map[string]exitGroupView{
		"b": {ID: "b", Members: []string{"b1", "b2"}, MemberAddresses: []memberAddress{{NodeID: "b1", AddressID: "public"}, {NodeID: "b2", AddressID: "public"}}},
		"c": {ID: "c", Members: []string{"c1", "c2"}, MemberAddresses: []memberAddress{{NodeID: "c1", AddressID: "public"}, {NodeID: "c2", AddressID: "public"}}},
	}
	addresses := map[string]tunnelExitView{}
	for _, id := range []string{"b1", "b2", "c1", "c2"} {
		addresses[id] = tunnelExitView{NodeID: id, Address: "192.0.2.1"}
		if id == "b2" || id == "c2" {
			addresses[id] = tunnelExitView{NodeID: id, Address: "exit.test"}
		}
	}
	r := forwarding.Rule{ChainID: "chain"}
	plan := chainCapabilityPlan(r, "a", []string{"b", "c"}, groups, c, addresses, nil)
	if plan.Blocked || len(plan.Blockers) == 0 || len(plan.Hops[0]) != 1 || plan.Hops[0][0].NodeID != "b1" || len(plan.Hops[1]) != 1 || plan.Edges[[2]string{"a", "b2"}] || plan.Edges[[2]string{"b1", "c2"}] {
		t.Fatal("backup address blocked public path", plan)
	}
	c["a"].capabilities[compatibility.IXAddressDialing] = true
	plan = chainCapabilityPlan(r, "a", []string{"b", "c"}, groups, c, addresses, nil)
	if plan.Blocked || len(plan.Hops[0]) != 2 || len(plan.Hops[1]) != 2 || !plan.Edges[[2]string{"b2", "c2"}] || plan.Edges[[2]string{"b1", "c2"}] {
		t.Fatal("per-predecessor edge permissions lost", plan)
	}
	delete(c["c1"].capabilities, compatibility.ForwardChains)
	plan = chainCapabilityPlan(r, "a", []string{"b", "c"}, groups, c, addresses, nil)
	if plan.Blocked || len(plan.Hops[0]) != 1 || plan.Hops[0][0].NodeID != "b2" {
		t.Fatal("no complete suffix must prune b1", plan)
	}
}

func TestChainSnapshot60KiBBoundaryAndImportRollback(t *testing.T) {
	for _, size := range []int{60 << 10, (60 << 10) + 1} {
		s := &controlpb.ConfigSnapshot{Digest: strings.Repeat("x", size-4)}
		if proto.Size(s) != size {
			t.Fatal(proto.Size(s), size)
		}
		if err := validateSnapshotWireSize(s); (err == nil) != (size == 60<<10) {
			t.Fatal(size, err)
		}
	}
	p, request := forwardChainsAPI(t)
	id := request("POST", "/api/nodes", map[string]any{"name": "入口"}, 201)["id"].(string)
	var before uint64
	if err := p.db.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rows := []any{}
	target := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for i := 0; i < 250; i++ {
		rows = append(rows, map[string]any{"name": fmt.Sprint(i), "entry_node_id": id, "kind": "direct", "listen_port": 30000 + i, "target_address": target, "target_family": "ipv4", "target_port": 443, "allow_private": false, "enabled": true})
	}
	body := map[string]any{"mode": "create", "document": map[string]any{"format_version": 3, "rules": rows}}
	for _, path := range []string{"/api/rules/import/preflight", "/api/rules/import"} {
		response := request("POST", path, body, 503)
		if len(fmt.Sprint(response)) > 1024 {
			t.Fatal("unbounded error")
		}
	}
	var count int
	var after uint64
	if err := p.db.QueryRow("SELECT count(*) FROM forward_rules").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err := p.db.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", id).Scan(&after); err != nil || after != before {
		t.Fatal(before, after, err)
	}
}

func TestChainRulesStaticLoopsAndActualListeners(t *testing.T) {
	p, request := forwardChainsAPI(t)
	p.deps.LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "proven.test" {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, fmt.Errorf("unresolved")
	}
	ids := []string{}
	for i := 0; i < 4; i++ {
		id := request("POST", "/api/nodes", map[string]any{"name": fmt.Sprint(i)}, 201)["id"].(string)
		ids = append(ids, id)
		request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "192.0.2." + fmt.Sprint(i+1), "listen_port": 24000 + i, "public_port": 27002, "transport": "wss"}, 200)
		if _, err := p.db.Exec("INSERT INTO node_regions(node_id,public_ipv4) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET public_ipv4=excluded.public_ipv4", id, "192.0.2."+fmt.Sprint(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	group := func(name string, m []string) string {
		return request("POST", "/api/exit-groups", map[string]any{"name": name, "members": m, "strategy": "primary_backup"}, 201)["id"].(string)
	}
	mid, last := group("中间", ids[1:2]), group("末跳主备", ids[2:])
	chain := request("POST", "/api/forward-chains", map[string]any{"name": "链", "exit_group_ids": []string{mid, last}}, 201)["id"].(string)
	direct := func(node string, port int, target string, to int) map[string]any {
		return map[string]any{"name": "本机服务", "entry_node_id": node, "kind": "direct", "listen_port": port, "target_address": target, "target_family": "ipv4", "target_port": to, "allow_private": true, "enabled": true}
	}
	request("POST", "/api/rules", direct(ids[3], 27001, "192.0.2.1", 27000), 201)
	for _, mode := range []string{"single", "chain"} {
		for _, host := range []string{"127.0.0.1", "localhost", "proven.test", "192.0.2.4"} {
			body := direct(ids[0], 27000, host, 27001)
			body["kind"] = "tunnel"
			if mode == "single" {
				body["exit_group_id"] = last
			} else {
				body["chain_id"] = chain
			}
			response := request("POST", "/api/rules", body, 409)
			if !strings.Contains(fmt.Sprint(response), "循环") {
				t.Fatal(response)
			}
		}
	}
	for _, mode := range []string{"single", "chain"} {
		for _, host := range []string{"127.0.0.1", "localhost", "unresolved.test"} {
			body := direct(ids[0], 27000, host, 27000)
			body["kind"] = "tunnel"
			if mode == "single" {
				body["exit_group_id"] = last
			} else {
				body["chain_id"] = chain
			}
			created := request("POST", "/api/rules", body, 201)
			request("DELETE", "/api/rules/"+created["id"].(string), nil, 204)
		}
	}
	request("POST", "/api/rules", direct(ids[1], 24001, "8.8.8.8", 443), 409)
	request("POST", "/api/rules", direct(ids[1], 18080, "8.8.8.8", 443), 409)
	// External NAT port is a dialing choice, never a local reservation.
	request("POST", "/api/rules", direct(ids[1], 27002, "8.8.8.8", 443), 201)
}

func TestChainRulesSharedEditChecksEveryDisabledReference(t *testing.T) {
	p, request := forwardChainsAPI(t)
	ids := []string{}
	for i := 0; i < 4; i++ {
		id := request("POST", "/api/nodes", map[string]any{"name": fmt.Sprint(i)}, 201)["id"].(string)
		ids = append(ids, id)
		request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 24000 + i, "transport": "wss"}, 200)
	}
	groups := []string{}
	for i := 1; i < 4; i++ {
		groups = append(groups, request("POST", "/api/exit-groups", map[string]any{"name": fmt.Sprint(i), "members": ids[i : i+1], "strategy": "primary_backup"}, 201)["id"].(string))
	}
	chain := request("POST", "/api/forward-chains", map[string]any{"name": "原链", "exit_group_ids": groups[:2]}, 201)["id"].(string)
	for _, entry := range []string{ids[0], ids[3]} {
		request("POST", "/api/rules", map[string]any{"name": "停用引用", "entry_node_id": entry, "kind": "tunnel", "chain_id": chain, "listen_port": 27000, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": false}, 201)
	}
	bad := request("PUT", "/api/forward-chains/"+chain, map[string]any{"name": "只第二条冲突", "exit_group_ids": []string{groups[0], groups[2]}}, 400)
	if !strings.Contains(fmt.Sprint(bad), ids[3]) {
		t.Fatal("missing conflicting server", bad)
	}
	var name string
	if err := p.db.QueryRow("SELECT name FROM forward_chains WHERE id=?", chain).Scan(&name); err != nil || name != "原链" {
		t.Fatal(name, err)
	}
}

func TestChainRulesNeverUseLegacyZeroGeneration(t *testing.T) {
	r := &controlpb.ForwardRule{Id: "r", ChainId: "chain", Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, TargetAddress: "8.8.8.8", TargetPort: 443}
	if sameLegacyAuthorization(r, proto.Clone(r).(*controlpb.ForwardRule)) {
		t.Fatal("chain accepted legacy zero generation")
	}
}
