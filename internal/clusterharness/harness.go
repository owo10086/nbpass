// Package clusterharness 在测试进程内运行真实核心，外部依赖可按票逐步扩展。
package clusterharness

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"nbpass/internal/controlpb"
	"nbpass/internal/dnswatch"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

type Harness struct {
	t                      testing.TB
	Dir                    string
	Clock                  *Clock
	Dependencies           panel.Dependencies
	Panel                  *panel.Panel
	Server                 *httptest.Server
	PublicOrigin           string
	Control                *httptest.Server
	controlServer          atomic.Pointer[grpc.Server]
	repeatRotationCommands atomic.Bool
	dropUpgradeCommands    atomic.Bool
	controlReportTransform atomic.Pointer[func(*controlpb.NodeReport)]
	ports                  portReservations
}

type Client struct {
	h    *Harness
	HTTP *http.Client
}

func New(t testing.TB, options ...func(*panel.Dependencies)) *Harness {
	t.Helper()
	h := &Harness{t: t, Dir: t.TempDir(), Clock: &Clock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}}
	t.Cleanup(h.ports.close)
	h.Dependencies = panel.Dependencies{
		Clock: h.Clock,
		DNSWatchLookup: func(context.Context, string, dnswatch.Resolver) ([]string, error) {
			return nil, errors.New("测试台未配置 DoH")
		},
		Persistence: platform.SQLite{Path: filepath.Join(h.Dir, "panel.db")},
		KeyFile:     platform.LocalKeyFile{Path: filepath.Join(h.Dir, "panel.key")},
		Random:      rand.Reader,
		LookupIPv4: func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("测试台未配置目标域名解析")
		},
	}
	for _, option := range options {
		option(&h.Dependencies)
	}
	// 独立 HTTPS/HTTP2 边界模拟生产 TLS 终结，核心 gRPC 本身仍是明文服务。
	h.Control = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.controlServer.Load().ServeHTTP(w, r) }))
	h.Control.EnableHTTP2 = true
	h.Control.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	h.Control.StartTLS()
	t.Cleanup(func() { h.Control.Close() })
	if err := h.start(); err != nil {
		t.Fatalf("启动面板: %v", err)
	}
	t.Cleanup(func() { h.stop() })
	return h
}

func (h *Harness) start() error {
	h.t.Helper()
	server := httptest.NewUnstartedServer(nil)
	origin := h.PublicOrigin
	if origin == "" {
		origin = "https://" + server.Listener.Addr().String()
	}
	core, err := panel.Open(context.Background(), panel.Config{PublicOrigin: origin, ControlURL: h.Control.URL}, h.Dependencies)
	if err != nil {
		server.Listener.Close()
		return err
	}
	h.Panel = core
	control := grpc.NewServer(grpc.MaxRecvMsgSize(64<<10), grpc.MaxSendMsgSize(64<<10), grpc.StreamInterceptor(h.repeatControlDelivery))
	core.RegisterControl(control)
	h.controlServer.Store(control)
	server.Config.Handler = core.Handler()
	server.StartTLS()
	h.Server = server
	return nil
}

func (h *Harness) stop() {
	if control := h.controlServer.Load(); control != nil {
		control.Stop()
	}
	if h.Server != nil {
		h.Server.Close()
		h.Server = nil
	}
	if h.Panel != nil {
		if err := h.Panel.Close(); err != nil {
			h.t.Errorf("关闭面板: %v", err)
		}
		h.Panel = nil
	}
}

func (h *Harness) Restart() error {
	h.stop()
	return h.start()
}

func (h *Harness) Client() *Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	client := new(http.Client)
	*client = *h.Server.Client()
	client.Jar = jar
	return &Client{h: h, HTTP: client}
}

func (c *Client) Request(method, path string, body any, headers map[string]string) *http.Response {
	c.h.t.Helper()
	var data io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.h.t.Fatal(err)
		}
		data = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.h.Server.URL+path, data)
	if err != nil {
		c.h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", c.h.Server.URL)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	return response
}

func (c *Client) Do(method, path string, body any, status int) map[string]any {
	c.h.t.Helper()
	response := c.Request(method, path, body, nil)
	defer response.Body.Close()
	var result map[string]any
	data, err := io.ReadAll(response.Body)
	if err != nil {
		c.h.t.Fatal(err)
	}
	if response.StatusCode != status {
		c.h.t.Fatalf("%s %s: 期望 %d，得到 %d: %s", method, path, status, response.StatusCode, data)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &result); err != nil {
			c.h.t.Fatalf("响应不是 JSON: %s", data)
		}
	}
	return result
}
