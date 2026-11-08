package clusterharness_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/ipregion"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

func regionBrowser(t *testing.T) (*clusterharness.Harness, *clusterharness.Client) {
	t.Helper()
	h := clusterharness.New(t, func(deps *panel.Dependencies) {
		deps.Passwords = nodePasswords{}
		deps.RegionLookup = func(_ context.Context, ip string) (panel.Region, error) {
			switch ip {
			case "223.5.5.5":
				return panel.Region{Name: "中国·浙江·杭州", MainlandChina: true}, nil
			case "8.8.8.8":
				return panel.Region{Name: "美国"}, nil
			case "1.1.1.1":
				return panel.Region{Name: "中国·香港"}, nil
			default:
				return panel.Region{}, errors.New("假 ip2region 无此地址")
			}
		}
	})
	c := h.Client()
	setup(c)
	login(c)
	return h, c
}

// 公网 IP 查询失败或返回无效/非公网地址时，不能覆盖最后一次有效观测，也不能中断活性。
func TestPublicIPv4QueryFailuresDoNotReplaceLastObservation(t *testing.T) {
	h, c := regionBrowser(t)
	var answer atomic.Value
	answer.Store("223.5.5.5")
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answer.Load().(string) == "error" {
			w.WriteHeader(503)
		}
		_, _ = w.Write([]byte(answer.Load().(string)))
	}))
	defer provider.Close()
	created := addNode(c, "查询失败")
	id := created["id"].(string)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.PublicIPv4 = (node.PublicIPv4Source{Client: provider.Client(), URL: provider.URL}).Query
	})
	awaitNode(t, c, id, connected)
	for _, invalid := range []string{"error", "", "not-an-ip", "127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "224.0.0.1", "240.0.0.1", "2001:4860:4860::8888", strings.Repeat(" ", 65) + "8.8.8.8"} {
		answer.Store(invalid)
		before := nodeRow(t, c, id)["last_seen_at"]
		h.Clock.Advance(10 * time.Second)
		awaitNode(t, c, id, func(row map[string]any) bool { return row["last_seen_at"] != before })
		if row := nodeRow(t, c, id); row["public_ipv4"] != "223.5.5.5" || row["domestic_checker"] != true {
			t.Fatalf("无效观测 %q 覆盖有效 IP: %v", invalid, row)
		}
	}
	answer.Store("8.8.8.8\n")
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool {
		return row["public_ipv4"] == "8.8.8.8" && row["domestic_checker"] == false
	})
}

func TestRegionManagementRequiresAdministratorOriginAndValidInput(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "地区管理校验")
	id := created["id"].(string)
	for _, path := range []string{"/api/nodes/" + id + "/region", "/api/nodes/" + id + "/checker"} {
		h.Client().Do("PUT", path, map[string]bool{"enabled": false}, 401)
		response := c.Request("PUT", path, map[string]bool{"enabled": false}, map[string]string{"Origin": "https://evil.example"})
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("跨源修改地区/角色: %d", response.StatusCode)
		}
	}
	regionPath := "/api/nodes/" + id + "/region"
	for _, invalid := range []any{map[string]any{"name": " "}, map[string]any{"name": strings.Repeat("地", 67)}, map[string]any{"name": "中国大陆", "other": true}, []any{}} {
		c.Do("PUT", regionPath, invalid, 400)
	}
	for _, invalid := range []any{map[string]any{}, map[string]any{"enabled": "false"}, map[string]any{"enabled": nil}, (*bool)(nil)} {
		c.Do("PUT", "/api/nodes/"+id+"/checker", invalid, 400)
	}
	c.Do("PUT", "/api/nodes/missing/region", map[string]any{"name": "中国大陆", "mainland_china": true}, 404)
	c.Do("PUT", "/api/nodes/missing/checker", map[string]bool{"enabled": false}, 404)
	row := nodeRow(t, c, id)
	if row["region_source"] != "auto" || row["domestic_checker"] != false || row["checker_disabled"] != false {
		t.Fatalf("无效请求修改了地区或角色: %v", row)
	}
}

// 查询源卡住时须按注入时钟超时，不能让公网 IP 查询阻止节点接入和活性。
func TestPublicIPv4QueryTimeoutDoesNotPreventNodeLiveness(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "查询超时")
	id := created["id"].(string)
	started := make(chan struct{}, 1)
	h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.PublicIPv4 = func(ctx context.Context) (string, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return "", ctx.Err()
		}
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("节点未开始查询公网 IP")
	}
	h.Clock.Advance(5 * time.Second)
	awaitNode(t, c, id, connected)
	if row := nodeRow(t, c, id); row["public_ipv4"] != "" || row["domestic_checker"] != false {
		t.Fatalf("查询超时伪造地区: %v", row)
	}
}

// 离线适配器若未正确处理 ip2region 的国家码或港澳台，API 将错误授予角色。
func TestBundledIP2RegionThroughNodeHTTPAPI(t *testing.T) {
	lookup, err := ipregion.New()
	if err != nil {
		t.Fatal(err)
	}
	h := clusterharness.New(t, func(deps *panel.Dependencies) {
		deps.Passwords = nodePasswords{}
		deps.RegionLookup = lookup
	})
	c := h.Client()
	setup(c)
	login(c)
	for _, tc := range []struct {
		ip      string
		checker bool
	}{
		{"223.5.5.5", true},
		{"8.8.8.8", false},
		{"203.198.7.66", false},
		{"202.175.3.3", false},
		{"168.95.1.1", false},
	} {
		created := addNode(c, tc.ip)
		id := created["id"].(string)
		h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
			deps.PublicIPv4 = func(context.Context) (string, error) { return tc.ip, nil }
		})
		awaitNode(t, c, id, connected)
		if row := nodeRow(t, c, id); row["region"].(map[string]any)["name"] == "未知" || row["domestic_checker"] != tc.checker {
			t.Fatalf("离线库识别 %s: %v", tc.ip, row)
		}
	}
}

// 自动识别若覆盖人工结果、IP 变化不重识别或覆盖未持久化时失败。
func TestManualRegionSurvivesIPChangeAndPanelRestart(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "人工地区")
	id := created["id"].(string)
	var ip atomic.Value
	ip.Store("223.5.5.5")
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
	})
	awaitNode(t, c, id, connected)
	c.Do("PUT", "/api/nodes/"+id+"/region", map[string]any{"name": "日本·东京", "mainland_china": false}, 204)
	ip.Store("8.8.8.8")
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["public_ipv4"] == "8.8.8.8" })
	assertRegion := func(c *clusterharness.Client) {
		t.Helper()
		row := nodeRow(t, c, id)
		if row["region"].(map[string]any)["name"] != "日本·东京" || row["region_source"] != "manual" || row["domestic_checker"] != false || row["automatic_region"].(map[string]any)["name"] != "美国" {
			t.Fatalf("人工覆盖未保留: %v", row)
		}
	}
	assertRegion(c)
	n.Stop()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	assertRegion(c)
	c.Do("PUT", "/api/nodes/"+id+"/region", nil, 415)
	// JSON null 明确恢复最新的自动识别结果。
	c.Do("PUT", "/api/nodes/"+id+"/region", (*panel.Region)(nil), 204)
	row := nodeRow(t, c, id)
	if row["region"].(map[string]any)["name"] != "美国" || row["region_source"] != "auto" {
		t.Fatalf("未恢复自动地区: %v", row)
	}
}

// 关闭开关若被新报告、IP 变化、地区修改或重启重置，此测试失败。
func TestDomesticCheckerCanBeDisabledAndRestored(t *testing.T) {
	h, c := regionBrowser(t)
	created := addNode(c, "关闭检测点")
	id := created["id"].(string)
	path := "/api/nodes/" + id + "/checker"
	c.Do("PUT", path, map[string]bool{"enabled": false}, 204)
	var ip atomic.Value
	ip.Store("223.5.5.5")
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.PublicIPv4 = func(context.Context) (string, error) { return ip.Load().(string), nil }
	})
	awaitNode(t, c, id, connected)
	if row := nodeRow(t, c, id); row["domestic_checker"] != false || row["checker_disabled"] != true {
		t.Fatalf("关闭的检测点恢复了角色: %v", row)
	}
	ip.Store("8.8.8.8")
	h.Clock.Advance(10 * time.Second)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["public_ipv4"] == "8.8.8.8" })
	c.Do("PUT", "/api/nodes/"+id+"/region", map[string]any{"name": "中国大陆·手动", "mainland_china": true}, 204)
	n.Stop()
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	if row := nodeRow(t, c, id); row["domestic_checker"] != false || row["checker_disabled"] != true {
		t.Fatalf("关闭开关未持久化: %v", row)
	}
	c.Do("PUT", path, map[string]bool{"enabled": true}, 204)
	if row := nodeRow(t, c, id); row["domestic_checker"] != true || row["checker_disabled"] != false {
		t.Fatalf("人工大陆地区未恢复角色: %v", row)
	}
	c.Do("PUT", "/api/nodes/"+id+"/region", (*panel.Region)(nil), 204)
	if nodeRow(t, c, id)["domestic_checker"] != false {
		t.Fatal("开启开关不应给非大陆节点授予角色")
	}
}

// 缺少真实节点的 IP 上报、错误使用控制连接地址或把非大陆节点授予角色时失败。
func TestNodeRegionDeterminesDefaultDomesticChecker(t *testing.T) {
	h, c := regionBrowser(t)
	for _, tc := range []struct {
		ip, name string
		checker  bool
	}{
		{"223.5.5.5", "中国·浙江·杭州", true},
		{"8.8.8.8", "美国", false},
		{"1.1.1.1", "中国·香港", false},
		{"9.9.9.9", "未知", false},
	} {
		t.Run(tc.ip, func(t *testing.T) {
			created := addNode(c, tc.ip)
			id := created["id"].(string)
			h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
				deps.PublicIPv4 = func(context.Context) (string, error) { return tc.ip, nil }
			})
			awaitNode(t, c, id, connected)
			row := nodeRow(t, c, id)
			region, ok := row["region"].(map[string]any)
			if !ok || region["name"] != tc.name || row["public_ipv4"] != tc.ip || row["domestic_checker"] != tc.checker || row["region_source"] != "auto" {
				t.Fatalf("地区与默认角色: %v", row)
			}
		})
	}
}
