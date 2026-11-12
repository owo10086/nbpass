package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func forwardChainsAPI(t *testing.T) (*Panel, func(string, string, any, int) map[string]any) {
	t.Helper()
	p := openDiskPanel(t, t.TempDir())
	t.Cleanup(func() { p.Close() })
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	return p, func(method, path string, body any, code int) map[string]any {
		t.Helper()
		data := diskRequest(t, p, method, path, body, cookie, code).Body.Bytes()
		v := map[string]any{}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
		}
		return v
	}
}

func TestForwardChainsAPIValidationOrderAndRollback(t *testing.T) {
	p, request := forwardChainsAPI(t)
	nodes := []string{}
	for _, name := range []string{"A", "B", "C"} {
		id := request("POST", "/api/nodes", map[string]string{"name": name}, 201)["id"].(string)
		request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 24000 + len(nodes), "public_port": 25000 + len(nodes), "transport": "wss"}, 200)
		nodes = append(nodes, id)
	}
	group := func(name string, members []string) string {
		return request("POST", "/api/exit-groups", map[string]any{"name": name, "members": members, "strategy": "primary_backup"}, 201)["id"].(string)
	}
	g1, g2, g3 := group("第一组", nodes[:2]), group("第二组", nodes[2:]), group("重复备用", nodes[1:2])
	for _, body := range []any{
		map[string]any{"name": " ", "exit_group_ids": []string{g1, g2}},
		map[string]any{"name": strings.Repeat("界", 67), "exit_group_ids": []string{g1, g2}},
		map[string]any{"name": "短", "exit_group_ids": []string{g1}},
		map[string]any{"name": "未知", "exit_group_ids": []string{g1, "missing"}},
		map[string]any{"name": "重复组", "exit_group_ids": []string{g1, g1}},
		map[string]any{"name": "备用重复", "exit_group_ids": []string{g1, g3}},
		map[string]any{"name": "未知字段", "exit_group_ids": []string{g1, g2}, "extra": 1},
	} {
		request("POST", "/api/forward-chains", body, 400)
	}
	got := request("POST", "/api/forward-chains", map[string]any{"name": " 链 ", "exit_group_ids": []string{g1, g2}}, 201)
	if len(got) != 5 || got["name"] != "链" || got["rule_count"] != float64(0) {
		t.Fatal(got)
	}
	hops := got["hops"].([]any)
	first := hops[0].(map[string]any)
	if first["position"] != float64(1) || len(first["observations"].([]any)) != 0 || len(first["members"].([]any)) != 2 || first["members"].([]any)[1].(map[string]any)["port"] != float64(25001) {
		t.Fatal(got)
	}
	id := got["id"].(string)
	path := "/api/forward-chains/" + id
	// Inject a failure after the first hop was persisted, then check both create
	// and replace remain atomic (including the previous name and hop order).
	if _, err := p.db.Exec(`CREATE TRIGGER fail_second_chain_hop BEFORE INSERT ON forward_chain_hops WHEN NEW.position=2 BEGIN SELECT RAISE(ABORT,'test storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/forward-chains", map[string]any{"name": "半条不能留下", "exit_group_ids": []string{g2, g1}}, 503)
	request("PUT", path, map[string]any{"name": "改名也回滚", "exit_group_ids": []string{g2, g1}}, 503)
	if _, err := p.db.Exec("DROP TRIGGER fail_second_chain_hop"); err != nil {
		t.Fatal(err)
	}
	if after := request("GET", "/api/forward-chains", nil, 200)["chains"].([]any); !reflect.DeepEqual(after, []any{got}) {
		t.Fatal("链写入故障未回滚", after)
	}

	request("PUT", path, map[string]any{"name": "坏", "exit_group_ids": []string{g1, g3}}, 400)
	request("PUT", "/api/exit-groups/"+g2, map[string]any{"name": "坏组", "members": []string{nodes[2], nodes[1]}, "strategy": "primary_backup"}, 409)
	list := request("GET", "/api/forward-chains", nil, 200)["chains"].([]any)
	if !reflect.DeepEqual(list, []any{got}) {
		t.Fatal("失败未回滚", list, got)
	}
	conflict := request("DELETE", "/api/exit-groups/"+g2, nil, 409)
	if !strings.Contains(fmt.Sprint(conflict), "链") {
		t.Fatal(conflict)
	}
	deletion := request("DELETE", "/api/nodes/"+nodes[2], nil, 409)
	if !strings.Contains(fmt.Sprint(deletion), "转发链") {
		t.Fatal(deletion)
	}
	refs := request("GET", "/api/nodes/"+nodes[2]+"/references", nil, 200)
	if !strings.Contains(fmt.Sprint(refs), "forward_chain") {
		t.Fatal(refs)
	}
	updated := request("PUT", path, map[string]any{"name": strings.Repeat("x", 200), "exit_group_ids": []string{g2, g1}}, 200)
	if !reflect.DeepEqual(updated["exit_group_ids"], []any{g2, g1}) {
		t.Fatal(updated)
	}
	request("PUT", "/api/forward-chains/missing", map[string]any{"name": "无", "exit_group_ids": []string{g1, g2}}, 404)
	request("DELETE", path, nil, 204)
	request("DELETE", path, nil, 404)
	var count int
	if err := p.db.QueryRow("SELECT count(*) FROM forward_chain_hops").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestForwardChainsMigration49To50AndForeignKeys(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:49] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"INSERT INTO nodes(id,name,created_at,desired_revision,outbound_ipv4) VALUES('a','旧节点',1,17,'127.0.0.1')", "INSERT INTO forward_rules(id,name,entry_node_id,kind,listen_port,target_address,target_port,target_family,enabled,allow_private) VALUES('old-rule','旧规则','a','direct',27000,'8.8.8.8',443,'ipv4',1,0)", "INSERT INTO exit_groups(id,name,strategy) VALUES('g','旧组','primary_backup')"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	var version int
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 50 {
		t.Fatal(version, err)
	}
	var oldRevision int
	var oldSource string
	if err = p.db.QueryRow("SELECT desired_revision,outbound_ipv4 FROM nodes WHERE id='a'").Scan(&oldRevision, &oldSource); err != nil || oldRevision != 17 || oldSource != "127.0.0.1" {
		t.Fatal(oldRevision, oldSource, err)
	}
	var oldRules, oldChains int
	if err = p.db.QueryRow("SELECT count(*) FROM forward_rules WHERE id='old-rule' AND name='旧规则' AND entry_node_id='a' AND kind='direct' AND listen_port=27000 AND enabled=1").Scan(&oldRules); err != nil || oldRules != 1 {
		t.Fatal(oldRules, err)
	}
	if err = p.db.QueryRow("SELECT count(*) FROM forward_chains").Scan(&oldChains); err != nil || oldChains != 0 {
		t.Fatal(oldChains, err)
	}
	var oldName string
	if err = p.db.QueryRow("SELECT name FROM nodes WHERE id='a'").Scan(&oldName); err != nil || oldName != "旧节点" {
		t.Fatal(oldName, err)
	}
	if err = p.db.QueryRow("SELECT name FROM exit_groups WHERE id='g'").Scan(&oldName); err != nil || oldName != "旧组" {
		t.Fatal(oldName, err)
	}
	for _, q := range []string{"INSERT INTO forward_chains VALUES('c','链')", "INSERT INTO forward_chain_hops VALUES('c',1,'g')"} {
		if _, err = p.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"DELETE FROM exit_groups WHERE id='g'", "INSERT INTO forward_chain_hops VALUES('c',1,'g')", "INSERT INTO forward_chain_hops VALUES('missing',2,'g')", "INSERT INTO forward_chain_hops VALUES('c',2,'missing')"} {
		if _, err = p.db.Exec(q); err == nil {
			t.Fatal("约束未生效", q)
		}
	}
	if _, err = p.db.Exec("DELETE FROM forward_chains WHERE id='c'"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = p.db.QueryRow("SELECT count(*) FROM forward_chain_hops").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestForwardChainsAffectedSingleHopAndHistoricalCompile(t *testing.T) {
	p, request := forwardChainsAPI(t)
	ids := []string{}
	for _, name := range []string{"A", "B", "C"} {
		id := request("POST", "/api/nodes", map[string]string{"name": name}, 201)["id"].(string)
		ids = append(ids, id)
		request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 26000 + len(ids), "transport": "wss"}, 200)
	}
	group := request("POST", "/api/exit-groups", map[string]any{"name": "出口", "members": []string{ids[1], ids[0]}, "strategy": "primary_backup"}, 201)["id"].(string)
	entry := request("POST", "/api/entry-groups", map[string]any{"name": "入口", "members": []string{ids[2], ids[0]}}, 201)["id"].(string)
	body := map[string]any{"name": "历史", "entry_node_id": ids[0], "kind": "tunnel", "exit_group_id": group, "listen_port": 27000, "target_address": "8.8.8.8", "target_port": 443, "target_family": "ipv4", "enabled": false, "allow_private": false}
	request("POST", "/api/rules", body, 409)
	// Simulate an untouched database rule written by the previous release.
	_, err := p.db.Exec("INSERT INTO forward_rules(id,name,entry_node_id,kind,exit_group_id,listen_port,target_address,target_port,target_family,enabled,allow_private) VALUES('legacy','历史',?,'tunnel',?,27000,'8.8.8.8',443,'ipv4',0,0)", ids[0], group)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.commitForwarding(context.Background(), func(*sql.Tx) error { return nil }); err != nil {
		t.Fatal("历史规则阻塞编译", err)
	}
	unrelated := request("POST", "/api/exit-groups", map[string]any{"name": "无关", "members": []string{ids[2]}, "strategy": "primary_backup"}, 201)["id"].(string)
	request("POST", "/api/forward-chains", map[string]any{"name": "无关链", "exit_group_ids": []string{group, unrelated}}, 201)
	direct := map[string]any{"name": "无关规则", "entry_node_id": ids[2], "kind": "direct", "listen_port": 27001, "target_address": "8.8.8.8", "target_port": 443, "target_family": "ipv4", "enabled": false, "allow_private": false}
	unrelatedRule := request("POST", "/api/rules", direct, 201)
	request("POST", "/api/rules/batch", map[string]any{"action": "disable", "ids": []string{unrelatedRule["id"].(string)}}, 200)
	unrelatedImport := map[string]any{"mode": "create", "document": map[string]any{"format_version": 2, "rules": []any{direct}}}
	request("POST", "/api/rules/import/preflight", unrelatedImport, 200)
	request("POST", "/api/rules/import", unrelatedImport, 200)
	request("PUT", "/api/rules/legacy", body, 409)
	request("PUT", "/api/exit-groups/"+group, map[string]any{"name": "仅改名也影响引用", "members": []string{ids[1], ids[0]}, "strategy": "primary_backup"}, 409)
	request("POST", "/api/rules/batch", map[string]any{"action": "disable", "ids": []string{"legacy"}}, 409)
	imported := map[string]any{}
	for k, v := range body {
		imported[k] = v
	}
	imported["id"] = "legacy"
	doc := map[string]any{"mode": "overwrite", "document": map[string]any{"format_version": 2, "rules": []any{imported}}}
	request("POST", "/api/rules/import/preflight", doc, 400)
	request("POST", "/api/rules/import", doc, 400)
	// A different entry group only becomes invalid when its own referenced members change.
	good := map[string]any{}
	for k, v := range body {
		good[k] = v
	}
	delete(good, "entry_node_id")
	good["entry_group_id"] = entry
	request("POST", "/api/rules", good, 409)
	request("PUT", "/api/exit-groups/"+group, map[string]any{"name": "修复", "members": []string{ids[1]}, "strategy": "primary_backup"}, 200)
	request("POST", "/api/rules", good, 201)
	request("PUT", "/api/entry-groups/"+entry, map[string]any{"name": "坏入口", "members": []string{ids[2], ids[1]}}, 409)
	// Ensure failure did not replace either group.
	var raw string
	err = p.db.QueryRow("SELECT name FROM entry_groups WHERE id=?", entry).Scan(&raw)
	if err != nil || raw != "入口" {
		t.Fatal(raw, err)
	}
	encoded, _ := json.Marshal(request("GET", "/api/rules", nil, 200))
	if !strings.Contains(string(encoded), "legacy") {
		t.Fatal(string(encoded))
	}
}
