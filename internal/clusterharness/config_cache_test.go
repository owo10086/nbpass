package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/node"
)

type faultyConfigStore struct {
	node.ConfigStore
	fail             atomic.Bool
	failFromRevision atomic.Uint64
}

func (s *faultyConfigStore) SaveConfig(ctx context.Context, data []byte) error {
	if s.fail.Load() {
		return errors.New("测试台注入配置磁盘写失败")
	}
	if revision := s.failFromRevision.Load(); revision > 0 {
		var state struct {
			State struct {
				HighestRevision uint64 `json:"highest_revision"`
			} `json:"state"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
		if state.State.HighestRevision >= revision {
			return errors.New("测试台注入新配置检查点写失败")
		}
	}
	return s.ConfigStore.SaveConfig(ctx, data)
}

// 撤销检查点写失败时，节点不能确认停用，旧监听仍是实际生效状态。
func TestConfigCheckpointFailureDoesNotConfirmRevocation(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "撤销写盘失败")
	id := created["id"].(string)
	store := &faultyConfigStore{}
	real := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		store.ConfigStore = deps.Store.(node.ConfigStore)
		deps.ConfigStore = store
	})
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	port := h.ReserveTCPPort()
	body := directBody(gid, port, "127.0.0.1", echoTarget(t, "A:"))
	rule := c.Do("POST", "/api/rules", body, 201)
	previous := awaitRevision(t, c, id, 0)
	store.fail.Store(true)
	body["enabled"] = false
	c.Do("PUT", "/api/rules/"+rule["id"].(string), body, 200)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	row := awaitRule(t, c, rule["id"].(string), "同步失败")
	member := row["sync_members"].([]any)[0].(map[string]any)
	if !strings.Contains(member["apply_error"].(string), "缓存") || member["applied_revision"] != previous {
		t.Fatalf("撤销失败未保留真实规则版本与错误: %v", member)
	}
	tcpExchange(t, port, "A:")
	store.fail.Store(false)
	h.Clock.Advance(10 * time.Second)
	awaitRevision(t, c, id, previous)
	awaitRule(t, c, rule["id"].(string), "正常")
	real.Stop()
}

// 最终结果写失败仍必须保留已提交的撤销，重建后不得恢复旧监听。
func TestConfigFinalWriteFailureKeepsRevocationsAcrossRestart(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "最终写盘失败")
	id := created["id"].(string)
	store := &faultyConfigStore{}
	oldPort, newPort, keepPort := h.ReserveTCPPort(), h.ReserveTCPPort(), h.ReserveTCPPort()
	var trigger atomic.Bool
	real := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		store.ConfigStore = deps.Store.(node.ConfigStore)
		deps.ConfigStore = store
		deps.Forwarding.Listen = func(network, address string) (net.Listener, error) {
			listener, err := h.ListenTCP(network, address)
			if err == nil && address == fmt.Sprintf("0.0.0.0:%d", newPort) && trigger.Load() {
				store.fail.Store(true)
			}
			return listener, err
		}
	})
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	target := echoTarget(t, "A:")
	body := directBody(gid, oldPort, "127.0.0.1", target)
	rule := c.Do("POST", "/api/rules", body, 201)
	c.Do("POST", "/api/rules", directBody(gid, keepPort, "127.0.0.1", target), 201)
	previous := awaitRevision(t, c, id, 0)
	conn := tcpExchange(t, oldPort, "A:")
	trigger.Store(true)
	body["listen_port"] = newPort
	c.Do("PUT", "/api/rules/"+rule["id"].(string), body, 200)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	if nodeRow(t, c, id)["applied_revision"] != previous {
		t.Fatal("最终落盘失败仍推进版本")
	}
	assertRevoked(t, oldPort, conn)
	tcpExchange(t, newPort, "A:")
	real.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return !connected(row) })
	trigger.Store(false)
	store.fail.Store(false)
	real.Restart()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", keepPort), 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("重建后未恢复未变化规则")
		}
		time.Sleep(time.Millisecond)
	}
	tcpExchange(t, keepPort, "A:")
	for _, port := range []int{oldPort, newPort} {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			conn.Close()
			t.Fatalf("重建后恢复了已撤销或未落盘规则: %d", port)
		}
	}
}

// 重启时缓存监听也可能 bind 失败，不能携带上个进程的完整成功确认上线。
func TestCachedListenFailureCannotReusePreviousAppliedRevision(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "缓存监听冲突")
	id := created["id"].(string)
	real := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	port := h.ReserveTCPPort()
	rule := c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", echoTarget(t, "A:")), 201)
	awaitRevision(t, c, id, 0)
	instance := nodeRow(t, c, id)["process_instance"]
	real.Stop()
	occupied, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	real.Restart()
	awaitNode(t, c, id, func(row map[string]any) bool {
		return connected(row) && row["process_instance"] != instance && row["config_status"] == "同步失败"
	})
	if nodeRow(t, c, id)["applied_revision"] != float64(0) {
		t.Fatal("缓存 bind 失败仍复用了旧完整确认")
	}
	awaitRule(t, c, rule["id"].(string), "同步失败")
	occupied.Close()
	h.Clock.Advance(10 * time.Second)
	awaitRevision(t, c, id, 0)
	awaitRule(t, c, rule["id"].(string), "正常")
	tcpExchange(t, port, "A:")
}

func TestDeletedRuleKeepsGroupFailedUntilRevocationIsConfirmed(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "删除撤销失败")
	id := created["id"].(string)
	store := &faultyConfigStore{}
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		store.ConfigStore = deps.Store.(node.ConfigStore)
		deps.ConfigStore = store
	})
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	port := h.ReserveTCPPort()
	rule := c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", echoTarget(t, "A:")), 201)
	previous := awaitRevision(t, c, id, 0)
	conn := tcpExchange(t, port, "A:")
	store.fail.Store(true)
	c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	group := groupRow(t, c, gid)
	if group["sync_status"] != "同步失败" {
		t.Fatalf("删除尚未撤销却汇总正常: %v", group)
	}
	members := group["sync_members"].([]any)
	pending := members[0].(map[string]any)["rules"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["rule_id"] != rule["id"] || pending[0].(map[string]any)["listen_port"] != float64(port) {
		t.Fatalf("删除规则丢失成员撤销明细: %v", group)
	}
	tcpExchange(t, port, "A:")
	store.fail.Store(false)
	h.Clock.Advance(10 * time.Second)
	awaitRevision(t, c, id, previous)
	if groupRow(t, c, gid)["sync_status"] != "正常" {
		t.Fatal("空快照撤销确认后入口组未恢复正常")
	}
	assertRevoked(t, port, conn)
}

func TestMovedRuleKeepsPreviousGroupFailedUntilRevocationIsConfirmed(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "移组撤销失败")
	id := created["id"].(string)
	store := &faultyConfigStore{}
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		store.ConfigStore = deps.Store.(node.ConfigStore)
		deps.ConfigStore = store
	})
	awaitNode(t, c, id, connected)
	oldGroup, newGroup := entryGroup(c, id)["id"].(string), entryGroup(c, id)["id"].(string)
	oldPort, newPort := h.ReserveTCPPort(), h.ReserveTCPPort()
	body := directBody(oldGroup, oldPort, "127.0.0.1", echoTarget(t, "A:"))
	rule := c.Do("POST", "/api/rules", body, 201)
	previous := awaitRevision(t, c, id, 0)
	conn := tcpExchange(t, oldPort, "A:")
	store.fail.Store(true)
	body["entry_group_id"], body["listen_port"] = newGroup, newPort
	c.Do("PUT", "/api/rules/"+rule["id"].(string), body, 200)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	old := groupRow(t, c, oldGroup)
	if old["sync_status"] != "同步失败" || groupRow(t, c, newGroup)["sync_status"] != "同步失败" {
		t.Fatalf("移组撤销失败丢失旧归属: %v", old)
	}
	pending := old["sync_members"].([]any)[0].(map[string]any)["rules"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["listen_port"] != float64(oldPort) {
		t.Fatalf("移组覆盖了实际旧监听端口: %v", old)
	}
	tcpExchange(t, oldPort, "A:")
	store.fail.Store(false)
	h.Clock.Advance(10 * time.Second)
	awaitRevision(t, c, id, previous)
	if groupRow(t, c, oldGroup)["sync_status"] != "正常" || groupRow(t, c, newGroup)["sync_status"] != "正常" {
		t.Fatal("移组确认后未清理旧归属")
	}
	assertRevoked(t, oldPort, conn)
	tcpExchange(t, newPort, "A:")
}

func TestDeletedRuleRevocationDetailsSurviveNewProcess(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "删除撤销重启")
	id := created["id"].(string)
	store := &faultyConfigStore{}
	real := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		store.ConfigStore = deps.Store.(node.ConfigStore)
		deps.ConfigStore = store
	})
	awaitNode(t, c, id, connected)
	gid := entryGroup(c, id)["id"].(string)
	port := h.ReserveTCPPort()
	rule := c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", echoTarget(t, "A:")), 201)
	previous := awaitRevision(t, c, id, 0)
	instance := nodeRow(t, c, id)["process_instance"]
	store.failFromRevision.Store(uint64(previous) + 1)
	c.Do("DELETE", "/api/rules/"+rule["id"].(string), nil, 204)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["config_status"] == "同步失败" })
	real.Restart()
	awaitNode(t, c, id, func(row map[string]any) bool {
		return connected(row) && row["process_instance"] != instance && row["config_status"] == "同步失败"
	})
	group := groupRow(t, c, gid)
	if group["sync_status"] != "同步失败" {
		t.Fatalf("新进程丢失待撤销删除规则: %v", group)
	}
	pending := group["sync_members"].([]any)[0].(map[string]any)["rules"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["rule_id"] != rule["id"] {
		t.Fatalf("新进程丢失待撤销明细: %v", group)
	}
	tcpExchange(t, port, "A:")
	store.failFromRevision.Store(0)
	h.Clock.Advance(10 * time.Second)
	awaitRevision(t, c, id, 0)
	if groupRow(t, c, gid)["sync_status"] != "正常" {
		t.Fatal("重启后的撤销确认未清理历史明细")
	}
}
