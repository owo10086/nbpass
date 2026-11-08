package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
	"net"
	"strconv"
	"testing"
	"time"
)

func rotationStatus(t *testing.T, f *authorizationFixture) map[string]any {
	t.Helper()
	return f.c.Do("GET", "/api/nodes/"+f.exitID+"/tunnel/rotation", nil, 200)
}

func awaitTunnelRotation(t *testing.T, f *authorizationFixture, phase string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		v := rotationStatus(t, f)
		if v["phase"] == phase {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("轮换未到 %s: %v", phase, v)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertRotationConnection(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetDeadline(time.Now().Add(time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 6)
	if _, err := io.ReadFull(c, data); err != nil || string(data) != "T:ping" {
		t.Fatalf("轮换中已有连接中断: %q %v", data, err)
	}
}

func TestTunnelRotationPreservesExistingAndNewTCPConnections(t *testing.T) {
	f := newAuthorizationFixture(t)
	f.create(t)
	conn := tcpExchange(t, f.port, "T:")
	before := f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)[0].(map[string]any)
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	awaitTunnelRotation(t, &f, "complete")
	f.h.Clock.Advance(time.Second)
	assertRotationConnection(t, conn)
	tcpExchange(t, f.port, "T:")
	after := f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)[0].(map[string]any)
	if before["certificate_pem"] == after["certificate_pem"] || before["server_name"] != after["server_name"] {
		t.Fatal("轮换必须更换证书并保留稳定身份")
	}
}

// 双入口中一个仅控制离线，旧缓存仍须能建立新业务；满 7 天和面板重启均不强撤。
func TestTunnelRotationOfflineEntryRetainsOldMaterialsAndConverges(t *testing.T) {
	f := newAuthorizationFixture(t)
	id := f.create(t)
	e := addNode(f.c, "离线轮换入口")
	secondID := e["id"].(string)
	second := f.h.StartNode(e["installation_ticket"].(string))
	awaitNode(t, f.c, secondID, connected)
	other := directBody(entryGroup(f.c, secondID)["id"].(string), freePort(t), "127.0.0.1", f.body["target_port"].(int))
	other["kind"], other["exit_group_id"] = "tunnel", f.body["exit_group_id"]
	r := f.c.Do("POST", "/api/rules", other, 201)
	awaitRule(t, f.c, r["id"].(string), "正常")
	awaitRule(t, f.c, id, "正常")
	second.Disconnect()
	awaitNode(t, f.c, secondID, func(v map[string]any) bool { return v["connected"] == false })
	conn := tcpExchange(t, f.port, "T:")
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	v := awaitTunnelRotation(t, &f, "distributing")
	deadline := time.Now().Add(3 * time.Second)
	for v["pending"] != float64(1) {
		if time.Now().After(deadline) {
			t.Fatalf("入口确认进度错误: %v", v)
		}
		time.Sleep(5 * time.Millisecond)
		v = rotationStatus(t, &f)
	}
	for _, row := range v["entries"].([]any) {
		entry := row.(map[string]any)
		if entry["confirmed"] != (entry["node_id"] == f.entryID) {
			t.Fatalf("入口确认归属错误: %v", v)
		}
	}
	assertRotationConnection(t, conn)
	tcpExchange(t, f.port, "T:")
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 409)
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	// 先恢复在线进程控制，避免将长时间快进造成的活性超时混入轮换断言。
	for i := 0; i < 5; i++ {
		f.h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitNode(t, f.c, f.entryID, connected)
	awaitNode(t, f.c, f.exitID, connected)
	end, err := time.Parse(time.RFC3339, v["deadline"].(string))
	if err != nil {
		t.Fatal(err)
	}
	f.h.Clock.Advance(end.Sub(f.h.Clock.Now()) - time.Nanosecond)
	login(f.c)
	if rotationStatus(t, &f)["overdue"] != false {
		t.Fatal("未满7天即提示超期")
	}
	f.h.Clock.Advance(time.Nanosecond)
	v = rotationStatus(t, &f)
	if v["phase"] != "distributing" || v["overdue"] != true || v["warning"] == "" {
		t.Fatalf("超期必须保留并提示: %v", v)
	}
	assertRotationConnection(t, conn)
	second.Restore()
	for i := 0; i < 10; i++ {
		f.h.Clock.Advance(time.Second)
		time.Sleep(15 * time.Millisecond)
	}
	awaitTunnelRotation(t, &f, "complete")
	assertRotationConnection(t, conn)
	tcpExchange(t, f.port, "T:")
}

func TestTunnelRotationCertificateAndCredentialsCanRotateSeparately(t *testing.T) {
	for _, kind := range []string{"certificate", "credentials"} {
		t.Run(kind, func(t *testing.T) {
			f := newAuthorizationFixture(t)
			f.create(t)
			conn := tcpExchange(t, f.port, "T:")
			before := f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)[0].(map[string]any)
			f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": kind}, 202)
			awaitTunnelRotation(t, &f, "complete")
			f.h.Clock.Advance(time.Second)
			assertRotationConnection(t, conn)
			tcpExchange(t, f.port, "T:")
			after := f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)[0].(map[string]any)
			if (before["certificate_pem"] == after["certificate_pem"]) != (kind == "credentials") {
				t.Fatal("独立证书轮换范围错误")
			}
		})
	}
}

func TestTunnelRotationOldCredentialCannotOpenNewStreamAfterRetirement(t *testing.T) {
	f := newAuthorizationFixture(t)
	f.create(t)
	data, err := f.entryCache.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var cache struct {
		Version    int              `json:"version"`
		NodeID     string           `json:"node_id"`
		ControlURL string           `json:"control_url"`
		State      forwarding.State `json:"state"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	cache.State.Rules[0].ListenPort = uint32(port)
	data, err = json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	rogue := f.h.CloneNode(f.entry, "", func(_ *node.Config, d *node.Dependencies) {
		d.ConfigStore = &forgedAuthorizationCache{data: data}
		d.DialContext = func(context.Context, string) (net.Conn, error) { return nil, errors.New("仅旧缓存") }
	})
	defer rogue.Stop()
	waitTunnelPort(t, port)
	old := tcpExchange(t, port, "T:")
	f.entry.Disconnect()
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	awaitTunnelRotation(t, &f, "distributing")
	tcpExchange(t, port, "T:") // 仍使用旧证书、旧凭据、真实旧 WSS/yamux 载体。
	f.entry.Restore()
	for i := 0; i < 8; i++ {
		f.h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitTunnelRotation(t, &f, "complete")
	f.h.Clock.Advance(time.Second)
	assertRotationConnection(t, old)
	assertTCPFailure(t, port) // 同一旧载体的新流必须重新核对旧凭据并被拒。
	tcpExchange(t, f.port, "T:")
}
func waitTunnelPort(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 20*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("真实入口监听未恢复")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTunnelRotationWaitsForExitDurabilityBeforeChangingEntry(t *testing.T) {
	store := &faultyConfigStore{}
	f := newAuthorizationFixture(t, func(_ *node.Config, d *node.Dependencies) {
		store.ConfigStore = d.Store.(node.ConfigStore)
		d.ConfigStore = store
	})
	f.create(t)
	conn := tcpExchange(t, f.port, "T:")
	previous := nodeRow(t, f.c, f.entryID)["desired_revision"]
	store.fail.Store(true)
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	awaitNode(t, f.c, f.exitID, func(v map[string]any) bool { return v["config_status"] == "同步失败" })
	if v := rotationStatus(t, &f); v["phase"] != "preparing" {
		t.Fatalf("失败报告推进轮换: %v", v)
	}
	if nodeRow(t, f.c, f.entryID)["desired_revision"] != previous {
		t.Fatal("出口尚未落盘，入口已切换轮换材料")
	}
	assertRotationConnection(t, conn)
	tcpExchange(t, f.port, "T:")
	store.fail.Store(false)
	f.h.Clock.Advance(10 * time.Second)
	awaitTunnelRotation(t, &f, "complete")
	assertRotationConnection(t, conn)
	tcpExchange(t, f.port, "T:")
}

func TestTunnelRotationRemovedEntryCannotReviveOldCredential(t *testing.T) {
	f := newAuthorizationFixture(t)
	id := f.create(t)
	data, err := f.entryCache.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		State forwarding.State `json:"state"`
	}
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	oldCredential := original.State.ExitGroups[0].Exits[0].Credential
	f.entry.Disconnect()
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "credentials"}, 202)
	awaitTunnelRotation(t, &f, "distributing")
	f.c.Do("DELETE", "/api/rules/"+id, nil, 204)
	awaitTunnelRotation(t, &f, "complete")
	// 原节点恢复后创建新的合法授权；攻击者仅把新本机缓存的凭据换回已撤下的旧凭据。
	f.entry.Restore()
	for i := 0; i < 5; i++ {
		f.h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitNode(t, f.c, f.entryID, connected)
	r := f.c.Do("POST", "/api/rules", f.body, 201)
	awaitRule(t, f.c, r["id"].(string), "正常")
	data, err = f.entryCache.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var cache struct {
		Version    int              `json:"version"`
		NodeID     string           `json:"node_id"`
		ControlURL string           `json:"control_url"`
		State      forwarding.State `json:"state"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	cache.State.Rules[0].ListenPort = uint32(port)
	cache.State.ExitGroups[0].Exits[0].Credential = oldCredential
	data, err = json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	rogue := f.h.CloneNode(f.entry, "", func(_ *node.Config, d *node.Dependencies) {
		d.ConfigStore = &forgedAuthorizationCache{data: data}
		d.DialContext = func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("攻击者仅持有本机缓存")
		}
	})
	defer rogue.Stop()
	waitTunnelPort(t, port)
	assertTCPFailure(t, port)
	tcpExchange(t, f.port, "T:")
}
