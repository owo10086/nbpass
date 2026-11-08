package clusterharness_test

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

const password = "a-long-test-password-01"

func setup(c *clusterharness.Client) {
	c.Do("POST", "/api/auth/setup", map[string]string{"password": password}, 201)
}

func TestPanelServesFrontendWithoutLettingAPIFallBackToHTML(t *testing.T) {
	h := clusterharness.New(t, func(deps *panel.Dependencies) {
		deps.Assets = fstest.MapFS{
			"index.html":    {Data: []byte("<!doctype html><title>nbpass</title>")},
			"assets/app.js": {Data: []byte("console.log('nbpass')")},
		}
	})
	c := h.Client()
	for _, path := range []string{"/", "/login", "/assets/app.js"} {
		response := c.Request("GET", path, nil, nil)
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Contains(data, []byte("nbpass")) {
			t.Fatalf("前端 %s: %d %s %v", path, response.StatusCode, data, err)
		}
	}
	setup(c)
	login(c)
	response := c.Request("GET", "/api/does-not-exist", nil, nil)
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 404 || bytes.Contains(data, []byte("<!doctype")) {
		t.Fatalf("未知 API 不应返回前端: %d %s", response.StatusCode, data)
	}
	response = c.Request("GET", "/assets/missing.js", nil, nil)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("不存在的资源得到 %d", response.StatusCode)
	}
}

type heldPasswords struct {
	entered chan struct{}
	release chan struct{}
}

func (p *heldPasswords) Hash(value string) (string, error) {
	return (panel.BcryptPasswords{}).Hash(value)
}
func (p *heldPasswords) Verify(hash, value string) bool {
	p.entered <- struct{}{}
	<-p.release
	return (panel.BcryptPasswords{}).Verify(hash, value)
}

func TestPasswordVerificationConcurrencyRejectsExcessWork(t *testing.T) {
	passwords := &heldPasswords{entered: make(chan struct{}, 5), release: make(chan struct{})}
	h := clusterharness.New(t, func(deps *panel.Dependencies) { deps.Passwords = passwords })
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(passwords.release) }) })
	c := h.Client()
	setup(c)
	results := make(chan int, 4)
	for range 4 {
		client := h.Client()
		go func() {
			response := client.Request("POST", "/api/auth/login", map[string]string{"password": password}, nil)
			response.Body.Close()
			results <- response.StatusCode
		}()
	}
	for range 4 {
		select {
		case <-passwords.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("密码校验未开始")
		}
	}
	// HTTP 超时仅作死锁看门狗，业务期限只使用可快进时钟。
	c.HTTP.Timeout = time.Second
	c.Do("POST", "/api/auth/login", map[string]string{"password": password}, 429)
	release.Do(func() { close(passwords.release) })
	for range 4 {
		select {
		case status := <-results:
			if status != 200 {
				t.Errorf("已接纳的登录得到 %d", status)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("密码校验未结束")
		}
	}
}

func TestConcurrentFirstSetupHasExactlyOneWinner(t *testing.T) {
	h := clusterharness.New(t)
	results := make(chan int, 2)
	for range 2 {
		client := h.Client()
		go func() {
			response := client.Request("POST", "/api/auth/setup", map[string]string{"password": password}, nil)
			response.Body.Close()
			results <- response.StatusCode
		}()
	}
	counts := map[int]int{}
	for range 2 {
		counts[<-results]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("并发初始化结果: %v", counts)
	}
	login(h.Client())
}

func TestSecretStorageIsPrivateAndSurvivesPanelRestart(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	c.Do("PUT", "/api/secrets/test-credential", map[string]string{"value": "private-credential-value-01"}, 428)
	setup(c)
	c.Do("GET", "/api/secrets/test-credential", nil, 401)
	login(c)
	c.Do("GET", "/api/secrets/test-credential", nil, 404)
	for _, value := range []string{"private-credential-value-01", "private-credential-value-02"} {
		response := c.Request("PUT", "/api/secrets/test-credential", map[string]string{"value": value}, nil)
		data, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || bytes.Contains(data, []byte(value)) {
			t.Fatalf("秘密写入: %d %s %v", response.StatusCode, data, err)
		}
		metadata := c.Do("GET", "/api/secrets/test-credential", nil, 200)
		if metadata["configured"] != true || len(metadata) != 1 {
			t.Fatalf("秘密不应回显: %v", metadata)
		}
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c.Do("GET", "/api/overview", nil, 200)
	metadata := c.Do("GET", "/api/secrets/test-credential", nil, 200)
	if metadata["configured"] != true {
		t.Fatalf("重启后秘密丢失: %v", metadata)
	}
	other := h.Client()
	other.Do("POST", "/api/auth/setup", map[string]string{"password": "another-long-password"}, 409)
	login(other)
	other.Do("GET", "/api/overview", nil, 200)
}

func TestMalformedAndOversizedRequestsCannotChangePassword(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	for _, body := range []string{`null`, `{}`, `{"password":123}`, `{"password":"valid-long-password","extra":1}`, `{"password":"valid-long-password"} {}`, `{"password":"` + strings.Repeat("x", 9000) + `"}`} {
		req, err := http.NewRequest("POST", h.Server.URL+"/api/auth/setup", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", h.Server.URL)
		req.Header.Set("Content-Type", "application/json")
		response, err := c.HTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("非法请求得到 %d", response.StatusCode)
		}
	}
	setup(c)
	login(c)
}

func login(c *clusterharness.Client) {
	c.Do("POST", "/api/auth/login", map[string]string{"password": password}, 200)
}

func TestFirstSetupGatesAPIAndCannotBeRepeated(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	status := c.Do("GET", "/api/auth/status", nil, 200)
	if status["setup_required"] != true || status["authenticated"] != false {
		t.Fatalf("首次状态: %v", status)
	}
	c.Do("GET", "/api/overview", nil, 428)
	c.Do("POST", "/api/auth/login", map[string]string{"password": password}, 428)
	c.Do("POST", "/api/auth/logout", nil, 428)
	c.Do("POST", "/api/auth/setup", map[string]string{"password": "short"}, 400)
	setup(c)
	c.Do("POST", "/api/auth/setup", map[string]string{"password": "another-long-password"}, 409)
	c.Do("GET", "/api/overview", nil, 401)
	login(c)
	c.Do("GET", "/api/overview", nil, 200)
	status = c.Do("GET", "/api/auth/status", nil, 200)
	if status["setup_required"] != false || status["authenticated"] != true {
		t.Fatalf("登录状态: %v", status)
	}
}

func TestWrongPasswordLogoutAndSessionReplay(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	setup(c)
	c.Do("POST", "/api/auth/login", map[string]string{"password": "wrong-password"}, 401)
	login(c)
	response := c.Request("POST", "/api/auth/login", map[string]string{"password": password}, nil)
	defer response.Body.Close()
	if response.StatusCode != 200 || len(response.Cookies()) != 1 {
		t.Fatalf("登录 Cookie: %v", response)
	}
	cookie := response.Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("Cookie 属性不安全: %s", cookie)
	}
	c.Do("POST", "/api/auth/logout", nil, 204)
	c.Do("GET", "/api/overview", nil, 401)
	replay := h.Client().Request("GET", "/api/overview", nil, map[string]string{"Cookie": cookie.Name + "=" + cookie.Value})
	defer replay.Body.Close()
	if replay.StatusCode != 401 {
		t.Fatalf("退出后的 Cookie 仍可重放: %d", replay.StatusCode)
	}
}

func TestSessionIdleDeadlineUsesInjectedClock(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	setup(c)
	login(c)
	h.Clock.Advance(23 * time.Hour)
	c.Do("GET", "/api/overview", nil, 200)
	h.Clock.Advance(24 * time.Hour)
	c.Do("GET", "/api/overview", nil, 401)
}

func TestSessionAbsoluteDeadlineCannotBeExtendedByActivity(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	setup(c)
	login(c)
	for range 13 {
		h.Clock.Advance(12 * time.Hour)
		c.Do("GET", "/api/overview", nil, 200)
	}
	h.Clock.Advance(12 * time.Hour)
	c.Do("GET", "/api/overview", nil, 401)
}

func TestRevokeAllSessions(t *testing.T) {
	h := clusterharness.New(t)
	c, other := h.Client(), h.Client()
	setup(c)
	login(c)
	login(other)
	c.Do("POST", "/api/auth/sessions/revoke", nil, 204)
	c.Do("GET", "/api/overview", nil, 401)
	other.Do("GET", "/api/overview", nil, 401)
	login(c)
	c.Do("GET", "/api/overview", nil, 200)
}

func TestLoggingOutOneBrowserDoesNotLogOutAnother(t *testing.T) {
	h := clusterharness.New(t)
	first, second := h.Client(), h.Client()
	setup(first)
	login(first)
	login(second)
	first.Do("POST", "/api/auth/logout", nil, 204)
	first.Do("GET", "/api/overview", nil, 401)
	second.Do("GET", "/api/overview", nil, 200)
}

func TestCrossOriginWritesAreRejectedIncludingSetup(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	for _, origin := range []string{"https://evil.example", "null", ""} {
		response := c.Request("POST", "/api/auth/setup", map[string]string{"password": password}, map[string]string{"Origin": origin, "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "https"})
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("Origin %q: 得到 %d", origin, response.StatusCode)
		}
	}
	setup(c)
	login(c)
	for _, path := range []string{"/api/auth/logout", "/api/auth/sessions/revoke", "/api/auth/login"} {
		response := c.Request("POST", path, map[string]string{"password": password}, map[string]string{"Origin": "https://evil.example"})
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("跨源写入 %s: 得到 %d", path, response.StatusCode)
		}
	}
	c.Do("GET", "/api/overview", nil, 200)
}

func TestPublicOriginConfigurationMatchesBrowserSerialization(t *testing.T) {
	for _, origin := range []string{"https://Panel.Example.com", "https://panel.example.com:443", "https://Panel.Example.com:443/"} {
		t.Run(origin, func(t *testing.T) {
			h := clusterharness.New(t)
			h.PublicOrigin = origin
			if err := h.Restart(); err != nil {
				t.Fatal(err)
			}
			response := h.Client().Request("POST", "/api/auth/setup", map[string]string{"password": password}, map[string]string{"Origin": "https://panel.example.com"})
			defer response.Body.Close()
			if response.StatusCode != 201 {
				t.Fatalf("源 %s: 得到 %d", origin, response.StatusCode)
			}
		})
	}
}
