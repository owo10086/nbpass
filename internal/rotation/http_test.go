package rotation

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTemplatesPreserveURLHeaderAndJSONStructure(t *testing.T) {
	c := Config{URL: "https://vendor.example/change/{{node_name}}?name={{node_name}}&old={{old_ip}}", Method: "POST", Headers: map[string]string{"X-Node": "{{node_name}}"}, Body: `{"name":"{{node_name}}","id":"{{action_id}}","n":2}`}
	r, err := c.Request(context.Background(), Variables{NodeName: "a/b?&\"\n中", OldIP: "8.8.8.8", ActionID: "action"})
	if err != nil {
		t.Fatal(err)
	}
	if r.URL.EscapedPath() != "/change/a%2Fb%3F&%22%0A%E4%B8%AD" || r.URL.Query().Get("name") != "a/b?&\"\n中" {
		t.Fatalf("URL结构损坏: %s", r.URL)
	}
	if strings.ContainsAny(r.Header.Get("X-Node"), "\r\n") {
		t.Fatal("请求头注入")
	}
	b, _ := io.ReadAll(r.Body)
	if string(b) != `{"id":"action","n":2,"name":"a/b?\u0026\"\n中"}` {
		t.Fatalf("JSON结构损坏: %s", b)
	}
	for _, u := range []string{"https://{{node_name}}/change", "https://user:pass@vendor.example/", "ftp://vendor.example/", "https://vendor.example/{{unknown}}"} {
		c.URL = u
		if c.Validate() == nil {
			t.Fatalf("接受非法模板: %s", u)
		}
	}
}

func TestHTTPEnforcesEndpointTLSRedirectAndResponseLimits(t *testing.T) {
	var redirect string
	var large bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect != "" {
			http.Redirect(w, r, redirect, 302)
			return
		}
		if large {
			_, _ = io.WriteString(w, strings.Repeat("x", (64<<10)+1))
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	c := Config{URL: server.URL + "/change", Method: "POST"}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	e := HTTPExecutor{RootCAs: roots}
	if e.Do(context.Background(), c, Variables{}) == nil {
		t.Fatal("默认访问内网")
	}
	c.PrivateEndpoints = []string{server.URL + "/change"}
	if err := e.Do(context.Background(), c, Variables{}); err != nil {
		t.Fatal(err)
	}
	if err := (HTTPExecutor{}).Do(context.Background(), c, Variables{}); err == nil {
		t.Fatal("未校验TLS证书")
	}
	for _, target := range []string{"https://elsewhere.example/change", strings.Replace(server.URL, "https:", "http:", 1) + "/change", server.URL + "/unauthorized"} {
		redirect = target
		if e.Do(context.Background(), c, Variables{}) == nil {
			t.Fatalf("接受重定向: %s", target)
		}
	}
	redirect = ""
	large = true
	if e.Do(context.Background(), c, Variables{}) == nil {
		t.Fatal("接受超大响应")
	}
}

func TestHTTPRejectsIPv6LocalAndTranslationTargets(t *testing.T) {
	for _, address := range []string{"fec0::1", "64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1", "2001:db8::1", "3fff::1"} {
		var dialed bool
		e := HTTPExecutor{Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(address)}, nil
		}, DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		}}
		_ = e.Do(context.Background(), Config{URL: "https://vendor.example/", Method: "POST"}, Variables{})
		if dialed {
			t.Fatalf("未授权的IPv6地址被拨号: %s", address)
		}
	}
}

func TestTemplateValuesContainingBracesAreLiteral(t *testing.T) {
	c := Config{URL: "https://vendor.example/{{node_name}}", Method: "POST", Body: `{"name":"{{node_name}}"}`}
	r, err := c.Request(context.Background(), Variables{NodeName: "{{literal}}"})
	if err != nil {
		t.Fatal(err)
	}
	if r.URL.Path != "/{{literal}}" {
		t.Fatalf("模板值被再次解释: %s", r.URL.Path)
	}
}

func TestHTTPRevalidatesDialAddressAgainstDNSRebinding(t *testing.T) {
	calls := 0
	dialed := false
	executor := HTTPExecutor{Resolve: func(context.Context, string) ([]netip.Addr, error) {
		calls++
		if calls == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}, DialContext: func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	}}
	if executor.Do(context.Background(), Config{URL: "https://vendor.example/change", Method: "POST"}, Variables{}) == nil || dialed {
		t.Fatal("DNS重绑定绕过实际拨号校验")
	}
}

func TestHTTPDoesNotInheritEnvironmentProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	executor := HTTPExecutor{RootCAs: roots, Resolve: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	// 仅测试拨号依赖把公网地址接到本机 TLS 服务，证书仍按原 URL 校验。
	config := Config{URL: "https://example.com/change", Method: "POST"}
	if err := executor.Do(context.Background(), config, Variables{}); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("继承了环境代理")
	}
}
