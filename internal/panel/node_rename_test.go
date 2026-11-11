package panel

import (
	"encoding/json"
	"strings"
	"testing"
)

// 节点名称只用于展示，改名不应触发配置下发。
func TestRenameNodeKeepsConfigRevision(t *testing.T) {
	p, cookie := openOrderPanel(t, t.TempDir())
	created := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": "旧名称"}, cookie, 201)
	var node struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	before := desiredRevision(t, p, node.ID)
	diskRequest(t, p, "PUT", "/api/nodes/"+node.ID+"/name", map[string]string{"name": "  HK-CN2-01  "}, cookie, 204)
	var name string
	if err := p.db.QueryRow("SELECT name FROM nodes WHERE id=?", node.ID).Scan(&name); err != nil || name != "HK-CN2-01" {
		t.Fatalf("改名结果 %q %v", name, err)
	}
	if after := desiredRevision(t, p, node.ID); after != before {
		t.Fatalf("改名改变了配置版本: %d -> %d", before, after)
	}
	diskRequest(t, p, "PUT", "/api/nodes/"+node.ID+"/name", map[string]string{"name": "   "}, cookie, 400)
	diskRequest(t, p, "PUT", "/api/nodes/"+node.ID+"/name", map[string]string{"name": strings.Repeat("x", 201)}, cookie, 400)
	diskRequest(t, p, "PUT", "/api/nodes/missing/name", map[string]string{"name": "新名称"}, cookie, 404)
}
