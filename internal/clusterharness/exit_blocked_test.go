package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
)

type exitBlockedRig struct {
	f        *rotationFixture
	sites    *domesticSites
	path     string
	entries  []*clusterharness.Node
	entryIDs []string
	ports    []int
	link     *exitLink
}

// 只替换公网地址映射、国内站点和商家接口；业务走真实 TCP/WSS/yamux。
func newExitBlockedRig(t *testing.T, count int, entryOptions ...func(*node.Config, *node.Dependencies)) *exitBlockedRig {
	t.Helper()
	h, c := nodeBrowser(t)
	settings := c.Do("GET", "/api/settings/self-check", nil, 200)
	settings["jitter_seconds"] = 0
	c.Do("PUT", "/api/settings/self-check", settings, 204)
	reserve, listen := directPorts(t)
	r := &exitBlockedRig{sites: &domesticSites{}, link: &exitLink{}}
	target := echoTarget(t, "出口:")
	r.f = rotationFixtureAt(t, h, c, false, r.sites.dependencies, func(cfg *node.Config, d *node.Dependencies) {
		listen(cfg, d)
		d.Forwarding.DialTarget = func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(target)))
		}
	})
	exitPort := reserve()
	c.Do("PUT", "/api/nodes/"+r.f.id+"/tunnel", map[string]any{"address": "223.5.5.5", "listen_port": exitPort, "transport": "wss"}, 200)
	backup := addNode(c, "后备出口")
	backupID := backup["id"].(string)
	backupTarget := echoTarget(t, "后备:")
	h.StartNode(backup["installation_ticket"].(string), func(cfg *node.Config, d *node.Dependencies) {
		listen(cfg, d)
		d.Forwarding.DialTarget = func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(backupTarget)))
		}
	})
	awaitNode(t, c, backupID, connected)
	backupPort := reserve()
	c.Do("PUT", "/api/nodes/"+backupID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": backupPort, "transport": "wss"}, 200)
	group := c.Do("POST", "/api/exit-groups", map[string]any{"name": "主备出口", "members": []string{r.f.id, backupID}, "strategy": "primary_backup"}, 201)["id"].(string)
	for i := 0; i < count; i++ {
		row := addNode(c, fmt.Sprintf("相关入口%d", i))
		id := row["id"].(string)
		n := h.StartNode(row["installation_ticket"].(string), func(cfg *node.Config, d *node.Dependencies) {
			listen(cfg, d)
			d.Forwarding.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
				host, port, _ := net.SplitHostPort(address)
				if port == strconv.Itoa(exitPort) {
					if host != r.f.ip.Load().(string) {
						return nil, errors.New("出口旧地址或被墙")
					}
					return r.link.dial(ctx, net.JoinHostPort("127.0.0.1", port))
				}
				return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
			}
			d.Forwarding.DialTarget = func(context.Context, string) (net.Conn, error) { return nil, errors.New("入口禁止直连目标") }
			for _, option := range entryOptions {
				option(cfg, d)
			}
		})
		awaitNode(t, c, id, connected)
		port := reserve()
		body := directBody(entryGroup(c, id)["id"].(string), port, "127.0.0.1", 12345)
		body["kind"], body["exit_group_id"] = "tunnel", group
		c.Do("POST", "/api/rules", body, 201)
		r.entries = append(r.entries, n)
		r.entryIDs = append(r.entryIDs, id)
		r.ports = append(r.ports, port)
	}
	for _, id := range append([]string{r.f.id, backupID}, r.entryIDs...) {
		awaitRevision(t, c, id, 0)
	}
	r.path = "/api/nodes/" + r.f.id + "/exit-blocked"
	return r
}

type legacyExitAddressCache struct {
	node.ConfigStore
	strip atomic.Bool
}

func (c *legacyExitAddressCache) LoadConfig(ctx context.Context) ([]byte, error) {
	data, err := c.ConfigStore.LoadConfig(ctx)
	if err != nil || !c.strip.Swap(false) {
		return data, err
	}
	var cache struct {
		Version    int              `json:"version"`
		NodeID     string           `json:"node_id"`
		ControlURL string           `json:"control_url"`
		State      forwarding.State `json:"state"`
	}
	if err = json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	// 模拟旧节点 JSON 缓存遗漏未知 protobuf 字段，保留原版本与摘要。
	for _, g := range cache.State.ExitGroups {
		for _, ep := range g.Exits {
			ep.AddressVersion = ""
		}
	}
	return json.Marshal(cache)
}

func TestExitBlockedUpgradeRepairsLegacyAddressVersionCache(t *testing.T) {
	cache := &legacyExitAddressCache{}
	r := newExitBlockedRig(t, 1, func(_ *node.Config, d *node.Dependencies) {
		cache.ConfigStore = d.Store.(node.ConfigStore)
		d.ConfigStore = cache
	})
	f := r.f
	r.sites.mode.Store(2)
	f.c.Do("POST", f.path+"/actions", nil, 202)
	a := awaitRotation(t, f.c, f.path, "observing")
	f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, f, time.Minute)
	awaitRotation(t, f.c, f.path, "changed")
	awaitRevision(t, f.c, r.entryIDs[0], 0)
	r.entries[0].Disconnect()
	cache.strip.Store(true)
	r.entries[0].Restore()
	r.entries[0].Restart()
	awaitNode(t, f.c, r.entryIDs[0], connected)
	// 相同revision/digest的快照也须补回丢失的地址版本，不靠无关配置变化。
	var health map[string]any
	for step := 0; step < 6; step++ {
		advanceHealthyRotation(t, f, 6*time.Second)
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			health = f.c.Do("GET", "/api/nodes/"+r.entryIDs[0]+"/exit-health", nil, 200)
			for _, raw := range health["exits"].([]any) {
				h := raw.(map[string]any)
				if h["exit_id"] == f.id && h["address_version"] == a["id"] && h["last_probe"] == "success" {
					tcpExchange(t, r.ports[0], "出口:")
					if f.calls.Load() != 1 {
						t.Fatal("补齐地址版本重复换IP")
					}
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatalf("旧缓存升级后未补回地址版本: %v", health)
}

func (r *exitBlockedRig) enable(t *testing.T) {
	t.Helper()
	c, f := r.f.c, r.f
	c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	c.Do("PUT", r.path, map[string]bool{"enabled": true}, 200)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, f.h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, f.h, c, f.id, 3, "blocked", 3)
	tickPolicy(t, f)
	awaitRotation(t, c, f.path, "observing")
}

func awaitExitAddress(t *testing.T, r *exitBlockedRig, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	var v map[string]any
	for time.Now().Before(deadline) {
		v = r.f.c.Do("GET", r.path, nil, 200)
		if ready(v) {
			return v
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("出口地址进度未收敛: %v", v)
	return nil
}
func exitAddressEntry(v map[string]any, id string) map[string]any {
	entries, _ := v["entries"].([]any)
	for _, raw := range entries {
		e := raw.(map[string]any)
		if e["node_id"] == id {
			return e
		}
	}
	return map[string]any{}
}

func awaitExitAddressHandshake(t *testing.T, r *exitBlockedRig, id string) {
	t.Helper()
	// 等待真实探测及其独立采样落库，再推进下一拍，避免快进越过异步采样。
	for step := 0; step < 6; step++ {
		advanceHealthyRotation(t, r.f, 6*time.Second)
		deadline := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(deadline) {
			v := r.f.c.Do("GET", r.path, nil, 200)
			e := exitAddressEntry(v, id)
			if e["applied"] == true && e["handshaken"] == true {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	t.Fatalf("真实握手未确认: %v", r.f.c.Do("GET", "/api/nodes/"+id+"/exit-health", nil, 200))
}

// 缺少出口策略、地址下发或新地址真实握手，都会阻止恢复。
func TestExitBlockedRotationRestoresTunnelAndConvergesOfflineEntry(t *testing.T) {
	r := newExitBlockedRig(t, 2)
	f := r.f
	for _, port := range r.ports {
		tcpExchange(t, port, "出口:")
	}
	r.entries[1].Disconnect()
	awaitNode(t, f.c, r.entryIDs[1], func(v map[string]any) bool { return v["connected"] == false })
	r.enable(t)
	f.c.Do("POST", f.path+"/actions", nil, 409)
	assertRotationQuota(t, f, 1, 1)
	// 换 IP 观察期间，主出口故障后后备出口仍接新连接。
	r.link.block(true)
	tcpExchange(t, r.ports[0], "后备:")
	// 地址更新与证书/凭据轮换并行，离线入口不能借用在线入口的确认。
	f.c.Do("POST", "/api/nodes/"+f.id+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	cert := &authorizationFixture{h: f.h, c: f.c, exitID: f.id}
	awaitTunnelRotation(t, cert, "distributing")
	r.sites.mode.Store(2)
	f.ip.Store("8.8.8.8")
	r.link.recover()
	advanceHealthyRotation(t, f, time.Minute)
	awaitRotation(t, f.c, f.path, "changed")
	awaitRevision(t, f.c, r.entryIDs[0], 0)
	v := awaitExitAddress(t, r, func(v map[string]any) bool { return exitAddressEntry(v, r.entryIDs[0])["applied"] == true })
	if e := exitAddressEntry(v, r.entryIDs[1]); e["applied"] != false || e["handshaken"] != false || e["connected"] != false {
		t.Fatalf("离线入口被误确认: %v", e)
	}
	awaitExitAddressHandshake(t, r, r.entryIDs[0])
	v = awaitExitAddress(t, r, func(v map[string]any) bool { return exitAddressEntry(v, r.entryIDs[0])["handshaken"] == true })
	if v["address"] != "8.8.8.8" || v["action_id"] == "" {
		t.Fatalf("缺少新地址及动作标识: %v", v)
	}
	tcpExchange(t, r.ports[0], "出口:")
	// 面板重建保留两类确认，不把离线参与者补为成功。
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	cert.c = f.c
	v = f.c.Do("GET", r.path, nil, 200)
	if exitAddressEntry(v, r.entryIDs[0])["handshaken"] != true || exitAddressEntry(v, r.entryIDs[1])["applied"] != false {
		t.Fatalf("重建丢失或伪造进度: %v", v)
	}
	r.entries[1].Restore()
	r.entries[1].Restart()
	awaitNode(t, f.c, r.entryIDs[1], connected)
	awaitRevision(t, f.c, r.entryIDs[1], 0)
	awaitExitAddressHandshake(t, r, r.entryIDs[1])
	awaitTunnelRotation(t, cert, "complete")
	tcpExchange(t, r.ports[1], "出口:")
	if f.calls.Load() != 1 {
		t.Fatalf("重连重复换 IP: %d", f.calls.Load())
	}
	assertRotationQuota(t, f, 1, 1)
}

// 出口与入口组两种用途同开，不能各自执行或绕过同节点额度。
func TestExitBlockedSharesEntryRotationQuotaAndOptIn(t *testing.T) {
	r := newExitBlockedRig(t, 1)
	f := r.f
	c := f.c
	if c.Do("GET", r.path, nil, 200)["enabled"] != false {
		t.Fatal("出口处理默认开启")
	}
	c.Do("PUT", f.path+"/automatic", map[string]bool{"enabled": true}, 200)
	c.Do("PUT", "/api/nodes/"+f.id+"/self-check", map[string]bool{"enabled": true}, 204)
	awaitSelfCheck(t, c, f.id, 1, "suspected", 1)
	advanceSelfCheck(t, f.h, c, f.id, 2, "suspected", 2)
	advanceSelfCheck(t, f.h, c, f.id, 3, "blocked", 3)
	tickPolicy(t, f)
	if c.Do("GET", f.path, nil, 200)["action"] != nil {
		t.Fatal("未开启出口处理却换 IP")
	}
	gid := entryGroup(c, f.id)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "rotate_ip"}, 200)
	c.Do("PUT", r.path, map[string]bool{"enabled": true}, 200)
	tickPolicy(t, f)
	awaitRotation(t, c, f.path, "observing")
	c.Do("POST", f.path+"/actions", nil, 409)
	assertRotationQuota(t, f, 1, 1)
	r.sites.mode.Store(2)
	f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, f, time.Minute)
	awaitRotationView(t, f, func(v map[string]any) bool { return v["recheck_status"] == "recovered" })
	tickPolicy(t, f)
	if f.calls.Load() != 1 {
		t.Fatalf("双角色重复调用: %d", f.calls.Load())
	}
	assertRotationQuota(t, f, 1, 1)
	// 关闭入口组用途后，出口用途仍受之前占用的一小时额度约束。
	c.Do("PUT", "/api/entry-groups/"+gid+"/blocked-policy", map[string]string{"policy": "disabled"}, 200)
	r.sites.mode.Store(0)
	advanceHealthyRotation(t, f, 30*time.Minute)
	tickPolicy(t, f)
	if c.Do("GET", "/api/nodes/"+f.id+"/self-check", nil, 200)["state"] != "blocked" {
		t.Fatal("未实际触发出口被墙申请")
	}
	q := assertRotationQuota(t, f, 1, 1)["quota"].(map[string]any)
	if q["cooldown_seconds"] != float64(0) || q["retry_after_seconds"].(float64) <= 0 {
		t.Fatalf("未覆盖冷却结束但共享小时额度仍满的窗口: %v", q)
	}
	if f.calls.Load() != 1 {
		t.Fatal("出口用途绕过入口组共用额度")
	}
	advanceHealthyRotation(t, f, 30*time.Minute)
	tickPolicy(t, f)
	awaitRotation(t, c, f.path, "observing")
	assertRotationQuota(t, f, 1, 2)
	if f.calls.Load() != 2 {
		t.Fatal("额度释放后出口用途没有继续执行")
	}
}

func TestExitBlockedSettingsRequireAdministratorAndExplicitBoolean(t *testing.T) {
	r := newExitBlockedRig(t, 0)
	r.f.h.Client().Do("GET", r.path, nil, 401)
	r.f.h.Client().Do("PUT", r.path, map[string]bool{"enabled": true}, 401)
	r.f.c.Do("PUT", r.path, map[string]any{}, 400)
	r.f.c.Do("PUT", r.path, map[string]any{"enabled": true, "unknown": true}, 400)
	r.f.c.Do("GET", "/api/nodes/missing/exit-blocked", nil, 404)
	r.f.c.Do("PUT", "/api/nodes/missing/exit-blocked", map[string]bool{"enabled": true}, 404)
}
