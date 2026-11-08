package clusterharness_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

const changedPassword = "a-new-long-password-02"

func authAttempt(t *testing.T, c *clusterharness.Client, password, forwarded string, want int) *http.Response {
	t.Helper()
	r := c.Request("POST", "/api/auth/login", map[string]string{"password": password}, map[string]string{
		"X-Forwarded-For": forwarded, "X-Real-IP": "198.51.100.99", "Forwarded": "for=198.51.100.99",
	})
	defer r.Body.Close()
	if r.StatusCode != want {
		data, _ := io.ReadAll(r.Body)
		t.Fatalf("登录来源 %q: 期望 %d，得到 %d: %s", forwarded, want, r.StatusCode, data)
	}
	return r
}

func trustProxies(c *clusterharness.Client, values ...string) {
	if values == nil {
		values = []string{}
	}
	c.Do("PUT", "/api/settings/trusted-proxies", map[string]any{"trusted_proxies": values}, 200)
}

func TestLoginLockPersistsAndExpiresAtFifteenMinutes(t *testing.T) {
	h, admin := nodeBrowser(t)
	c := h.Client()
	for range 4 {
		authAttempt(t, c, "wrong", "", 401)
	}
	if r := authAttempt(t, c, "wrong", "", 429); r.Header.Get("Retry-After") != "900" {
		t.Fatalf("锁定期限: %v", r.Header)
	}
	// 正确密码不能提前解除锁定；锁定请求不能延长截止时间。
	authAttempt(t, c, password, "", 429)
	admin.Do("GET", "/api/overview", nil, 200)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	h.Clock.Advance(15*time.Minute - time.Second)
	if r := authAttempt(t, c, password, "", 429); r.Header.Get("Retry-After") != "1" {
		t.Fatalf("锁定剩余时间: %v", r.Header)
	}
	h.Clock.Advance(time.Second)
	authAttempt(t, c, password, "", 200)
	for range 4 {
		authAttempt(t, c, "wrong", "", 401)
	}
	authAttempt(t, c, password, "", 200)
	for range 4 {
		authAttempt(t, c, strings.Repeat("x", 73), "", 401)
	}
	authAttempt(t, c, "wrong", "", 429)
}

func TestUntrustedForwardingHeadersCannotEvadeLockOrLockVictim(t *testing.T) {
	h, admin := nodeBrowser(t)
	c := h.Client()
	for i := range 5 {
		status := 401
		if i == 4 {
			status = 429
		}
		authAttempt(t, c, "wrong", "198.51.100."+string(rune('1'+i)), status)
	}
	authAttempt(t, c, password, "203.0.113.8", 429)
	// 管理员显式信任代理后，先前伪造的受害者地址没有失败预算。
	trustProxies(admin, "127.0.0.1", "::1")
	authAttempt(t, c, password, "198.51.100.1", 200)
}

func TestTrustedProxyChainUsesNearestUntrustedHopAndSurvivesRestart(t *testing.T) {
	h, admin := nodeBrowser(t)
	trustProxies(admin, "127.0.0.1/32", "10.0.0.0/8", "2001:db8:abcd::/48")
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	admin.Do("GET", "/api/settings/trusted-proxies", nil, 200)
	c := h.Client()
	for i := range 5 {
		status := 401
		if i == 4 {
			status = 429
		}
		// 左侧来自攻击者；只能采用可信链右侧第一个不可信地址。
		authAttempt(t, c, "wrong", "198.51.100."+string(rune('1'+i))+", 203.0.113.9, 10.2.3.4", status)
	}
	authAttempt(t, c, password, "203.0.113.9, 10.2.3.4", 429)
	// 超长的伪造左前缀不能把已锁定的真实来源改算为代理本身。
	authAttempt(t, c, password, strings.Repeat("198.51.100.7, ", 33)+"203.0.113.9, 10.2.3.4", 429)
	authAttempt(t, c, password, "198.51.100.1, 10.2.3.4", 200)
	for range 4 {
		authAttempt(t, c, "wrong", "2001:db8::1, 2001:db8:abcd::2", 401)
	}
	authAttempt(t, c, "wrong", "2001:db8::1", 429)
	authAttempt(t, c, password, "2001:db8::2", 200)
	// IPv4 映射地址不得为同一来源另开预算。
	for range 4 {
		authAttempt(t, c, "wrong", "::ffff:192.0.2.5", 401)
	}
	authAttempt(t, c, "wrong", "192.0.2.5", 429)
}

func TestInvalidForwardedChainsFailClosed(t *testing.T) {
	h, admin := nodeBrowser(t)
	trustProxies(admin, "127.0.0.1")
	c := h.Client()
	for _, chain := range []string{"invalid", "198.51.100.1, unknown", "198.51.100.2,", "[2001:db8::1]:443", "fe80::1%eth0"} {
		status := 401
		if strings.Contains(chain, "%") {
			status = 429
		}
		authAttempt(t, c, "wrong", chain, status)
	}
	authAttempt(t, c, password, "", 429)
	authAttempt(t, c, password, "198.51.100.1", 200)
}

func TestTrustedProxySettingsRequireSessionAndSameOriginAndValidateAtomically(t *testing.T) {
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{} })
	c := h.Client()
	c.Do("GET", "/api/settings/trusted-proxies", nil, 428)
	setup(c)
	c.Do("GET", "/api/settings/trusted-proxies", nil, 401)
	login(c)
	trustProxies(c, "127.0.0.1")
	r := c.Request("PUT", "/api/settings/trusted-proxies", map[string]any{"trusted_proxies": []string{}}, map[string]string{"Origin": "https://evil.example"})
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatalf("跨源代理设置: %d", r.StatusCode)
	}
	for _, value := range []any{nil, []string{"127.0.0.1", "invalid"}, []string{"example.com"}, []string{"fe80::1%en0"}, []string{"10.0.0.0/33"}, "127.0.0.1"} {
		c.Do("PUT", "/api/settings/trusted-proxies", map[string]any{"trusted_proxies": value}, 400)
	}
	result := c.Do("GET", "/api/settings/trusted-proxies", nil, 200)
	values := result["trusted_proxies"].([]any)
	if len(values) != 1 || values[0] != "127.0.0.1/32" {
		t.Fatalf("错误设置破坏已有值: %v", result)
	}
	trustProxies(c)
	// 空列表用显式 [] 表达，JSON null 不得误清空配置。
}

func TestPasswordChangeRevokesAllSessionsAndPersists(t *testing.T) {
	h, c := nodeBrowser(t)
	other := h.Client()
	login(other)
	path := "/api/auth/password"
	body := map[string]string{"current_password": password, "new_password": changedPassword}
	h.Client().Do("POST", path, body, 401)
	r := c.Request("POST", path, body, map[string]string{"Origin": "https://evil.example"})
	r.Body.Close()
	if r.StatusCode != 403 {
		t.Fatalf("跨源改密: %d", r.StatusCode)
	}
	c.Do("POST", path, map[string]string{"current_password": "wrong", "new_password": changedPassword}, 401)
	c.Do("POST", path, map[string]string{"current_password": password, "new_password": "short"}, 400)
	c.Do("GET", "/api/overview", nil, 200)
	c.Do("POST", path, body, 204)
	c.Do("GET", "/api/overview", nil, 401)
	other.Do("GET", "/api/overview", nil, 401)
	authAttempt(t, c, password, "", 401)
	authAttempt(t, c, changedPassword, "", 200)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	authAttempt(t, other, changedPassword, "", 200)
}

type pausedLoginPasswords struct {
	nodePasswords
	pause   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (p *pausedLoginPasswords) Verify(hash, password string) bool {
	valid := p.nodePasswords.Verify(hash, password)
	if p.pause.CompareAndSwap(true, false) {
		close(p.entered)
		<-p.release
	}
	return valid
}

func TestPasswordChangeRejectsLoginVerifiedAgainstOldPassword(t *testing.T) {
	for _, operation := range []string{"change", "reset"} {
		t.Run(operation, func(t *testing.T) { testPasswordReplacementDuringLogin(t, operation) })
	}
}

func testPasswordReplacementDuringLogin(t *testing.T, operation string) {
	passwords := &pausedLoginPasswords{entered: make(chan struct{}), release: make(chan struct{})}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = passwords })
	t.Cleanup(func() {
		select {
		case <-passwords.release:
		default:
			close(passwords.release)
		}
	})
	c := h.Client()
	setup(c)
	login(c)
	passwords.pause.Store(true)
	result := make(chan int, 1)
	go func() {
		r := h.Client().Request("POST", "/api/auth/login", map[string]string{"password": password}, nil)
		r.Body.Close()
		result <- r.StatusCode
	}()
	select {
	case <-passwords.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("登录未进入校验")
	}
	if operation == "change" {
		c.Do("POST", "/api/auth/password", map[string]string{"current_password": password, "new_password": changedPassword}, 204)
	} else if err := panel.ResetAdministratorPassword(context.Background(), changedPassword, h.Dependencies); err != nil {
		t.Fatal(err)
	}
	close(passwords.release)
	select {
	case status := <-result:
		if status != 401 {
			t.Fatalf("旧密码的并发登录仍创建会话: %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("登录未完成")
	}
}

func TestLoginAlreadyBeingVerifiedCannotBypassNewLock(t *testing.T) {
	passwords := &pausedLoginPasswords{entered: make(chan struct{}), release: make(chan struct{})}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = passwords })
	t.Cleanup(func() {
		select {
		case <-passwords.release:
		default:
			close(passwords.release)
		}
	})
	c := h.Client()
	setup(c)
	for range 3 {
		authAttempt(t, c, "wrong", "", 401)
	}
	passwords.pause.Store(true)
	result := make(chan int, 1)
	go func() {
		r := h.Client().Request("POST", "/api/auth/login", map[string]string{"password": password}, nil)
		r.Body.Close()
		result <- r.StatusCode
	}()
	select {
	case <-passwords.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("登录未进入校验")
	}
	authAttempt(t, c, "wrong", "", 401)
	authAttempt(t, c, "wrong", "", 429)
	close(passwords.release)
	select {
	case status := <-result:
		if status != 429 {
			t.Fatalf("在途登录绕过锁定: %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("登录未完成")
	}
}

func TestRevokedSessionCannotCompletePasswordChange(t *testing.T) {
	passwords := &pausedLoginPasswords{entered: make(chan struct{}), release: make(chan struct{})}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = passwords })
	t.Cleanup(func() {
		select {
		case <-passwords.release:
		default:
			close(passwords.release)
		}
	})
	c, other := h.Client(), h.Client()
	setup(c)
	login(c)
	login(other)
	passwords.pause.Store(true)
	result := make(chan int, 1)
	go func() {
		r := c.Request("POST", "/api/auth/password", map[string]string{"current_password": password, "new_password": changedPassword}, nil)
		r.Body.Close()
		result <- r.StatusCode
	}()
	select {
	case <-passwords.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("改密未进入校验")
	}
	other.Do("POST", "/api/auth/sessions/revoke", nil, 204)
	close(passwords.release)
	select {
	case status := <-result:
		if status != 401 {
			t.Fatalf("已撤销会话仍可改密: %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("改密未完成")
	}
	authAttempt(t, other, changedPassword, "", 401)
	login(other)
}

func TestPasswordReplacementRollsBackWhenSessionRevocationFails(t *testing.T) {
	persistence := &interruptedPersistence{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		persistence.base = d.Persistence
		d.Persistence = persistence
		d.Passwords = nodePasswords{}
	})
	c := h.Client()
	setup(c)
	login(c)
	// 在外部持久化接缝注入删除失败，不读取面板内部状态。
	_, err := persistence.db.Exec(`CREATE TRIGGER fail_revoke BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	c.Do("POST", "/api/auth/password", map[string]string{"current_password": password, "new_password": changedPassword}, 503)
	deps := h.Dependencies
	deps.Persistence = persistence.base
	if err := panel.ResetAdministratorPassword(context.Background(), changedPassword, deps); err == nil {
		t.Fatal("本机重置未报告撤销失败")
	}
	c.Do("GET", "/api/overview", nil, 200)
	authAttempt(t, h.Client(), changedPassword, "", 401)
	login(h.Client())
	if _, err := persistence.db.Exec("DROP TRIGGER fail_revoke"); err != nil {
		t.Fatal(err)
	}
	if err := panel.ResetAdministratorPassword(context.Background(), changedPassword, deps); err != nil {
		t.Fatal(err)
	}
	c.Do("GET", "/api/overview", nil, 401)
	authAttempt(t, c, changedPassword, "", 200)
}

func TestLocalPasswordRecoveryRevokesSessionsAndClearsLock(t *testing.T) {
	h, c := nodeBrowser(t)
	for range 4 {
		authAttempt(t, h.Client(), "wrong", "", 401)
	}
	authAttempt(t, h.Client(), "wrong", "", 429)
	// 模拟 CLI 访问同一个数据库；结果只能经面板 HTTP API 断言。
	if err := panel.ResetAdministratorPassword(context.Background(), changedPassword, h.Dependencies); err != nil {
		t.Fatal(err)
	}
	c.Do("GET", "/api/overview", nil, 401)
	authAttempt(t, c, changedPassword, "", 200)
	authAttempt(t, c, password, "", 401)
}

func TestRevokedNodeCredentialReplayCannotReconnect(t *testing.T) {
	for _, operation := range []string{"reset", "revoke"} {
		t.Run(operation, func(t *testing.T) {
			h, c := nodeBrowser(t)
			created := addNode(c, "凭据撤销")
			id := created["id"].(string)
			n := h.StartNode(created["installation_ticket"].(string))
			awaitNode(t, c, id, connected)
			status := 204
			if operation == "reset" {
				status = 200
			}
			c.Do("POST", "/api/nodes/"+id+"/token/"+operation, nil, status)
			if connected(nodeRow(t, c, id)) {
				t.Fatal("凭据撤销后仍连接")
			}
			last := nodeRow(t, c, id)["last_seen_at"]
			replay := h.CloneNode(n, "")
			// 等真实节点的重放尝试结束，只作环境同步；业务结果仍只读取 HTTP。
			select {
			case <-replay.Done:
			case <-time.After(5 * time.Second):
				t.Fatal("凭据重放尝试未结束")
			}
			row := nodeRow(t, c, id)
			if connected(row) || row["last_seen_at"] != last {
				t.Fatalf("旧凭据重连: %v", row)
			}
		})
	}
}
