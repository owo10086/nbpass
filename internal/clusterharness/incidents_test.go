package clusterharness_test

import (
	"nbpass/internal/clusterharness"
	"testing"
	"time"
)

func incidentRows(c *clusterharness.Client) []any {
	return c.Do("GET", "/api/incidents", nil, 200)["incidents"].([]any)
}
func awaitIncident(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, kind string) map[string]any {
	t.Helper()
	for i := 0; i < 30; i++ {
		for _, v := range incidentRows(c) {
			r := v.(map[string]any)
			if r["kind"] == kind {
				return r
			}
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("没有 %s 故障", kind)
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
