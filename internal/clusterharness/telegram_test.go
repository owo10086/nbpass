package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

const telegramToken = "123456789:telegram-secret-marker-abcdefghijklmnop"
const telegramChat = "-1001234567890"

// 设置必须经过认证、同源和严格校验；任何响应均不得回显 token 或聊天目标。
func TestTelegramSettingsAreWriteOnlyAndPersist(t *testing.T) {
	h, c := nodeBrowser(t)
	h.Client().Do("GET", "/api/settings/telegram", nil, 401)
	initial := c.Do("GET", "/api/settings/telegram", nil, 200)
	if initial["enabled"] != false || initial["configured"] != false || initial["reachability"] != "unknown" {
		t.Fatalf("初始状态: %v", initial)
	}
	response := c.Request("PUT", "/api/settings/telegram", map[string]any{"enabled": false}, map[string]string{"Origin": "https://evil.example"})
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatalf("跨源设置: %d", response.StatusCode)
	}
	for _, body := range []map[string]any{
		{}, {"enabled": nil}, {"enabled": true}, {"enabled": true, "bot_token": "bad", "chat_id": telegramChat},
		{"enabled": true, "bot_token": telegramToken, "chat_id": "bad/chat"}, {"enabled": false, "unknown": true},
	} {
		c.Do("PUT", "/api/settings/telegram", body, 400)
	}
	result := c.Do("PUT", "/api/settings/telegram", map[string]any{"enabled": true, "bot_token": telegramToken, "chat_id": telegramChat}, 200)
	assertTelegramRedacted(t, result)
	if result["configured"] != true || result["enabled"] != true {
		t.Fatalf("配置未保存: %v", result)
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	result = c.Do("GET", "/api/settings/telegram", nil, 200)
	assertTelegramRedacted(t, result)
	if result["configured"] != true || result["enabled"] != true {
		t.Fatal("重启丢失设置")
	}
	result = c.Do("PUT", "/api/settings/telegram", map[string]any{"enabled": false}, 200)
	if result["configured"] != true || result["enabled"] != false {
		t.Fatal("停用不应删除凭据")
	}
	c.Do("POST", "/api/settings/telegram/test", nil, 409)
}

func assertTelegramRedacted(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), telegramToken) || strings.Contains(string(data), telegramChat) {
		t.Fatalf("响应泄露 Telegram 凭据: %s", data)
	}
}

type fakeTelegram struct {
	mu                  sync.Mutex
	attempts, delivered []panel.TelegramMessage
	fail                bool
	block               <-chan struct{}
}

func (f *fakeTelegram) send(ctx context.Context, message panel.TelegramMessage) error {
	f.mu.Lock()
	f.attempts = append(f.attempts, message)
	fail, block := f.fail, f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-block:
		}
	}
	if fail {
		return errors.New("模拟第三方错误，含秘密 " + message.BotToken + " " + message.ChatID)
	}
	f.mu.Lock()
	f.delivered = append(f.delivered, message)
	f.mu.Unlock()
	return nil
}

func (f *fakeTelegram) snapshot() ([]panel.TelegramMessage, []panel.TelegramMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]panel.TelegramMessage(nil), f.attempts...), append([]panel.TelegramMessage(nil), f.delivered...)
}

func telegramBrowser(t *testing.T, fake *fakeTelegram, options ...func(*panel.Dependencies)) (*clusterharness.Harness, *clusterharness.Client) {
	t.Helper()
	h := clusterharness.New(t, append([]func(*panel.Dependencies){func(deps *panel.Dependencies) { deps.Passwords = nodePasswords{}; deps.Telegram = fake.send }}, options...)...)
	c := h.Client()
	setup(c)
	login(c)
	c.Do("PUT", "/api/settings/telegram", map[string]any{"enabled": true, "bot_token": telegramToken, "chat_id": telegramChat}, 200)
	return h, c
}

// 通知队列独立故障不能使状态查询失败、丢掉离线事件或拒绝恢复活性。
func TestTelegramQueueStorageFailureDoesNotBlockOfflineOrRecovery(t *testing.T) {
	fake := &fakeTelegram{}
	persistence := &interruptedPersistence{}
	h, c := telegramBrowser(t, fake, func(deps *panel.Dependencies) { persistence.base = deps.Persistence; deps.Persistence = persistence })
	if _, err := persistence.db.Exec(`CREATE TRIGGER fail_notifications BEFORE INSERT ON telegram_deliveries BEGIN SELECT RAISE(ABORT,'injected queue failure'); END`); err != nil {
		t.Fatal(err)
	}
	a := addNode(c, "通知存储故障")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(210 * time.Second)
	if nodeRow(t, c, id)["status"] != "offline" {
		t.Fatal("通知存储故障阻止离线")
	}
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_offline", 1)
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_recovered", 1)
	if _, err := persistence.db.Exec("DROP TRIGGER fail_notifications"); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	_, delivered := fake.snapshot()
	if len(delivered) != 1 || !strings.Contains(delivered[0].Text, "恢复上线") {
		t.Fatalf("队列恢复后未合并续投: %v", delivered)
	}
}

// 正在发送旧版本时发生恢复，迟到成功不能消费恢复通知。
func TestTelegramInFlightResultDoesNotConsumeRecovery(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeTelegram{block: gate}
	h, c := telegramBrowser(t, fake)
	a := addNode(c, "在途恢复")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(210 * time.Second)
	waitTelegramStatus(t, c, func(map[string]any) bool { a, _ := fake.snapshot(); return len(a) == 1 })
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_recovered", 1)
	close(gate)
	waitTelegramStatus(t, c, func(s map[string]any) bool {
		_, sent := fake.snapshot()
		return len(sent) == 2 && s["pending"] == float64(0)
	})
	_, sent := fake.snapshot()
	if !strings.Contains(sent[1].Text, "恢复上线") {
		t.Fatal("在途成功吞掉恢复内容")
	}
}

// 事故持续超过旧通知的保留窗口后，恢复仍属于一条新的待发版本。
func TestTelegramRecoveryAfterLongIncidentHasFreshDeliveryWindow(t *testing.T) {
	fake := &fakeTelegram{}
	h, c := telegramBrowser(t, fake)
	a := addNode(c, "长期离线")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(210 * time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	h.Clock.Advance(25 * time.Hour)
	login(c)
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_recovered", 1)
	waitTelegramStatus(t, c, func(s map[string]any) bool {
		_, sent := fake.snapshot()
		return len(sent) == 2 && s["pending"] == float64(0)
	})
	_, sent := fake.snapshot()
	if !strings.Contains(sent[1].Text, "恢复上线") {
		t.Fatal("长期事故恢复通知丢失")
	}
}

// 已经离线后进入维护，不能在维护结束时复用旧事故的累计离线时长。
func TestTelegramMaintenanceRestartsObservationAndNotificationWindow(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "尚未入队", true: "已经失败待重试"}[pending], func(t *testing.T) {
			fake := &fakeTelegram{fail: pending}
			h, c := telegramBrowser(t, fake)
			a := addNode(c, "维护通知")
			id := a["id"].(string)
			n := h.StartNode(a["installation_ticket"].(string))
			awaitNode(t, c, id, connected)
			n.Disconnect()
			awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
			elapsed := 91 * time.Second
			if pending {
				elapsed = 210 * time.Second
			}
			h.Clock.Advance(elapsed)
			waitOfflineEvents(t, c, "?node_id="+id+"&type=node_offline", 1)
			if pending {
				waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "unreachable" })
			}
			c.Do("PUT", "/api/nodes/"+id+"/maintenance", map[string]bool{"enabled": true}, 204)
			fake.mu.Lock()
			fake.fail = false
			fake.mu.Unlock()
			h.Clock.Advance(10 * time.Minute)
			if row := nodeRow(t, c, id); row["status"] == "offline" {
				t.Fatal("维护到期提前判离线")
			}
			// 另一个测试通知是后台处理完该次唤醒的外部可观察屏障。
			c.Do("POST", "/api/settings/telegram/test", nil, 202)
			waitTelegramStatus(t, c, func(map[string]any) bool {
				_, sent := fake.snapshot()
				for _, m := range sent {
					if m.Text == "nbpass Telegram 测试通知" {
						return true
					}
				}
				return false
			})
			_, sent := fake.snapshot()
			if len(sent) != 1 {
				t.Fatalf("维护到期立即发送旧离线通知: %v", sent)
			}
			h.Clock.Advance(209 * time.Second)
			c.Do("POST", "/api/settings/telegram/test", nil, 202)
			waitTelegramStatus(t, c, func(map[string]any) bool { _, s := fake.snapshot(); return len(s) >= 2 })
			_, sent = fake.snapshot()
			if len(sent) != 2 {
				t.Fatalf("重新离线未满两分钟提前发送: %v", sent)
			}
			h.Clock.Advance(time.Second)
			waitTelegramStatus(t, c, func(map[string]any) bool { _, s := fake.snapshot(); return len(s) == 3 })
		})
	}
}

func TestTelegramInFlightOldSettingsCannotConfirmNewTarget(t *testing.T) {
	for _, oldFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "旧发送成功", true: "旧发送失败"}[oldFailure], func(t *testing.T) {
			oldGate, newGate := make(chan struct{}), make(chan struct{})
			fake := &fakeTelegram{block: oldGate, fail: oldFailure}
			_, c := telegramBrowser(t, fake)
			c.Do("POST", "/api/settings/telegram/test", nil, 202)
			waitTelegramStatus(t, c, func(map[string]any) bool { a, _ := fake.snapshot(); return len(a) == 1 })
			const replacement = "987654321:replacement-token-abcdefghijklmnop"
			c.Do("PUT", "/api/settings/telegram", map[string]any{"enabled": true, "bot_token": replacement}, 200)
			fake.mu.Lock()
			fake.block = newGate
			fake.fail = false
			fake.mu.Unlock()
			close(oldGate)
			state := waitTelegramStatus(t, c, func(map[string]any) bool { a, _ := fake.snapshot(); return len(a) == 2 })
			if state["reachability"] != "unknown" || len(state["failures"].([]any)) != 0 || state["pending"] != float64(1) {
				t.Fatalf("旧结果污染新目标: %v", state)
			}
			attempts, _ := fake.snapshot()
			if attempts[1].BotToken != replacement {
				t.Fatal("新目标仍使用旧 token")
			}
			close(newGate)
			waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
		})
	}
}

func TestTelegramShortLossAndRecoveryBeforeNotificationDelay(t *testing.T) {
	fake := &fakeTelegram{}
	h, c := telegramBrowser(t, fake)
	a := addNode(c, "短时失联")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(30 * time.Second)
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	if rows := eventRows(c, "?node_id="+id+"&type=node_offline"); len(rows) != 0 {
		t.Fatal("短时失联产生离线事故")
	}
	if attempts, _ := fake.snapshot(); len(attempts) != 0 {
		t.Fatal("短时重连发出事故通知")
	}
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(91 * time.Second)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_offline", 1)
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	_, sent := fake.snapshot()
	if len(sent) != 1 || !strings.Contains(sent[0].Text, "恢复上线") {
		t.Fatalf("延迟内恢复未合并: %v", sent)
	}
}

func TestTelegramRestartGracePausesPendingOfflineNotification(t *testing.T) {
	fake := &fakeTelegram{fail: true}
	h, c := telegramBrowser(t, fake)
	a := addNode(c, "重建暂停通知")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(210 * time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "unreachable" })
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	h.Clock.Advance(89 * time.Second)
	c.Do("POST", "/api/settings/telegram/test", nil, 202)
	waitTelegramStatus(t, c, func(map[string]any) bool { _, s := fake.snapshot(); return len(s) >= 1 })
	_, sent := fake.snapshot()
	if len(sent) != 1 || sent[0].Text != "nbpass Telegram 测试通知" {
		t.Fatal("启动宽限内发送旧离线告警")
	}
	h.Clock.Advance(time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool {
		_, sent := fake.snapshot()
		return len(sent) == 2 && s["pending"] == float64(0)
	})
	if rows := eventRows(c, "?node_id="+id+"&type=node_offline"); len(rows) != 1 {
		t.Fatal("重建重复记录离线")
	}
}

func waitTelegramStatus(t *testing.T, c *clusterharness.Client, accept func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := c.Do("GET", "/api/settings/telegram", nil, 200)
		assertTelegramRedacted(t, state)
		if accept(state) {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Telegram 未达到预期状态: %v", c.Do("GET", "/api/settings/telegram", nil, 200))
	return nil
}

// 没有浏览器轮询节点列表，后台也必须记录离线，并在离线满两分钟后通知。
func TestTelegramOfflineDelayAndRecovery(t *testing.T) {
	fake := &fakeTelegram{}
	h, c := telegramBrowser(t, fake)
	a := addNode(c, "通知节点")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(91 * time.Second)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_offline", 1)
	h.Clock.Advance(118 * time.Second)
	if attempts, _ := fake.snapshot(); len(attempts) != 0 {
		t.Fatal("离线不足两分钟提前通知")
	}
	h.Clock.Advance(time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	attempts, delivered := fake.snapshot()
	if len(attempts) != 1 || len(delivered) != 1 || !strings.Contains(delivered[0].Text, "通知节点") || !strings.Contains(delivered[0].Text, "离线") || delivered[0].BotToken != telegramToken || delivered[0].ChatID != telegramChat {
		t.Fatalf("离线通知内容或目标不正确: %v", delivered)
	}
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_recovered", 1)
	waitTelegramStatus(t, c, func(s map[string]any) bool {
		_, sent := fake.snapshot()
		return len(sent) == 2 && s["pending"] == float64(0)
	})
	_, delivered = fake.snapshot()
	if !strings.Contains(delivered[1].Text, "恢复上线") {
		t.Fatal("恢复没有通知")
	}
}

// 失败必须可查且按退避重试；恢复覆盖同一事故待发摘要，不能再发过时的离线通知。
func TestTelegramRetryMergesRecoveryIntoSameIncident(t *testing.T) {
	fake := &fakeTelegram{fail: true}
	h, c := telegramBrowser(t, fake)
	a := addNode(c, "失败恢复")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(210 * time.Second)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_offline", 1)
	state := waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "unreachable" })
	if len(state["failures"].([]any)) != 1 || state["pending"] != float64(1) {
		t.Fatalf("失败未持久化: %v", state)
	}
	n.Restore()
	n.Restart()
	awaitNode(t, c, id, connected)
	waitOfflineEvents(t, c, "?node_id="+id+"&type=node_recovered", 1)
	if err := n.RecordEvent("sync_failed", "通知失败不阻塞事件", 1); err != nil {
		t.Fatal(err)
	}
	n.Restart()
	waitOfflineEvents(t, c, "?node_id="+id+"&type=sync_failed", 1)
	h.Clock.Advance(4 * time.Second)
	if attempts, _ := fake.snapshot(); len(attempts) != 1 {
		t.Fatalf("重试未遵守退避: %d", len(attempts))
	}
	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	h.Clock.Advance(time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	attempts, delivered := fake.snapshot()
	if len(attempts) != 2 || len(delivered) != 1 || !strings.Contains(delivered[0].Text, "离线") || !strings.Contains(delivered[0].Text, "恢复上线") {
		t.Fatalf("同一事故未合并: %v", delivered)
	}
}

// 外部发送挂起时控制、事件与管理 API 仍继续；注入时钟触发发送期限。
func TestTelegramBlockedSendDoesNotBlockControlAndTimesOut(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeTelegram{block: gate}
	h, c := telegramBrowser(t, fake)
	c.Do("POST", "/api/settings/telegram/test", nil, 202)
	waitTelegramStatus(t, c, func(map[string]any) bool { a, _ := fake.snapshot(); return len(a) == 1 })
	a := addNode(c, "发送阻塞期间接入")
	id := a["id"].(string)
	n := h.StartNode(a["installation_ticket"].(string))
	awaitNode(t, c, id, connected)
	if err := n.RecordEvent("sync_failed", "继续处理", 3); err != nil {
		t.Fatal(err)
	}
	n.Restart()
	waitOfflineEvents(t, c, "?node_id="+id+"&type=sync_failed", 1)
	h.Clock.Advance(10 * time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool {
		return s["reachability"] == "unreachable" && len(s["failures"].([]any)) == 1
	})
	close(gate)
}

// 未发送队列与退避不能因面板重建丢失；停用期间暂停投递，过期须展示。
func TestTelegramRetrySurvivesRestartAndExpiresWhileDisabled(t *testing.T) {
	fake := &fakeTelegram{fail: true}
	h, c := telegramBrowser(t, fake)
	c.Do("POST", "/api/settings/telegram/test", nil, 202)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "unreachable" })
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	fake.mu.Lock()
	fake.fail = false
	fake.mu.Unlock()
	h.Clock.Advance(5 * time.Second)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "reachable" && s["pending"] == float64(0) })
	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	c.Do("POST", "/api/settings/telegram/test", nil, 202)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["reachability"] == "unreachable" })
	c.Do("PUT", "/api/settings/telegram", map[string]any{"enabled": false}, 200)
	h.Clock.Advance(24 * time.Hour)
	login(c)
	waitTelegramStatus(t, c, func(s map[string]any) bool { return s["expired"] == float64(1) && s["pending"] == float64(0) })
	attempts, delivered := fake.snapshot()
	if len(attempts) != 3 || len(delivered) != 1 {
		t.Fatalf("停用后仍有投递: %d %d", len(attempts), len(delivered))
	}
}
