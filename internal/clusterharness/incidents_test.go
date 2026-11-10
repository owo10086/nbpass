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

func TestIncidentsBlockedLinksEntryDecisionAndAction(t *testing.T) {
	f := newBlockedEntryRig(t, true)
	f.block(t, 0)
	f.awaitPublication(t, f.ids[1], 2)
	row := awaitIncident(t, f.h, f.c, "blocked")
	timeline := f.c.Do("GET", "/api/incidents/"+row["id"].(string)+"/timeline", nil, 200)["entries"].([]any)
	found := false
	for _, v := range timeline {
		if v.(map[string]any)["stage"] == "服务商已写入" {
			found = true
		}
	}
	if !found {
		t.Fatalf("被墙故障未包含实际切换动作: %v", timeline)
	}
	if f.calls.Load() != 0 {
		t.Fatal("只读追踪发起换 IP")
	}
}
