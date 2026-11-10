package panel

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 本机开发可用 http 回环源登录并保持会话；非回环地址仍必须 HTTPS。
func TestLocalHTTPOriginAllowsLoopbackOnly(t *testing.T) {
	for _, origin := range []string{"http://panel.example.com", "http://192.168.1.2:8080", "ftp://127.0.0.1"} {
		if _, err := Open(context.Background(), Config{PublicOrigin: origin}, diskDependencies(t.TempDir())); err == nil {
			t.Fatalf("%s 不应被接受", origin)
		}
	}
	p, err := Open(context.Background(), Config{PublicOrigin: "http://127.0.0.1:8080"}, diskDependencies(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		p.Handler().ServeHTTP(response, req)
		return response
	}
	if code := request("POST", "/api/auth/setup", `{"password":"local-development-password"}`, nil).Code; code != 201 {
		t.Fatalf("初始化: %d", code)
	}
	cookies := request("POST", "/api/auth/login", `{"password":"local-development-password"}`, nil).Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "nbpass_session" || cookies[0].Secure {
		t.Fatalf("本机 http 会话 Cookie: %+v", cookies)
	}
	if code := request("GET", "/api/dns-watches", "", cookies[0]).Code; code != 200 {
		t.Fatalf("会话未保持: %d", code)
	}
}
