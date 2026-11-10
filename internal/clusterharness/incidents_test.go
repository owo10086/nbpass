package clusterharness_test

import (
	"nbpass/internal/clusterharness"
	"net"
	"strings"
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
	t.Fatalf("没有 %s 故障，列表=%v，概览=%v", kind, incidentRows(c), operations(t, c))
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
	awaitIncidentEntry(t, f.h, f.c, row["id"].(string), func(e map[string]any) bool { return e["stage"] == "服务商已写入" })
	if f.calls.Load() != 0 {
		t.Fatal("只读追踪发起换 IP")
	}
}

func TestIncidentsRotationAppearsInOriginalBlockedFlow(t *testing.T) {
	f := policyThree(t, true)
	f.block(t, 0)
	action := awaitStrategyRotation(t, f)
	row := awaitIncident(t, f.h, f.c, "blocked")
	found := false
	for i := 0; i < 12; i++ {
		entries := f.c.Do("GET", "/api/incidents/"+row["id"].(string)+"/timeline", nil, 200)["entries"].([]any)
		for _, v := range entries {
			if strings.Contains(v.(map[string]any)["source"].(string), action["id"].(string)) {
				found = true
			}
		}
		if found {
			break
		}
		f.h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("换 IP 没有关联原被墙事故")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("追踪改变换 IP 调用次数: %d", f.calls.Load())
	}
}

func awaitIncidentEntry(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, id string, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	for i := 0; i < 30; i++ {
		for _, v := range c.Do("GET", "/api/incidents/"+id+"/timeline", nil, 200)["entries"].([]any) {
			e := v.(map[string]any)
			if ready(e) {
				return e
			}
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("故障 %s 未出现期待的处理阶段，概览=%v", id, operations(t, c))
	return nil
}

func TestIncidentsRuleFailureEndsAsConfigurationChange(t *testing.T) {
	h, c := nodeBrowser(t)
	n := addNode(c, "监听故障入口")
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, n["id"].(string), connected)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	b := directBody("", occupied.Addr().(*net.TCPAddr).Port, "127.0.0.1", echoTarget(t, "I:"))
	delete(b, "entry_group_id")
	b["entry_node_id"] = n["id"]
	rule := c.Do("POST", "/api/rules", b, 201)["id"].(string)
	awaitRule(t, c, rule, "同步失败")
	incident := awaitIncident(t, h, c, "rule")
	if incident["impact"] != "abnormal" {
		t.Fatalf("监听失败没有明确影响: %v", incident)
	}
	b["enabled"] = false
	c.Do("PUT", "/api/rules/"+rule, b, 200)
	awaitIncidentEntry(t, h, c, incident["id"].(string), func(e map[string]any) bool { return e["stage"] == "配置变更结束" })
	ended := c.Do("GET", "/api/incidents/"+incident["id"].(string), nil, 200)
	if ended["closure"] != "配置变更结束" {
		t.Fatalf("停用伪装为恢复: %v", ended)
	}
	c.Do("DELETE", "/api/rules/"+rule, nil, 204)
	refs := c.Do("GET", "/api/incidents/"+incident["id"].(string), nil, 200)["refs"].([]any)
	for _, value := range refs {
		ref := value.(map[string]any)
		if ref["kind"] == "rule" && ref["exists"] != false {
			t.Fatal("已删除规则未保留历史快照")
		}
	}
}
