package clusterharness_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

func rulesDocument(rules ...map[string]any) map[string]any {
	return map[string]any{"format_version": 1, "rules": rules}
}

func importRules(c *clusterharness.Client, path, mode string, document map[string]any, status int) map[string]any {
	return c.Do("POST", "/api/rules/import"+path, map[string]any{"mode": mode, "document": document}, status)
}

// 导出若改为复用同步视图或节点快照，会混入运行状态或隧道秘密。
func TestRulesJSONExportContainsOnlyVersionedRuleDefinitions(t *testing.T) {
	f := newAuthorizationFixture(t)
	id := f.create(t)
	f.c.Do("PUT", "/api/secrets/export-canary", map[string]any{"value": "must-never-export-this-secret"}, 200)
	direct := directBody(f.body["entry_group_id"].(string), 23102, "example.com", 443)
	direct["enabled"] = false
	f.c.Do("POST", "/api/rules", direct, 201)
	doc := f.c.Do("GET", "/api/rules/export", nil, 200)
	if len(doc) != 2 || doc["format_version"] != float64(1) || len(doc["rules"].([]any)) != 2 {
		t.Fatalf("导出缺少格式版本或规则: %v", doc)
	}
	allowed := map[string]bool{"id": true, "name": true, "entry_group_id": true, "kind": true, "exit_group_id": true, "listen_port": true, "target_address": true, "target_family": true, "target_port": true, "allow_private": true, "enabled": true}
	for _, value := range doc["rules"].([]any) {
		for field := range value.(map[string]any) {
			if !allowed[field] {
				t.Fatalf("导出混入非规则字段 %q", field)
			}
		}
	}
	data, _ := json.Marshal(doc)
	if strings.Contains(string(data), "must-never-export") || strings.Contains(string(data), "BEGIN ") {
		t.Fatal("导出泄露秘密或证书材料")
	}
	selected := f.c.Do("GET", "/api/rules/export?id="+id, nil, 200)["rules"].([]any)
	if len(selected) != 1 || selected[0].(map[string]any)["id"] != id {
		t.Fatal("导出选择未生效")
	}
	f.c.Do("GET", "/api/rules/export?id=missing", nil, 404)
	f.h.Client().Do("GET", "/api/rules/export", nil, 401)
}

// 预检若写入、create 沿用导出 ID、overwrite 偷偷新增，此测试会失败。
func TestRulesJSONCreateAndOverwriteAreExplicitAndPreflightDoesNotWrite(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "导入入口")
	id := n["id"].(string)
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	g := entryGroup(c, id)["id"].(string)
	body := directBody(g, h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "J:"))
	body["id"] = "source-rule-id"
	doc := rulesDocument(body)
	before := nodeRow(t, c, id)["desired_revision"]
	preview := importRules(c, "/preflight", "create", doc, 200)
	if preview["valid"] != true || preview["results"].([]any)[0].(map[string]any)["action"] != "create" {
		t.Fatalf("预检未说明新增: %v", preview)
	}
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 || nodeRow(t, c, id)["desired_revision"] != before {
		t.Fatal("预检写入规则或推进版本")
	}
	result := importRules(c, "", "create", doc, 200)
	createdID := result["results"].([]any)[0].(map[string]any)["id"].(string)
	if createdID == "" || createdID == body["id"] {
		t.Fatal("新增导入未分配新 ID")
	}
	awaitRule(t, c, createdID, "正常")
	tcpExchange(t, body["listen_port"].(int), "J:")
	body["id"], body["name"], body["target_port"] = createdID, "覆盖后的规则", echoTarget(t, "K:")
	preview = importRules(c, "/preflight", "overwrite", doc, 200)
	if preview["results"].([]any)[0].(map[string]any)["action"] != "overwrite" {
		t.Fatal("预检未说明覆盖")
	}
	importRules(c, "", "overwrite", doc, 200)
	awaitRule(t, c, createdID, "正常")
	tcpExchange(t, body["listen_port"].(int), "K:")
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	rows := c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != createdID || rows[0].(map[string]any)["name"] != "覆盖后的规则" {
		t.Fatalf("覆盖未保持 ID 或未持久化: %v", rows)
	}
	body["id"] = "not-existing"
	importRules(c, "", "overwrite", doc, 400)
}

// 每条错误都须返回，合法的首条也不能提前写入或推进 revision。
func TestRulesJSONPreflightAggregatesErrorsAndRollsBackWholeBatch(t *testing.T) {
	_, c := nodeBrowser(t)
	id := addNode(c, "批量入口")["id"].(string)
	g := entryGroup(c, id)["id"].(string)
	before := nodeRow(t, c, id)["desired_revision"]
	good := directBody(g, 23201, "example.com", 443)
	missing := directBody("missing-entry", 23202, "example.com", 443)
	badType := directBody(g, 23203, "example.com", 443)
	badType["listen_port"] = "not-a-number"
	reserved := directBody(g, 18080, "example.com", 443)
	badExit := directBody(g, 23204, "example.com", 443)
	badExit["kind"], badExit["exit_group_id"] = "tunnel", "missing-exit"
	otherReserved := directBody(g, 18443, "example.com", 443)
	doc := rulesDocument(good, missing, badType, reserved, badExit, otherReserved)
	for _, path := range []string{"/preflight", ""} {
		result := importRules(c, path, "create", doc, 400)
		rows := result["results"].([]any)
		if result["valid"] != false || len(rows) != 6 {
			t.Fatalf("未逐条返回预检结果: %v", result)
		}
		for i, value := range rows {
			row := value.(map[string]any)
			if row["index"] != float64(i+1) || (len(row["errors"].([]any)) > 0) != (i > 0) {
				t.Fatalf("第 %d 条错误归属不正确: %v", i+1, row)
			}
		}
	}
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 || nodeRow(t, c, id)["desired_revision"] != before {
		t.Fatal("失败导入留下部分规则或配置版本")
	}
}

func TestRulesJSONRejectsAmbiguousFormatsAndModes(t *testing.T) {
	_, c := nodeBrowser(t)
	g := entryGroup(c, addNode(c, "格式入口")["id"].(string))["id"].(string)
	for _, mutate := range []func(map[string]any, map[string]any){
		func(doc, _ map[string]any) { doc["format_version"] = 2 },
		func(doc, _ map[string]any) { delete(doc, "format_version") },
		func(doc, _ map[string]any) { doc["rules"] = nil },
		func(doc, _ map[string]any) { doc["token"] = "secret" },
		func(_, row map[string]any) { row["credential"] = "secret" },
		func(_, row map[string]any) { delete(row, "enabled") },
		func(_, row map[string]any) { row["enabled"] = nil },
		func(doc, row map[string]any) { row["id"] = "duplicate"; doc["rules"] = []any{row, row} },
	} {
		body := directBody(g, 23301, "example.com", 443)
		doc := rulesDocument(body)
		mutate(doc, body)
		importRules(c, "", "create", doc, 400)
	}
	for _, mode := range []string{"", "upsert", "replace"} {
		importRules(c, "", mode, rulesDocument(directBody(g, 23301, "example.com", 443)), 400)
	}
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 {
		t.Fatal("格式错误仍写入了规则")
	}
}

func TestRulesJSONValidatesFinalBatchAndRechecksAfterPreflight(t *testing.T) {
	_, c := nodeBrowser(t)
	id := addNode(c, "事务入口")["id"].(string)
	g := entryGroup(c, id)["id"].(string)
	a := c.Do("POST", "/api/rules", directBody(g, 23401, "example.com", 443), 201)
	b := c.Do("POST", "/api/rules", directBody(g, 23402, "example.com", 443), 201)
	a["listen_port"], b["listen_port"] = b["listen_port"], a["listen_port"]
	doc := rulesDocument(a, b)
	importRules(c, "/preflight", "overwrite", doc, 200)
	importRules(c, "", "overwrite", doc, 200) // 端口交换按最终配置校验。
	fresh := directBody(g, 23403, "example.com", 443)
	newDoc := rulesDocument(fresh)
	importRules(c, "/preflight", "create", newDoc, 200)
	c.Do("POST", "/api/rules", fresh, 201)
	before := c.Do("GET", "/api/rules", nil, 200)
	importRules(c, "", "create", newDoc, 400)
	if after := c.Do("GET", "/api/rules", nil, 200); !reflect.DeepEqual(before, after) {
		t.Fatal("预检后的并发冲突被绕过")
	}
	// 批内相互冲突及跨规则循环也必须在写入之前发现。
	conflict := rulesDocument(directBody(g, 23404, "example.com", 443), directBody(g, 23404, "example.com", 443), directBody(g, 23404, "example.com", 443))
	result := importRules(c, "", "create", conflict, 400)
	for _, value := range result["results"].([]any) {
		if len(value.(map[string]any)["errors"].([]any)) == 0 {
			t.Fatal("批内冲突没有标到双方")
		}
	}
	loop := rulesDocument(directBody(g, 23405, "localhost", 23406), directBody(g, 23406, "localhost", 23405))
	importRules(c, "", "create", loop, 400)
}

func TestRulesJSONRejectsDuplicateJSONKeys(t *testing.T) {
	_, c := nodeBrowser(t)
	g := entryGroup(c, addNode(c, "重复键入口")["id"].(string))["id"].(string)
	doc, _ := json.Marshal(rulesDocument(directBody(g, 23501, "example.com", 443)))
	for _, raw := range []string{
		`{"mode":"overwrite","mode":"create","document":` + string(doc) + `}`,
		`{"mode":"overwrite","MODE":"create","document":` + string(doc) + `}`,
		`{"mode":"create","document":` + strings.Replace(string(doc), `"format_version":1`, `"format_version":2,"format_version":1`, 1) + `}`,
		`{"mode":"create","document":` + strings.Replace(string(doc), `"enabled":true`, `"enabled":false,"enabled":true`, 1) + `}`,
		`{"mode":"create","document":` + strings.Replace(string(doc), `"enabled":true`, `"enabled":false,"Enabled":true`, 1) + `}`,
		`{"mode":"create","document":` + strings.Replace(string(doc), `"listen_port":23501`, `"listen_port":23501,"liſten_port":23502`, 1) + `}`,
		`{"mode":"create","document":` + strings.Replace(string(doc), `"kind":"direct"`, `"kind":"direct","Kind":"direct"`, 1) + `}`,
	} {
		c.Do("POST", "/api/rules/import", json.RawMessage(raw), 400)
	}
	if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 {
		t.Fatal("含重复 JSON 键的歧义输入写入成功")
	}
}

func TestRulesJSONConflictDetailsStayBoundedPerRow(t *testing.T) {
	_, c := nodeBrowser(t)
	members := []string{}
	for range 3 {
		members = append(members, addNode(c, "冲突成员")["id"].(string))
	}
	g := c.Do("POST", "/api/entry-groups", map[string]any{"name": "批量冲突", "members": members}, 201)["id"].(string)
	batch := []map[string]any{}
	for range 12 {
		batch = append(batch, directBody(g, 23701, "example.com", 443))
	}
	result := importRules(c, "", "create", rulesDocument(batch...), 400)
	for _, value := range result["results"].([]any) {
		row := value.(map[string]any)
		if len(row["listeners"].([]any)) > 2 || len(row["errors"].([]any)) != 1 || row["conflict_count"] != float64(3) {
			t.Fatal("每行重复整组冲突证据，响应呈平方增长")
		}
	}
}

func TestRulesJSONReportsAllSharedRuleCyclesAcrossMembers(t *testing.T) {
	_, c := nodeBrowser(t)
	a, b := addNode(c, "循环成员 A")["id"].(string), addNode(c, "循环成员 B")["id"].(string)
	g := c.Do("POST", "/api/entry-groups", map[string]any{"name": "跨成员循环", "members": []string{a, b}}, 201)["id"].(string)
	ga, gb := entryGroup(c, a)["id"].(string), entryGroup(c, b)["id"].(string)
	doc := rulesDocument(directBody(g, 23801, "localhost", 23802), directBody(ga, 23802, "localhost", 23801), directBody(gb, 23802, "localhost", 23801))
	result := importRules(c, "", "create", doc, 400)
	for _, value := range result["results"].([]any) {
		if len(value.(map[string]any)["errors"].([]any)) == 0 {
			t.Fatal("共享规则使另一成员上的环路被漏标")
		}
	}
}

// 导入若绕开现有编译器，出口确认前会提前在入口监听。
func TestRulesJSONImportKeepsTunnelAuthorizationGate(t *testing.T) {
	store := &faultyConfigStore{}
	f := newAuthorizationFixture(t, func(_ *node.Config, d *node.Dependencies) {
		store.ConfigStore = d.Store.(node.ConfigStore)
		d.ConfigStore = store
	})
	store.fail.Store(true)
	before := nodeRow(t, f.c, f.exitID)["desired_revision"]
	importRules(f.c, "/preflight", "create", rulesDocument(f.body), 200)
	if len(f.c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 || nodeRow(t, f.c, f.exitID)["desired_revision"] != before {
		t.Fatal("隧道预检发布了规则、授权或配置版本")
	}
	result := importRules(f.c, "", "create", rulesDocument(f.body), 200)
	id := result["results"].([]any)[0].(map[string]any)["id"].(string)
	awaitNode(t, f.c, f.exitID, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	awaitNode(t, f.c, f.entryID, func(row map[string]any) bool { return row["desired_revision"] == row["applied_revision"] })
	assertNoAuthorizationListener(t, f)
	store.fail.Store(false)
	f.h.Clock.Advance(10 * time.Second)
	awaitRule(t, f.c, id, "正常")
	tcpExchange(t, f.port, "T:")
	f.body["id"], f.body["target_port"] = id, echoTarget(t, "N:")
	store.fail.Store(true)
	importRules(f.c, "", "overwrite", rulesDocument(f.body), 200)
	awaitNode(t, f.c, f.exitID, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	awaitNode(t, f.c, f.entryID, func(row map[string]any) bool { return row["desired_revision"] == row["applied_revision"] })
	assertNoAuthorizationListener(t, f)
	store.fail.Store(false)
	f.h.Clock.Advance(10 * time.Second)
	awaitRule(t, f.c, id, "正常")
	tcpExchange(t, f.port, "N:")
}

func TestRulesJSONStorageFailureRollsBackEarlierRowsAndSnapshots(t *testing.T) {
	persistence := &interruptedPersistence{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		d.Passwords = panel.BcryptPasswords{Cost: 4}
		persistence.base, d.Persistence = d.Persistence, persistence
	})
	c := h.Client()
	setup(c)
	login(c)
	id := addNode(c, "落盘失败入口")["id"].(string)
	g := entryGroup(c, id)["id"].(string)
	before := nodeRow(t, c, id)["desired_revision"]
	if _, err := persistence.db.Exec(`CREATE TRIGGER fail_import BEFORE INSERT ON forward_rules WHEN NEW.name='故障规则' BEGIN SELECT RAISE(ABORT,'测试注入'); END`); err != nil {
		t.Fatal(err)
	}
	bad := directBody(g, 23602, "example.com", 443)
	bad["name"] = "故障规则"
	for _, path := range []string{"/preflight", ""} {
		importRules(c, path, "create", rulesDocument(directBody(g, 23601, "example.com", 443), bad), 503)
		if len(c.Do("GET", "/api/rules", nil, 200)["rules"].([]any)) != 0 || nodeRow(t, c, id)["desired_revision"] != before {
			t.Fatal("后行写入失败未回滚首行或配置版本")
		}
	}
}

func TestRulesJSONImportRequiresAuthenticationAndSameOrigin(t *testing.T) {
	h, c := nodeBrowser(t)
	for _, path := range []string{"/api/rules/import", "/api/rules/import/preflight"} {
		h.Client().Do("POST", path, map[string]any{}, 401)
		response := c.Request("POST", path, map[string]any{}, map[string]string{"Origin": "https://other.example"})
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal("跨源规则导入被放行")
		}
	}
}
