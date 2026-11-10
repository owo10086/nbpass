package clusterharness_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/compatibility"
	"nbpass/internal/node"
)

// 所有业务断言通过 HTTP；节点的启动/网络只属于测试台的环境驱动。
func TestRealNodeEnrollsAndReportsLivenessEveryTenSeconds(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "入口 A")
	id := created["id"].(string)
	h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == true })
	row := nodeRow(t, c, id)
	if row["status"] != "online" || row["config_status"] != "正常" || row["last_seen_at"] == nil || row["software_version"] == "" || row["process_instance"] == "" {
		t.Fatalf("接入状态: %v", row)
	}
	before := row["last_seen_at"]
	h.Clock.Advance(9 * time.Second)
	if nodeRow(t, c, id)["last_seen_at"] != before {
		t.Fatal("活性提前上报")
	}
	h.Clock.Advance(time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] != before })
	if nodeRow(t, c, id)["desired_revision"] != float64(0) || nodeRow(t, c, id)["applied_revision"] != float64(0) {
		t.Fatal("无配置时不应伪造配置应用")
	}
}

func TestInstallationTicketIsConsumedOnceEvenConcurrently(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "出口 A")
	id := created["id"].(string)
	first := h.StartNode(created["installation_ticket"].(string))
	second := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == true })
	// 被拒的真实节点应结束；成功者持续在线，不能被第二次兑换轮换 token。
	select {
	case err := <-first.Done:
		if err == nil {
			t.Fatal("失败兑换未报错")
		}
	case err := <-second.Done:
		if err == nil {
			t.Fatal("失败兑换未报错")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("并发兑换未拒绝重复票据")
	}
	before := nodeRow(t, c, id)["last_seen_at"]
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] != before })
	h.StartNode(created["installation_ticket"].(string))
	if nodeRow(t, c, id)["connected"] != true {
		t.Fatal("重复兑换影响已接入节点")
	}
}

func TestExpiredTicketNeverBringsNodeOnline(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "过期节点")
	h.Clock.Advance(30 * time.Minute)
	n := h.StartNode(created["installation_ticket"].(string))
	select {
	case err := <-n.Done:
		if err == nil {
			t.Fatal("过期票据未报错")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("兑换未结束")
	}
	row := nodeRow(t, c, created["id"].(string))
	if row["connected"] != false || row["last_seen_at"] != nil {
		t.Fatalf("过期票据接入: %v", row)
	}
}

func TestNodeNetworkIsolationOfflineThresholdAndRecovery(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "入口 A"), addNode(c, "出口 B")
	n := h.StartNode(a["installation_ticket"].(string))
	h.StartNode(b["installation_ticket"].(string))
	id, otherID := a["id"].(string), b["id"].(string)
	awaitNode(t, c, id, connected)
	awaitNode(t, c, otherID, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(89 * time.Second)
	if nodeRow(t, c, id)["status"] == "offline" {
		t.Fatal("不足 90 秒过早离线")
	}
	h.Clock.Advance(time.Second + time.Nanosecond)
	row := nodeRow(t, c, id)
	if row["status"] != "offline" || row["config_status"] != "待确认" {
		t.Fatalf("90 秒后: %v", row)
	}
	// B 仍能上报，模拟网络只阻断 A。
	h.Clock.Advance(10 * time.Second)
	// 一次跳过多个定时期限后，真实传输可能先处理活性超时，再建立重试定时器。
	// 与下方 A 的恢复相同，继续驱动注入时钟，让未被阻断的 B 完成重连。
	deadline := time.Now().Add(5 * time.Second)
	for !connected(nodeRow(t, c, otherID)) {
		if time.Now().After(deadline) {
			t.Fatal("未被阻断的节点未恢复控制连接")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	n.Restore()
	// 断网错误与重连定时器的建立经过真实 gRPC 异步传输，推进重试周期直至恢复。
	deadline = time.Now().Add(5 * time.Second)
	for !connected(nodeRow(t, c, id)) {
		if time.Now().After(deadline) {
			t.Fatal("恢复网络后节点未重连")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResetAndRevokeTokenImmediatelyDisconnectEstablishedStream(t *testing.T) {
	for _, operation := range []string{"reset", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			h, c := nodeBrowser(t)
			created := addNode(c, "入口 A")
			id := created["id"].(string)
			h.StartNode(created["installation_ticket"].(string))
			awaitNode(t, c, id, connected)
			status := 204
			if operation == "reset" {
				status = 200
			}
			reset := c.Do("POST", "/api/nodes/"+id+"/token/"+operation, nil, status)
			row := nodeRow(t, c, id)
			if row["connected"] != false || row["config_status"] != "待确认" {
				t.Fatalf("撤销未立即断开: %v", row)
			}
			last := row["last_seen_at"]
			h.Clock.Advance(10 * time.Second)
			if nodeRow(t, c, id)["last_seen_at"] != last {
				t.Fatal("旧凭据仍能上报")
			}
			if operation == "reset" {
				h.StartNode(reset["installation_ticket"].(string))
				awaitNode(t, c, id, connected)
			}
		})
	}
}

func TestPanelRestartGetsNinetySecondOfflineGrace(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "入口 A")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return !connected(row) })
	h.Clock.Advance(2 * time.Minute)
	if nodeRow(t, c, id)["status"] != "offline" {
		t.Fatal("重启前未离线")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(89 * time.Second)
	if nodeRow(t, c, id)["status"] == "offline" {
		t.Fatal("启动宽限期内误判")
	}
	h.Clock.Advance(time.Second)
	if nodeRow(t, c, id)["status"] != "offline" {
		t.Fatal("启动宽限期满未重评")
	}
}

func TestMaintenanceExpiresAndStartsFreshOfflineObservation(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "维护节点")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	c.Do("PUT", "/api/nodes/"+id+"/maintenance", map[string]bool{"enabled": true}, 204)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return !connected(row) })
	h.Clock.Advance(9 * time.Minute)
	if nodeRow(t, c, id)["status"] != "maintenance" {
		t.Fatal("维护期限内误判")
	}
	h.Clock.Advance(time.Minute)
	if nodeRow(t, c, id)["status"] == "offline" {
		t.Fatal("维护期满复用了累计失联")
	}
	h.Clock.Advance(90 * time.Second)
	if nodeRow(t, c, id)["status"] == "offline" {
		t.Fatal("维护期满的完整观察窗口内提前判离线")
	}
	h.Clock.Advance(time.Nanosecond)
	if nodeRow(t, c, id)["status"] != "offline" {
		t.Fatal("维护期满未重新评估")
	}
}

func TestSilentControlLinkIsOfflineAfterNinetySeconds(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "静默节点")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Blackhole()
	h.Clock.Advance(90*time.Second + time.Nanosecond)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["status"] == "offline" })
	if nodeRow(t, c, id)["config_status"] != "待确认" {
		t.Fatal("离线配置仍标正常")
	}
}

func TestWrongCertificateAndPlaintextNeverConsumeTicket(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "安全节点")
	id := created["id"].(string)
	for _, plaintext := range []bool{false, true} {
		n := h.StartNode(created["installation_ticket"].(string), func(config *node.Config, deps *node.Dependencies) {
			if plaintext {
				config.ControlURL = "http://" + config.ControlURL[len("https://"):]
			} else {
				deps.RootCAs = x509.NewCertPool()
			}
		})
		select {
		case err := <-n.Done:
			if err == nil {
				t.Fatal("不安全连接未报错")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("不安全连接未拒绝")
		}
		if nodeRow(t, c, id)["last_seen_at"] != nil {
			t.Fatal("不安全节点接入了面板")
		}
	}
	h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
}

func TestAuthenticatedIdentityOverridesClaimedNodeAndOldSession(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "入口 A"), addNode(c, "出口 B")
	id, otherID := a["id"].(string), b["id"].(string)
	first := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	oldInstance := nodeRow(t, c, id)["process_instance"]
	h.CloneNode(first, otherID)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["process_instance"] != oldInstance })
	newInstance := nodeRow(t, c, id)["process_instance"]
	first.Stop()
	// 旧流结束不能清除替代它的新会话。
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return connected(row) && row["process_instance"] == newInstance })
	if row := nodeRow(t, c, otherID); row["last_seen_at"] != nil || connected(row) {
		t.Fatalf("冒充另一节点成功: %v", row)
	}
}

func TestNodeRestartUsesPersistedTokenWithoutReusingTicket(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "重启节点")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	instance := nodeRow(t, c, id)["process_instance"]
	n.Restart()
	awaitNode(t, c, id, func(row map[string]any) bool { return connected(row) && row["process_instance"] != instance })
}

func TestReplacedProcessCannotReconnectAndOverwriteNewInstance(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "实例隔离")
	id := created["id"].(string)
	first := h.StartNode(created["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	old := nodeRow(t, c, id)["process_instance"].(string)
	h.CloneNode(first, "")
	awaitNode(t, c, id, func(row map[string]any) bool { return row["process_instance"] != old })
	current := nodeRow(t, c, id)["process_instance"]
	first.Stop()
	oldBytes, err := base64.RawURLEncoding.DecodeString(old)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := first.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	replay := h.StartNode("", func(_ *node.Config, deps *node.Dependencies) {
		deps.Store = credential
		deps.Random = bytes.NewReader(oldBytes)
	})
	select {
	case <-replay.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("旧实例重连没有被拒绝")
	}
	row := nodeRow(t, c, id)
	if !connected(row) || row["process_instance"] != current {
		t.Fatalf("旧实例覆盖了新状态: %v", row)
	}
}

func TestEnrollmentOnlyPersistsCredentialsAndCanReplaceResetToken(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "安装服务")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string), func(config *node.Config, _ *node.Dependencies) { config.EnrollOnly = true })
	awaitEnrollment(t, n, false)
	if nodeRow(t, c, id)["last_seen_at"] != nil {
		t.Fatal("安装阶段提前启动控制服务")
	}
	h.CloneNode(n, "")
	awaitNode(t, c, id, connected)
	reset := c.Do("POST", "/api/nodes/"+id+"/token/reset", nil, 200)
	oldStore, err := n.Credentials()
	if err != nil {
		t.Fatal(err)
	}
	reinstalled := h.StartNode(reset["installation_ticket"].(string), func(config *node.Config, deps *node.Dependencies) { config.EnrollOnly = true; deps.Store = oldStore })
	awaitEnrollment(t, reinstalled, false)
	h.CloneNode(reinstalled, "")
	awaitNode(t, c, id, connected)
}

// 同一台机器换成另一个节点身份重装：旧身份的配置缓存不能卡住新节点，旧事件也不能记到新节点名下。
// 已按旧版本装成“新凭据 + 旧缓存”的机器，重置 token 重跑安装后也必须恢复。
func TestReinstallAsAnotherNodeDiscardsPreviousIdentityState(t *testing.T) {
	h, c := nodeBrowser(t)
	ctx := context.Background()
	store := node.FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	useStore := func(_ *node.Config, deps *node.Dependencies) { deps.Store = store }
	enroll := func(ticket string) {
		t.Helper()
		awaitEnrollment(t, h.StartNode(ticket, useStore, func(config *node.Config, _ *node.Dependencies) { config.EnrollOnly = true }), false)
	}
	leaveOldIdentityState := func(id string) {
		t.Helper()
		cache, _ := json.Marshal(map[string]any{"version": compatibility.StateCurrent, "node_id": id, "control_url": h.Control.URL, "state": map[string]any{}})
		if err := store.SaveConfig(ctx, cache); err != nil {
			t.Fatal(err)
		}
		queue, err := node.OpenEventQueue(ctx, store, h.Clock, rand.Reader, node.EventLimits{})
		if err == nil {
			err = queue.Record(ctx, "sync_failed", "旧身份遗留事件", 0)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	first := addNode(c, "旧身份")
	oldID := first["id"].(string)
	enroll(first["installation_ticket"].(string))
	old := h.StartNode("", useStore)
	awaitNode(t, c, oldID, connected)
	old.Stop()
	leaveOldIdentityState(oldID)

	second := addNode(c, "新身份")
	newID := second["id"].(string)
	enroll(second["installation_ticket"].(string))
	running := h.StartNode("", useStore)
	awaitNode(t, c, newID, connected)
	deadline := time.Now().Add(5 * time.Second)
	for {
		events := fmt.Sprint(c.Do("GET", "/api/events?node_id="+newID, nil, 200)["events"])
		if strings.Contains(events, "旧身份遗留事件") {
			t.Fatalf("旧身份事件记到新节点: %s", events)
		}
		if strings.Contains(events, "节点进程启动") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("新节点启动事件未上报: %s", events)
		}
		time.Sleep(time.Millisecond)
	}
	running.Stop()

	leaveOldIdentityState(oldID)
	reset := c.Do("POST", "/api/nodes/"+newID+"/token/reset", nil, 200)
	enroll(reset["installation_ticket"].(string))
	h.StartNode("", useStore)
	awaitNode(t, c, newID, connected)
}

type failedNodeStore struct{}

func (failedNodeStore) Load(context.Context) (node.Credentials, error) {
	return node.Credentials{}, os.ErrNotExist
}
func (failedNodeStore) Save(context.Context, node.Credentials) error {
	return errors.New("测试台注入节点磁盘写失败")
}

func TestNodeCannotConnectWhenCredentialPersistenceFails(t *testing.T) {
	h, c := nodeBrowser(t)
	created := addNode(c, "磁盘故障节点")
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) { deps.Store = failedNodeStore{} })
	awaitEnrollment(t, n, true)
	row := nodeRow(t, c, created["id"].(string))
	if row["last_seen_at"] != nil || connected(row) {
		t.Fatalf("未落盘即接入: %v", row)
	}
}

func awaitEnrollment(t *testing.T, n *clusterharness.Node, wantFailure bool) {
	t.Helper()
	select {
	case err := <-n.Done:
		if (err != nil) != wantFailure {
			t.Fatalf("安装结果: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("安装未结束")
	}
}

func connected(row map[string]any) bool { return row["connected"] == true }

func awaitNode(t *testing.T, c *clusterharness.Client, id string, predicate func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := nodeRow(t, c, id)
		if predicate(row) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待节点状态超时: %v", row)
		}
		// 仅等待真实传输完成；业务时间全部通过 Clock.Advance 驱动。
		time.Sleep(time.Millisecond)
	}
}
