package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

func openOrderPanel(t *testing.T, dir string) (*Panel, *http.Cookie) {
	t.Helper()
	deps := diskDependencies(dir)
	deps.Passwords = BcryptPasswords{Cost: 4}
	p, err := Open(context.Background(), Config{PublicOrigin: "https://panel.test"}, deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	var configured int
	if err := p.db.QueryRow("SELECT count(*) FROM administrator").Scan(&configured); err != nil {
		t.Fatal(err)
	}
	if configured == 0 {
		diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	}
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	return p, login.Result().Cookies()[0]
}

func listIDs(t *testing.T, p *Panel, cookie *http.Cookie, path, key string) []string {
	t.Helper()
	response := diskRequest(t, p, "GET", path, nil, cookie, 200)
	var body map[string][]struct{ ID string }
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, row := range body[key] {
		ids = append(ids, row.ID)
	}
	return ids
}

func desiredRevision(t *testing.T, p *Panel, node string) int64 {
	t.Helper()
	var revision int64
	if err := p.db.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", node).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

// 升级前没有排序列：迁移后列表保持插入顺序，而不是按随机 id 排。
func TestListOrderMigrationKeepsInsertionOrder(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous := slices.Index(migrations, listOrderMigration)
	for i, m := range migrations[:previous] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{
		`INSERT INTO nodes(id,name,created_at) VALUES('n','入口',0)`,
		`INSERT INTO entry_groups(id,name) VALUES('zg','后建组排第一'),('ag','组二')`,
		`INSERT INTO entry_group_members VALUES('zg','n',0),('ag','n',0)`,
		`INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id) VALUES
			('zz','一','zg','direct',20001,'8.8.8.8','ipv4',443,0,1,''),
			('aa','二','zg','direct',20002,'8.8.8.8','ipv4',443,0,1,''),
			('mm','三','ag','direct',20003,'8.8.8.8','ipv4',443,0,1,'')`,
	} {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p, cookie := openOrderPanel(t, dir)
	var version int
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("迁移版本 %d %v", version, err)
	}
	if got := listIDs(t, p, cookie, "/api/rules", "rules"); !slices.Equal(got, []string{"zz", "aa", "mm"}) {
		t.Fatalf("规则顺序 %v", got)
	}
	if got := listIDs(t, p, cookie, "/api/entry-groups", "entry_groups"); !slices.Equal(got, []string{"zg", "ag"}) {
		t.Fatalf("入口组顺序 %v", got)
	}
}

type orderFixture struct {
	p      *Panel
	cookie *http.Cookie
	node   string
	rules  []string
}

func newOrderFixture(t *testing.T, rules int) orderFixture {
	t.Helper()
	p, cookie := openOrderPanel(t, t.TempDir())
	created := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": "入口"}, cookie, 201)
	var node struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	f := orderFixture{p: p, cookie: cookie, node: node.ID}
	for i := range rules {
		f.rules = append(f.rules, f.createRule(t, fmt.Sprintf("规则%d", i), 21000+i, true))
	}
	return f
}

func (f orderFixture) createRule(t *testing.T, name string, port int, enabled bool) string {
	t.Helper()
	response := diskRequest(t, f.p, "POST", "/api/rules", map[string]any{"name": name, "entry_node_id": f.node, "kind": "direct", "listen_port": port, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": enabled}, f.cookie, 201)
	var rule struct{ ID string }
	if err := json.Unmarshal(response.Body.Bytes(), &rule); err != nil {
		t.Fatal(err)
	}
	return rule.ID
}

func TestSaveListOrderKeepsConfigRevision(t *testing.T) {
	f := newOrderFixture(t, 3)
	before := desiredRevision(t, f.p, f.node)
	if got := listIDs(t, f.p, f.cookie, "/api/rules", "rules"); !slices.Equal(got, f.rules) {
		t.Fatalf("新建规则未按创建顺序: %v", got)
	}
	r := f.rules
	for name, ids := range map[string][]string{
		"缺少": {r[2], r[1]},
		"多余": {r[2], r[1], r[0], "不存在"},
		"重复": {r[2], r[1], r[1]},
		"未知": {r[2], r[1], "不存在"},
	} {
		t.Run(name, func(t *testing.T) {
			diskRequest(t, f.p, "PUT", "/api/rules/order", map[string]any{"ids": ids}, f.cookie, 409)
		})
	}
	reversed := []string{r[2], r[0], r[1]}
	diskRequest(t, f.p, "PUT", "/api/rules/order", map[string]any{"ids": reversed}, f.cookie, 204)
	if got := listIDs(t, f.p, f.cookie, "/api/rules", "rules"); !slices.Equal(got, reversed) {
		t.Fatalf("保存后顺序 %v", got)
	}
	if after := desiredRevision(t, f.p, f.node); after != before {
		t.Fatalf("调整显示顺序改变了配置版本: %d -> %d", before, after)
	}
	// 新建行排在已调整的行之后。
	added := f.createRule(t, "新规则", 21100, true)
	if got := listIDs(t, f.p, f.cookie, "/api/rules", "rules"); !slices.Equal(got, append(slices.Clone(reversed), added)) {
		t.Fatalf("新建规则位置 %v", got)
	}

	second := diskRequest(t, f.p, "POST", "/api/nodes", map[string]string{"name": "第二节点"}, f.cookie, 201)
	var node struct{ ID string }
	if err := json.Unmarshal(second.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	nodes := []string{node.ID, f.node}
	diskRequest(t, f.p, "PUT", "/api/nodes/order", map[string]any{"ids": nodes}, f.cookie, 204)
	if got := listIDs(t, f.p, f.cookie, "/api/nodes", "nodes"); !slices.Equal(got, nodes) {
		t.Fatalf("节点顺序 %v", got)
	}
	groups := []string{}
	for _, name := range []string{"组一", "组二"} {
		created := diskRequest(t, f.p, "POST", "/api/entry-groups", map[string]any{"name": name, "members": []string{f.node}}, f.cookie, 201)
		var group struct{ ID string }
		if err := json.Unmarshal(created.Body.Bytes(), &group); err != nil {
			t.Fatal(err)
		}
		groups = append([]string{group.ID}, groups...)
	}
	diskRequest(t, f.p, "PUT", "/api/entry-groups/order", map[string]any{"ids": groups}, f.cookie, 204)
	if got := listIDs(t, f.p, f.cookie, "/api/entry-groups", "entry_groups"); !slices.Equal(got, groups) {
		t.Fatalf("入口组顺序 %v", got)
	}
	diskRequest(t, f.p, "PUT", "/api/exit-groups/order", map[string]any{"ids": []string{}}, f.cookie, 204)
}

func ruleEnabled(t *testing.T, p *Panel) map[string]bool {
	t.Helper()
	rows, err := p.db.Query("SELECT id,enabled FROM forward_rules")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states := map[string]bool{}
	for rows.Next() {
		var id string
		var enabled bool
		if err := rows.Scan(&id, &enabled); err != nil {
			t.Fatal(err)
		}
		states[id] = enabled
	}
	return states
}

func TestBatchRulesCompilesOnceAndRollsBack(t *testing.T) {
	f := newOrderFixture(t, 3)
	r := f.rules
	revision := desiredRevision(t, f.p, f.node)
	expect := func(step string, delta int64, states map[string]bool) {
		t.Helper()
		if got := desiredRevision(t, f.p, f.node); got != revision+delta {
			t.Fatalf("%s: 配置版本 %d，期望 %d", step, got, revision+delta)
		}
		revision += delta
		if got := ruleEnabled(t, f.p); fmt.Sprint(got) != fmt.Sprint(states) {
			t.Fatalf("%s: 规则状态 %v，期望 %v", step, got, states)
		}
	}
	diskRequest(t, f.p, "POST", "/api/rules/batch", map[string]any{"action": "disable", "ids": r}, f.cookie, 200)
	expect("批量停用", 1, map[string]bool{r[0]: false, r[1]: false, r[2]: false})

	diskRequest(t, f.p, "POST", "/api/rules/batch", map[string]any{"action": "enable", "ids": []string{r[0], "不存在"}}, f.cookie, 404)
	expect("部分不存在", 0, map[string]bool{r[0]: false, r[1]: false, r[2]: false})

	// 停用规则可与启用规则同端口；批量启用触发冲突时整批回滚。
	clash := f.createRule(t, "同端口", 21000, true)
	revision = desiredRevision(t, f.p, f.node)
	diskRequest(t, f.p, "POST", "/api/rules/batch", map[string]any{"action": "enable", "ids": []string{r[1], r[0]}}, f.cookie, 409)
	expect("端口冲突", 0, map[string]bool{r[0]: false, r[1]: false, r[2]: false, clash: true})

	diskRequest(t, f.p, "POST", "/api/rules/batch", map[string]any{"action": "enable", "ids": []string{r[1], r[2]}}, f.cookie, 200)
	expect("批量启用", 1, map[string]bool{r[0]: false, r[1]: true, r[2]: true, clash: true})

	for _, body := range []map[string]any{{"action": "pause", "ids": r}, {"action": "delete", "ids": []string{}}, {"action": "delete", "ids": []string{r[0], r[0]}}} {
		diskRequest(t, f.p, "POST", "/api/rules/batch", body, f.cookie, 400)
	}
	diskRequest(t, f.p, "POST", "/api/rules/batch", map[string]any{"action": "delete", "ids": []string{r[1], r[2], r[0]}}, f.cookie, 200)
	expect("批量删除", 1, map[string]bool{clash: true})
}
