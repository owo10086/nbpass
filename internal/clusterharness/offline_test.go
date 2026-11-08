package clusterharness_test

import (
	"testing"
	"time"

	"nbpass/internal/clusterharness"
)

// 恰好 90 秒仍在观察窗口；离线事件应只记录一次，恢复后能记录新的事故。
func TestOfflineBoundaryRecordsOneEventPerIncident(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "离线边界")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(90 * time.Second)
	if row := nodeRow(t, c, id); row["status"] == "offline" {
		t.Fatalf("恰好 90 秒提前判为离线: %v", row)
	}
	h.Clock.Advance(time.Nanosecond)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["status"] == "offline" })
	query := "?node_id=" + id + "&type=node_offline"
	waitOfflineEvents(t, c, query, 1)
	for range 3 {
		nodeRow(t, c, id)
	}
	if rows := eventRows(c, query); len(rows) != 1 {
		t.Fatalf("同一事故重复记离线: %v", rows)
	}
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_recovered", 1)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(91 * time.Second)
	nodeRow(t, c, id)
	waitOfflineEvents(t, c, query, 2)
}

// 面板重建后，即使旧活性已过期，宽限内也不能新增离线事件。
func TestOfflineRestartGraceDoesNotCreateOrDuplicateIncidents(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "重建宽限")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(89 * time.Second)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	h.Clock.Advance(89 * time.Second)
	if row := nodeRow(t, c, id); row["status"] == "offline" {
		t.Fatal("重建宽限内判离线")
	}
	query := "?node_id=" + id + "&type=node_offline"
	if rows := eventRows(c, query); len(rows) != 0 {
		t.Fatalf("宽限内产生离线事件: %v", rows)
	}
	h.Clock.Advance(time.Second)
	nodeRow(t, c, id)
	waitOfflineEvents(t, c, query, 1)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	h.Clock.Advance(91 * time.Second)
	nodeRow(t, c, id)
	if rows := eventRows(c, query); len(rows) != 1 {
		t.Fatalf("重建重复事故: %v", rows)
	}
}

func waitOfflineEvents(t *testing.T, c *clusterharness.Client, query string, count int) []any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows := eventRows(c, query)
		if len(rows) == count {
			return rows
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("事件数量未达到 %d: %v", count, eventRows(c, query))
	return nil
}
