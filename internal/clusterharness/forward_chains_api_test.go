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
	saved := c.Do("PUT", "/api/nodes/"+cid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": reserve(), "transport": "wss", "dedicated_addresses": []any{map[string]any{"name": "专线", "address": "ix.example.test", "public_port": 3443}}}, 200)
	addressID := saved["dedicated_addresses"].([]any)[0].(map[string]any)["id"].(string)
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
	c.Do("PUT", "/api/exit-groups/"+cg, map[string]any{"name": "坏组", "members": []string{cid, f.exitID}, "strategy": "primary_backup"}, 409)
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
