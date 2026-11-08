package clusterharness_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
)

func eventRows(c *clusterharness.Client, query string) []any {
	return c.Do("GET", "/api/events"+query, nil, 200)["events"].([]any)
}

type retryEventStore struct {
	node.FileStore
	fail atomic.Bool
}

func (s *retryEventStore) SaveEvents(ctx context.Context, data []byte) error {
	if s.fail.Load() {
		// 只拒绝 ACK 引起的删除，允许先持久化新在途批次再发送。
		type state struct{ Events, Sending, Frozen []json.RawMessage }
		var previous, next state
		old, err := s.FileStore.LoadEvents(ctx)
		if err == nil && json.Unmarshal(old, &previous) == nil && json.Unmarshal(data, &next) == nil {
			blocked := false
			for _, raw := range previous.Sending {
				var e struct{ Type string }
				if json.Unmarshal(raw, &e) == nil && e.Type == "sync_failed" {
					blocked = true
				}
			}
			if blocked && len(next.Events)+len(next.Sending)+len(next.Frozen) < len(previous.Events)+len(previous.Sending)+len(previous.Frozen) {
				return errors.New("模拟确认落盘失败")
			}
		}
	}
	return s.FileStore.SaveEvents(ctx, data)
}

func eventNode(t *testing.T, h *clusterharness.Harness, ticket string, limits node.EventLimits) (*clusterharness.Node, *retryEventStore) {
	t.Helper()
	store := &retryEventStore{FileStore: node.FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}}
	queue, err := node.OpenEventQueue(context.Background(), store, h.Clock, rand.Reader, limits)
	if err != nil {
		t.Fatal(err)
	}
	n := h.StartNode(ticket, func(_ *node.Config, deps *node.Dependencies) { deps.Store = store; deps.Events = queue })
	return n, store
}

// 面板已经入库而节点无法持久化确认时必须重传；面板重启也不能丢掉去重记录。
func TestEventReplayAfterAckFailureAndPanelRestart(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "重传节点")
	n, store := eventNode(t, h, a["installation_ticket"].(string), node.EventLimits{})
	awaitNode(t, c, a["id"].(string), connected)
	n.Disconnect()
	awaitNode(t, c, a["id"].(string), func(row map[string]any) bool { return row["connected"] == false })
	if err := n.RecordEvent("sync_failed", "端口被占用", 9); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	n.Restore()
	query := "?node_id=" + a["id"].(string) + "&type=sync_failed"
	rows := awaitEvents(t, h, c, query, 1)
	id := rows[0].(map[string]any)["event_id"]
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	for range 5 {
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	store.fail.Store(false)
	n.Restart()
	awaitNode(t, c, a["id"].(string), connected)
	rows = awaitEvents(t, h, c, query, 1)
	if rows[0].(map[string]any)["event_id"] != id {
		t.Fatal("重传更换了事件 ID")
	}
	if rows[0].(map[string]any)["config_revision"] != float64(9) {
		t.Fatal("关联配置版本丢失")
	}
}

// 队列必须保留最新事件，并让管理员看见因容量不足造成的缺口。
func TestEventOverflowProducesVisibleGap(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "有界队列")
	n, _ := eventNode(t, h, a["installation_ticket"].(string), node.EventLimits{Count: 3})
	awaitNode(t, c, a["id"].(string), connected)
	n.Disconnect()
	awaitNode(t, c, a["id"].(string), func(row map[string]any) bool { return row["connected"] == false })
	for i := 0; i < 5; i++ {
		if err := n.RecordEvent("sync_failed", fmt.Sprintf("失败 %d", i), 2); err != nil {
			t.Fatal(err)
		}
	}
	n.Restore()
	query := "?node_id=" + a["id"].(string)
	rows := awaitEvents(t, h, c, query+"&type=sync_failed", 3)
	want := map[string]bool{"失败 2": true, "失败 3": true, "失败 4": true}
	for _, row := range rows {
		if !want[row.(map[string]any)["message"].(string)] {
			t.Fatal("没有保留最新事件")
		}
	}
	gaps := awaitEvents(t, h, c, query+"&type=event_gap", 1)
	gap := gaps[0].(map[string]any)["gap"].(map[string]any)
	if gap["reason"] != "overflow" || gap["count"].(float64) < 2 {
		t.Fatalf("缺口未记录淘汰数量: %v", gap)
	}
}

// 超过 24 小时的内容不得补报，恰好 24 小时的内容仍在允许窗口内。
func TestEventBackfillWindowIncludesExactly24Hours(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "补报窗口")
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, a["id"].(string), connected)
	n.Disconnect()
	awaitNode(t, c, a["id"].(string), func(row map[string]any) bool { return row["connected"] == false })
	if err := n.RecordEvent("sync_failed", "过期事件", 1); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(time.Second)
	if err := n.RecordEvent("sync_failed", "窗口边界", 2); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(24 * time.Hour)
	login(c)
	n.Restore()
	// 重新启动使重连立即开始，避免为异步重试继续推进窗口时钟。
	n.Restart()
	query := "?node_id=" + a["id"].(string)
	awaitNode(t, c, a["id"].(string), connected)
	rows := eventRows(c, query+"&type=sync_failed")
	if len(rows) != 1 || rows[0].(map[string]any)["message"] != "窗口边界" {
		t.Fatalf("24 小时窗口错误: %v", rows)
	}
	gaps := eventRows(c, query+"&type=event_gap")
	if len(gaps) != 1 || gaps[0].(map[string]any)["gap"].(map[string]any)["reason"] != "expired" {
		t.Fatalf("过期缺口未显示: %v", gaps)
	}
}

func TestEventBatchesDrainAndCursorPagesDoNotOverlap(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "分批补报")
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, a["id"].(string), connected)
	n.Disconnect()
	awaitNode(t, c, a["id"].(string), func(row map[string]any) bool { return row["connected"] == false })
	for i := 0; i < 40; i++ {
		if err := n.RecordEvent("sync_failed", fmt.Sprintf("配置 %d", i), uint64(i)); err != nil {
			t.Fatal(err)
		}
	}
	n.Restore()
	query := "?node_id=" + a["id"].(string) + "&type=sync_failed"
	awaitEvents(t, h, c, query, 40)
	seen := map[string]bool{}
	before := ""
	for {
		page := c.Do("GET", "/api/events"+query+"&limit=7"+before, nil, 200)
		for _, value := range page["events"].([]any) {
			id := value.(map[string]any)["event_id"].(string)
			if seen[id] {
				t.Fatal("分页事件重叠")
			}
			seen[id] = true
		}
		if page["next_before"] == nil {
			break
		}
		before = fmt.Sprintf("&before=%.0f", page["next_before"].(float64))
	}
	if len(seen) != 40 {
		t.Fatalf("分页丢事件: %d", len(seen))
	}
}

func awaitEvents(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, query string, count int) []any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows := eventRows(c, query)
		if len(rows) == count {
			return rows
		}
		h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("事件数量未达到 %d: %v", count, eventRows(c, query))
	return nil
}

// 未挂载认证门禁、未记录真实上线事件、筛选忽略条件时失败。
func TestEventLogFiltersAndAuthentication(t *testing.T) {
	h, c := nodeBrowser(t)
	h.Client().Do("GET", "/api/events", nil, 401)
	a := addNode(c, "入口 A")
	b := addNode(c, "入口 B")
	h.StartNode(a["installation_ticket"].(string))
	query := "?node_id=" + a["id"].(string) + "&type=node_online"
	rows := awaitEvents(t, h, c, query, 1)
	at := rows[0].(map[string]any)["occurred_at"].(string)
	if len(eventRows(c, "?node_id="+b["id"].(string))) != 0 || len(eventRows(c, "?type=sync_failed")) != 0 {
		t.Fatal("节点或类型筛选失效")
	}
	if len(eventRows(c, query+"&from="+url.QueryEscape(at)+"&to="+url.QueryEscape(at))) != 1 {
		t.Fatal("时间边界未包含事件")
	}
	if len(eventRows(c, query+"&from="+url.QueryEscape(h.Clock.Now().Add(time.Hour).Format(time.RFC3339Nano)))) != 0 {
		t.Fatal("时间筛选失效")
	}
	for _, invalid := range []string{"?from=bad", "?limit=0", "?limit=1001", "?before=bad", "?from=2031-01-01T00:00:00Z&to=2030-01-01T00:00:00Z"} {
		c.Do("GET", "/api/events"+invalid, nil, 400)
	}
}

// 离线重启丢失节点事件，或重复上报后日志重复时失败。
func TestOfflineNodeRestartBackfillsPersistentEvents(t *testing.T) {
	h, c := nodeBrowser(t)
	a := addNode(c, "持久补报节点")
	n := h.StartNode(a["installation_ticket"].(string))
	awaitEvents(t, h, c, "?node_id="+a["id"].(string)+"&type=node_online", 1)
	query := "?node_id=" + a["id"].(string) + "&type=sync_failed"
	n.Disconnect()
	if err := n.RecordEvent("sync_failed", "监听失败", 7); err != nil {
		t.Fatal(err)
	}
	n.Restart()
	if err := n.RecordEvent("sync_failed", "配置校验失败", 8); err != nil {
		t.Fatal(err)
	}
	n.Restart()
	if len(eventRows(c, query)) != 0 {
		t.Fatal("失联期间事件提前出现在面板")
	}
	n.Restore()
	rows := awaitEvents(t, h, c, query, 2)
	seen := map[string]bool{}
	for _, value := range rows {
		id := value.(map[string]any)["event_id"].(string)
		if seen[id] {
			t.Fatal("事件重复")
		}
		seen[id] = true
	}
	for range 3 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	if len(eventRows(c, query)) != 2 {
		t.Fatal("重复投递导致重复事件")
	}
}
