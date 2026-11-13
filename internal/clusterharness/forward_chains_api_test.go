package clusterharness_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"nbpass/internal/platform"
	"path/filepath"
)

func TestForwardChainsAPIHTTPPersistenceIsolationAndHistoricalCompile(t *testing.T) {
	f := newAuthorizationFixture(t)
	rid := f.create(t)
	h, c := f.h, f.c
	reserve := h.ReserveTCPPort
	target := echoTarget(t, "D:")
	directPort := reserve()
	direct := directBody("", directPort, "127.0.0.1", target)
	direct["entry_node_id"] = f.entryID
	dr := c.Do("POST", "/api/rules", direct, 201)
	awaitRule(t, c, dr["id"].(string), "正常")
	// C is a persisted candidate only. The established A->B data plane is real.
	cid := addNode(c, "第三跳候选")["id"].(string)
	c.Do("PUT", "/api/nodes/"+cid+"/tunnel", map[string]any{"address": "ix.example.test", "listen_port": reserve(), "public_port": 3443, "transport": "wss"}, 200)
	addressID := "public"
	cg := c.Do("POST", "/api/exit-groups", map[string]any{"name": "第三组", "strategy": "primary_backup", "member_addresses": []any{map[string]any{"node_id": cid, "address_id": addressID}}}, 201)["id"].(string)
	bg := f.body["exit_group_id"].(string)
	// A pre-upgrade self-hop is injected at the persistence boundary. New saves
	// must reject this shape, while future full compiles must preserve it.
	c.Do("PUT", "/api/nodes/"+f.entryID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": reserve(), "transport": "wss"}, 200)
	ag := c.Do("POST", "/api/exit-groups", map[string]any{"name": "历史自跳组", "members": []string{f.entryID}, "strategy": "primary_backup"}, 201)["id"].(string)
	db, err := (platform.SQLite{Path: filepath.Join(h.Dir, "panel.db")}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacyPort := reserve()
	if _, err = db.Exec(`INSERT INTO forward_rules(id,name,entry_node_id,kind,exit_group_id,listen_port,target_address,target_port,target_family,allow_private,enabled) VALUES('historical-self','历史自跳',?,'tunnel',?,?,'127.0.0.1',?,'ipv4',1,1)`, f.entryID, ag, legacyPort, target); err != nil {
		t.Fatal(err)
	}
	previous := nodeRow(t, c, f.entryID)["process_instance"]
	f.entry.Restart()
	awaitNode(t, c, f.entryID, func(v map[string]any) bool { return connected(v) && v["process_instance"] != previous })
	awaitRule(t, c, "historical-self", "正常") // includes durable grant acknowledgement and another full compile
	awaitRule(t, c, rid, "正常")
	tcpExchange(t, legacyPort, "D:")
	tcpExchange(t, f.port, "T:")
	tcpExchange(t, directPort, "D:")
	// Saving an unrelated rule also compiles without retroactively rejecting history.
	direct["name"] = "无关规则重命名"
	c.Do("PUT", "/api/rules/"+dr["id"].(string), direct, 200)
	awaitRule(t, c, dr["id"].(string), "正常")
	revisions := map[string]any{}
	for _, id := range []string{f.entryID, f.exitID, cid} {
		revisions[id] = nodeRow(t, c, id)["desired_revision"]
	}
	chain := c.Do("POST", "/api/forward-chains", map[string]any{"name": " 复用链 ", "exit_group_ids": []string{bg, cg}}, 201)
	id := chain["id"].(string)
	path := "/api/forward-chains/" + id
	if chain["name"] != "复用链" || chain["rule_count"] != float64(0) {
		t.Fatal(chain)
	}
	hops := chain["hops"].([]any)
	member := hops[1].(map[string]any)["members"].([]any)[0].(map[string]any)
	if member["address_id"] != addressID || member["address"] != "ix.example.test" || member["port"] != float64(3443) || len(member) != 5 {
		t.Fatal(member)
	}
	for _, raw := range hops {
		hop := raw.(map[string]any)
		if len(hop) != 5 || len(hop["observations"].([]any)) != 0 {
			t.Fatal(hop)
		}
	}
	c.Do("DELETE", "/api/exit-groups/"+cg, nil, 409)
	c.Do("PUT", "/api/exit-groups/"+cg, map[string]any{"name": "坏组", "members": []string{cid, f.exitID}, "strategy": "primary_backup"}, 400)
	if got := c.Do("GET", "/api/forward-chains", nil, 200)["chains"].([]any); !reflect.DeepEqual(got, []any{chain}) {
		t.Fatal("失败改变链", got)
	}
	var groupName, address string
	if err = db.QueryRow("SELECT name FROM exit_groups WHERE id=?", cg).Scan(&groupName); err != nil || groupName != "第三组" {
		t.Fatal(groupName, err)
	}
	if err = db.QueryRow("SELECT address_id FROM exit_group_members WHERE group_id=?", cg).Scan(&address); err != nil || address != addressID {
		t.Fatal(address, err)
	}
	chain = c.Do("PUT", path, map[string]any{"name": "改名重排", "exit_group_ids": []string{cg, bg}}, 200)
	if !reflect.DeepEqual(chain["exit_group_ids"], []any{cg, bg}) {
		t.Fatal(chain)
	}
	for _, nodeID := range []string{f.entryID, f.exitID, cid} {
		if row := nodeRow(t, c, nodeID); row["desired_revision"] != revisions[nodeID] {
			t.Fatal("未引用链推进配置", nodeID, row, revisions)
		}
	}
	tcpExchange(t, f.port, "T:")
	tcpExchange(t, directPort, "D:")
	tcpExchange(t, legacyPort, "D:")
	if err = h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	for i := 0; i < 5; i++ {
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitNode(t, c, f.entryID, connected)
	awaitNode(t, c, f.exitID, connected)
	if got := c.Do("GET", "/api/forward-chains", nil, 200)["chains"].([]any); !reflect.DeepEqual(got, []any{chain}) {
		t.Fatal("重启未保留链", got)
	}
	awaitRule(t, c, rid, "正常")
	awaitRule(t, c, "historical-self", "正常")
	beforeDelete := map[string]any{}
	for _, nodeID := range []string{f.entryID, f.exitID, cid} {
		beforeDelete[nodeID] = nodeRow(t, c, nodeID)["desired_revision"]
	}
	c.Do("DELETE", path, nil, 204)
	for _, nodeID := range []string{f.entryID, f.exitID, cid} {
		if after := nodeRow(t, c, nodeID)["desired_revision"]; after != beforeDelete[nodeID] {
			t.Fatal("删除未引用链推进配置", nodeID, beforeDelete, after)
		}
	}
	c.Do("DELETE", path, nil, 404)
	c.Do("DELETE", "/api/exit-groups/"+cg, nil, 204)
	tcpExchange(t, f.port, "T:")
	tcpExchange(t, directPort, "D:")
	tcpExchange(t, legacyPort, "D:")
	// No unused node is started; avoid claiming this management test executes a chain.
}

func TestForwardChainsAPIAuthenticationAndOrigin(t *testing.T) {
	h, c := nodeBrowser(t)
	body := map[string]any{"name": "链", "exit_group_ids": []string{"one", "two"}}
	for _, request := range []struct {
		method, path string
		body         any
	}{{"GET", "/api/forward-chains", nil}, {"POST", "/api/forward-chains", body}, {"PUT", "/api/forward-chains/unknown", body}, {"DELETE", "/api/forward-chains/unknown", nil}} {
		h.Client().Do(request.method, request.path, request.body, 401)
		if request.method == "GET" {
			continue
		}
		response := c.Request(request.method, request.path, request.body, map[string]string{"Origin": "https://other.invalid"})
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal("跨源管理未拒绝", request.method, response.StatusCode)
		}
	}
	if got := c.Do("GET", "/api/forward-chains", nil, 200)["chains"].([]any); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestForwardChainsAPIRuleReferencesTransactionsAndCapabilityGate(t *testing.T) {
	f := newAuthorizationFixture(t)
	ordinary := f.create(t)
	h, c := f.h, f.c
	cid := addNode(c, "末跳")["id"].(string)
	c.Do("PUT", "/api/nodes/"+cid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
	cg := c.Do("POST", "/api/exit-groups", map[string]any{"name": "末跳组", "members": []string{cid}, "strategy": "primary_backup"}, 201)["id"].(string)
	chain := c.Do("POST", "/api/forward-chains", map[string]any{"name": "共享链", "exit_group_ids": []string{f.body["exit_group_id"].(string), cg}}, 201)["id"].(string)
	body := map[string]any{}
	for k, v := range f.body {
		body[k] = v
	}
	delete(body, "id")
	delete(body, "exit_group_id")
	body["chain_id"] = chain
	body["listen_port"] = h.ReserveTCPPort()
	body["name"] = "链入口"
	rid := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, c, rid, "整条阻塞（需升级服务器）")
	tcpExchange(t, f.port, "T:")
	c.Do("DELETE", "/api/forward-chains/"+chain, nil, 409)
	body["enabled"] = false
	body["listen_port"] = h.ReserveTCPPort()
	second := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	list := c.Do("GET", "/api/forward-chains", nil, 200)["chains"].([]any)
	if list[0].(map[string]any)["rule_count"] != float64(2) {
		t.Fatal(list)
	}
	// Every member of an entry group participates, including disabled references.
	c.Do("PUT", "/api/entry-groups/"+body["entry_group_id"].(string), map[string]any{"name": "坏入口", "members": []string{f.entryID, cid}}, 400)
	c.Do("PUT", "/api/exit-groups/"+cg, map[string]any{"name": "入口作备用", "members": []string{cid, f.entryID}, "strategy": "primary_backup"}, 400)
	c.Do("PUT", "/api/forward-chains/"+chain, map[string]any{"name": "合法重排", "exit_group_ids": []string{cg, f.body["exit_group_id"].(string)}}, 200)
	for _, version := range []int{1, 2, 3} {
		doc := map[string]any{"format_version": version, "rules": []any{body}}
		code := 400
		if version == 3 {
			code = 200
		}
		importRules(c, "/preflight", "create", doc, code)
	}
	// One bad changed rule rejects the entire import, including a valid first row.
	bad := map[string]any{}
	for k, v := range body {
		bad[k] = v
	}
	delete(bad, "entry_group_id")
	bad["entry_node_id"] = cid
	doc := map[string]any{"format_version": 3, "rules": []any{body, bad}}
	importRules(c, "/preflight", "create", doc, 400)
	importRules(c, "", "create", doc, 400)
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 3 {
		t.Fatal("partial import")
	}
	db, err := (platform.SQLite{Path: filepath.Join(h.Dir, "panel.db")}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Failure inside generation persistence must roll back rule edits too.
	if _, err = db.Exec(`CREATE TRIGGER fail_chain_authorization BEFORE UPDATE ON rule_authorizations WHEN NEW.rule_id='` + rid + `' BEGIN SELECT RAISE(ABORT,'injected authorization storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	body["target_port"] = 8443
	c.Do("PUT", "/api/rules/"+rid, body, 503)
	if _, err = db.Exec("DROP TRIGGER fail_chain_authorization"); err != nil {
		t.Fatal(err)
	}
	var port int
	if err = db.QueryRow("SELECT target_port FROM forward_rules WHERE id=?", rid).Scan(&port); err != nil || port == 8443 {
		t.Fatal("rule was not rolled back", port, err)
	}
	// Random port and mode conversion share the same references and path checks.
	body["target_port"] = f.body["target_port"]
	body["listen_port"] = 0
	body["random_port"] = true
	random := c.Do("POST", "/api/rules", body, 201)
	if random["listen_port"].(float64) < 1024 {
		t.Fatal(random)
	}
	delete(body, "random_port")
	body["listen_port"] = h.ReserveTCPPort()
	delete(body, "chain_id")
	body["exit_group_id"] = f.body["exit_group_id"]
	c.Do("PUT", "/api/rules/"+second, body, 200)
	delete(body, "exit_group_id")
	body["kind"] = "direct"
	c.Do("PUT", "/api/rules/"+second, body, 200)
	c.Do("POST", "/api/rules/batch", map[string]any{"action": "delete", "ids": []string{rid, random["id"].(string)}}, 200)
	c.Do("DELETE", "/api/forward-chains/"+chain, nil, 204)
	awaitRule(t, c, ordinary, "正常")
	tcpExchange(t, f.port, "T:")
}
