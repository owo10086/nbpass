package clusterharness_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"nbpass/internal/clusterharness"
	"nbpass/internal/node"
	"nbpass/internal/rotation"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRotationSettingsArePrivateAndRequireAdministrator(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "换 IP")
	path := "/api/nodes/" + created["id"].(string) + "/ip-rotation"
	h.Client().Do("GET", path, nil, 401)
	c.Do("GET", path, nil, 200)
	c.Do("PUT", path, map[string]any{"url": "https://vendor.example/change?key=private-token", "method": "POST", "headers": map[string]string{"Authorization": "Bearer private-token"}, "body": "{\"node\":\"{{node_id}}\"}"}, 200)
	state := c.Do("GET", path, nil, 200)
	if state["configured"] != true {
		t.Fatalf("配置未保存: %v", state)
	}
	if _, ok := state["url"]; ok {
		t.Fatalf("回显秘密: %v", state)
	}
	c.Do("PUT", path, map[string]any{"url": "http://vendor.example/change?key=private-token", "method": "POST"}, 400)
}

// 商家返回 2xx 不代表换成；只有两个查询源一致观察到新 IP 才更新面板及地区。
func TestManualRotationObservesAddressAndRegionThroughAPI(t *testing.T) {
	h, c := regionBrowser(t)
	var ip atomic.Value
	ip.Store("223.5.5.5")
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ip.Store("8.8.8.8"); w.WriteHeader(204) }))
	defer vendor.Close()
	roots := x509.NewCertPool()
	roots.AddCert(vendor.Certificate())
	created := addNode(c, "手动换 IP")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/ip-rotation"
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
		d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, d.PublicIPv4}
		d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{ip.Load().(string)}, nil }
		d.RotationHTTP = (rotation.HTTPExecutor{RootCAs: roots}).Do
	})
	awaitNode(t, c, id, connected)
	before := nodeRow(t, c, id)
	c.Do("PUT", path, map[string]any{"url": vendor.URL + "/change", "method": "POST", "private_endpoints": []string{vendor.URL + "/change"}}, 200)
	c.Do("POST", path+"/actions", nil, 202)
	state := awaitRotation(t, c, path, "observing")
	if state["new_ip"] != "" {
		t.Fatal("未观察就宣布换成")
	}
	c.Do("POST", path+"/actions", nil, 409)
	h.Clock.Advance(60 * time.Second)
	awaitRotation(t, c, path, "changed")
	row := nodeRow(t, c, id)
	if row["public_ipv4"] != "8.8.8.8" || row["automatic_region"].(map[string]any)["name"] != "美国" || row["domestic_checker"] != false {
		t.Fatalf("新IP/地区未更新: %v", row)
	}
	if row["desired_revision"] != before["desired_revision"] || row["applied_revision"] != before["applied_revision"] {
		t.Fatal("换 IP 误推进配置版本")
	}
}

func awaitRotation(t *testing.T, c *clusterharness.Client, path, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var state map[string]any
	for time.Now().Before(deadline) {
		result := c.Do("GET", path, nil, 200)
		state, _ = result["action"].(map[string]any)
		if state != nil && state["state"] == want {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("换 IP 未进入 %s: %v", want, state)
	return nil
}

func TestRotationCancelsBeforeCallingVendor(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		fail                bool
		unsafe              bool
	}{
		{name: "查询失败", fail: true},
		{name: "查询源不一致", first: "223.5.5.5", second: "8.8.8.8", unsafe: true},
		{name: "非公网", first: "10.0.0.1", second: "10.0.0.1"},
		{name: "无效地址", first: "invalid", second: "invalid"},
		{name: "旧地址不符", first: "8.8.8.8", second: "8.8.8.8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := regionBrowser(t)
			var calls atomic.Int32
			vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
			defer vendor.Close()
			roots := x509.NewCertPool()
			roots.AddCert(vendor.Certificate())
			created := addNode(c, tc.name)
			id := created["id"].(string)
			path := "/api/nodes/" + id + "/ip-rotation"
			h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
				d.PublicIPv4 = func(context.Context) (string, error) { return "223.5.5.5", nil }
				d.RotationIPv4Sources = [2]func(context.Context) (string, error){func(context.Context) (string, error) {
					if tc.fail {
						return "", errors.New("失败")
					}
					return tc.first, nil
				}, func(context.Context) (string, error) { return tc.second, nil }}
				d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{"223.5.5.5"}, nil }
				d.RotationHTTP = (rotation.HTTPExecutor{RootCAs: roots}).Do
			})
			awaitNode(t, c, id, connected)
			c.Do("PUT", path, map[string]any{"url": vendor.URL + "/change", "method": "POST", "private_endpoints": []string{vendor.URL + "/change"}}, 200)
			c.Do("POST", path+"/actions", nil, 202)
			state := awaitRotation(t, c, path, "cancelled")
			if state["attempts"] != float64(0) || calls.Load() != 0 {
				t.Fatalf("取消仍执行接口: %v 调用%d", state, calls.Load())
			}
			if row := nodeRow(t, c, id); row["public_ipv4"] != "223.5.5.5" {
				t.Fatalf("无效地址覆盖节点: %v", row)
			}
			if tc.unsafe && c.Do("GET", path, nil, 200)["automatic_eligible"] != false {
				t.Fatal("不一致节点仍可自动换 IP")
			}
		})
	}
}

func TestRotation2xxWithoutAddressChangeRemainsUnconfirmed(t *testing.T) {
	h, c := regionBrowser(t)
	var calls atomic.Int32
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer vendor.Close()
	roots := x509.NewCertPool()
	roots.AddCert(vendor.Certificate())
	created := addNode(c, "未变化")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/ip-rotation"
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return "223.5.5.5", nil }
		d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, d.PublicIPv4}
		d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{"10.0.0.2"}, nil }
		d.RotationHTTP = (rotation.HTTPExecutor{RootCAs: roots}).Do
	})
	awaitNode(t, c, id, connected)
	c.Do("PUT", path, map[string]any{"url": vendor.URL + "/change", "method": "POST", "private_endpoints": []string{vendor.URL + "/change"}}, 200)
	c.Do("POST", path+"/actions", nil, 202)
	awaitRotation(t, c, path, "observing")
	if row := c.Do("GET", path, nil, 200); row["automatic_eligible"] != false || row["warning"] == "" {
		t.Fatalf("NAT 未提示: %v", row)
	}
	for i := 1; i <= 5; i++ {
		h.Clock.Advance(60 * time.Second)
		want := "observing"
		if i == 5 {
			want = "unconfirmed"
		}
		deadline := time.Now().Add(6 * time.Second)
		for {
			a := awaitRotation(t, c, path, want)
			if a["attempts"] == float64(i) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("观察次数未推进: %v", a)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	c.Do("POST", path+"/actions", nil, 409)
	if calls.Load() != 1 {
		t.Fatalf("重复调用商家: %d", calls.Load())
	}
	if nodeRow(t, c, id)["public_ipv4"] != "223.5.5.5" {
		t.Fatal("2xx 伪造IP变化")
	}
}

func TestRotationExpiredOrDisconnectedDuringPreflightDoesNotCallVendor(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(fmt.Sprint("offline=", offline), func(t *testing.T) {
			h, c := regionBrowser(t)
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			created := addNode(c, "许可")
			id := created["id"].(string)
			path := "/api/nodes/" + id + "/ip-rotation"
			n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
				d.PublicIPv4 = func(context.Context) (string, error) { return "223.5.5.5", nil }
				d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, d.PublicIPv4}
				d.RotationLocalIPv4 = func(context.Context) ([]string, error) { close(entered); <-release; return []string{"223.5.5.5"}, nil }
				d.RotationHTTP = func(context.Context, rotation.Config, rotation.Variables) error { calls.Add(1); return nil }
			})
			awaitNode(t, c, id, connected)
			c.Do("PUT", path, map[string]any{"url": "https://vendor.example/change", "method": "POST"}, 200)
			c.Do("POST", path+"/actions", nil, 202)
			select {
			case <-entered:
			case <-time.After(6 * time.Second):
				t.Fatal("未开始前置检查")
			}
			if offline {
				n.Disconnect()
				awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
			} else {
				h.Clock.Advance(2 * time.Minute)
			}
			close(release)
			// 过期分支可能因同时快进控制超时而先断线，两者均不能执行。
			if !offline {
				h.Clock.Advance(time.Second)
			}
			n.Restore()
			for i := 0; i < 5; i++ {
				h.Clock.Advance(time.Second)
				time.Sleep(20 * time.Millisecond)
			}
			awaitRotation(t, c, path, "cancelled")
			if calls.Load() != 0 {
				t.Fatal("过期或离线许可执行了接口")
			}
			n.Disconnect()
			awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
			c.Do("POST", path+"/actions", nil, 409)
		})
	}
}

func TestRotationRejectsExpandedSecretsThatExceedCommandLimit(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "配置大小")
	path := "/api/nodes/" + created["id"].(string) + "/ip-rotation"
	request, err := http.NewRequest("PUT", h.Server.URL+path, strings.NewReader(`{"url":"https://vendor.example/change?q=`+strings.Repeat("&", 1500)+`","method":"POST"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", h.Server.URL)
	response, err := c.HTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("接受序列化后过大命令: %d", response.StatusCode)
	}
	if c.Do("GET", path, nil, 200)["configured"] != false {
		t.Fatal("超大命令已保存")
	}
}

// 控制主循环卡在活性查询时，接收协程已经看到断流也必须立即撤销执行资格。
func TestRotationTransportLossDuringBlockedHeartbeatCancelsExecution(t *testing.T) {
	h, c := regionBrowser(t)
	var heartbeats, calls atomic.Int32
	preflightEntered, releasePreflight := make(chan struct{}), make(chan struct{})
	heartbeatEntered, releaseHeartbeat := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releasePreflight:
		default:
			close(releasePreflight)
		}
		select {
		case <-releaseHeartbeat:
		default:
			close(releaseHeartbeat)
		}
	}()
	created := addNode(c, "断流窗口")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/ip-rotation"
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(ctx context.Context) (string, error) {
			if heartbeats.Add(1) == 2 {
				close(heartbeatEntered)
				select {
				case <-releaseHeartbeat:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return "223.5.5.5", nil
		}
		query := func(context.Context) (string, error) { return "223.5.5.5", nil }
		d.RotationIPv4Sources = [2]func(context.Context) (string, error){query, query}
		d.RotationLocalIPv4 = func(context.Context) ([]string, error) {
			close(preflightEntered)
			<-releasePreflight
			return []string{"223.5.5.5"}, nil
		}
		d.RotationHTTP = func(context.Context, rotation.Config, rotation.Variables) error { calls.Add(1); return nil }
	})
	awaitNode(t, c, id, connected)
	c.Do("PUT", path, map[string]any{"url": "https://vendor.example/change", "method": "POST"}, 200)
	c.Do("POST", path+"/actions", nil, 202)
	select {
	case <-preflightEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("未进入前置检查")
	}
	h.Clock.Advance(10 * time.Second)
	select {
	case <-heartbeatEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("未阻塞活性查询")
	}
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	time.Sleep(50 * time.Millisecond) // 给真实 gRPC 接收协程处理已经关闭的连接。
	close(releasePreflight)
	time.Sleep(50 * time.Millisecond)
	close(releaseHeartbeat)
	n.Restore()
	for i := 0; i < 5; i++ {
		h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	if calls.Load() != 0 {
		t.Fatal("接收协程已知断流，仍调用接口")
	}
	awaitRotation(t, c, path, "cancelled")
}

func TestRotationDoesNotPublishOneSourceBeforeConsensus(t *testing.T) {
	h, c := regionBrowser(t)
	var first, second atomic.Value
	first.Store("223.5.5.5")
	second.Store("223.5.5.5")
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { first.Store("8.8.8.8"); w.WriteHeader(204) }))
	defer vendor.Close()
	roots := x509.NewCertPool()
	roots.AddCert(vendor.Certificate())
	created := addNode(c, "查询共识")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/ip-rotation"
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return first.Load().(string), nil }
		d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, func(context.Context) (string, error) { return second.Load().(string), nil }}
		d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{"223.5.5.5"}, nil }
		d.RotationHTTP = (rotation.HTTPExecutor{RootCAs: roots}).Do
	})
	awaitNode(t, c, id, connected)
	c.Do("PUT", path, map[string]any{"url": vendor.URL + "/change", "method": "POST", "private_endpoints": []string{vendor.URL + "/change"}}, 200)
	c.Do("POST", path+"/actions", nil, 202)
	awaitRotation(t, c, path, "observing")
	before := nodeRow(t, c, id)["last_seen_at"]
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] != before })
	if nodeRow(t, c, id)["public_ipv4"] != "223.5.5.5" {
		t.Fatal("未达双源共识，活性报告提前发布新IP")
	}
	second.Store("8.8.8.8")
	h.Clock.Advance(50 * time.Second)
	awaitRotation(t, c, path, "changed")
}

func TestRotationContinuesObservingOfflineAndReportsAfterRestart(t *testing.T) {
	h, c := regionBrowser(t)
	var ip atomic.Value
	ip.Store("223.5.5.5")
	var calls atomic.Int32
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		ip.Store("8.8.8.8")
		w.WriteHeader(503)
		_, _ = w.Write([]byte("vendor-secret-response"))
	}))
	defer vendor.Close()
	roots := x509.NewCertPool()
	roots.AddCert(vendor.Certificate())
	created := addNode(c, "离线观察")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/ip-rotation"
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
		d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, d.PublicIPv4}
		d.RotationLocalIPv4 = func(context.Context) ([]string, error) { return []string{"223.5.5.5"}, nil }
		d.RotationHTTP = (rotation.HTTPExecutor{RootCAs: roots}).Do
	})
	awaitNode(t, c, id, connected)
	c.Do("PUT", path, map[string]any{"url": vendor.URL + "/change?token=rotation-private-marker", "method": "POST", "private_endpoints": []string{vendor.URL + "/change"}}, 200)
	c.Do("POST", path+"/actions", nil, 202)
	awaitRotation(t, c, path, "unconfirmed")
	n.Disconnect()
	awaitNode(t, c, id, func(row map[string]any) bool { return row["connected"] == false })
	h.Clock.Advance(60 * time.Second)
	// 离线期间真实执行器继续观察；重启从加密动作缓存恢复，不能再次调用商家。
	time.Sleep(40 * time.Millisecond)
	n.Restart()
	n.Restore()
	for i := 0; i < 5; i++ {
		h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	awaitRotation(t, c, path, "changed")
	if calls.Load() != 1 {
		t.Fatalf("重启后重复执行: %d", calls.Load())
	}
	events := c.Do("GET", "/api/events?node_id="+id, nil, 200)
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "rotation-private-marker") || strings.Contains(string(encoded), "vendor-secret-response") {
		t.Fatal("事件泄漏商家凭据或响应")
	}
}
