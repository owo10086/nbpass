package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
	"net"
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
	// 本场景只验证7天重叠期限，空闲回收由票11单独覆盖。
	f := newAuthorizationFixtureWithLimits(t, forwarding.ResourceLimits{IdleTimeout: 8 * 24 * time.Hour})
	id := f.create(t)
	e := addNode(f.c, "离线轮换入口")
	secondID := e["id"].(string)
	second := f.h.StartNode(e["installation_ticket"].(string))
	awaitNode(t, f.c, secondID, connected)
	other := directBody(entryGroup(f.c, secondID)["id"].(string), f.h.ReserveTCPPort(), "127.0.0.1", f.body["target_port"].(int))
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
	port := f.h.ReserveTCPPort()
	cache.State.Rules[0].ListenPort = uint32(port)
	data, err = json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	listening := make(chan struct{}, 1)
	rogue := f.h.CloneNode(f.entry, "", func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.Listen = func(network, address string) (net.Listener, error) {
			listener, err := f.h.ListenTCP(network, address)
			if err == nil {
				listening <- struct{}{}
			}
			return listener, err
		}
		d.ConfigStore = &forgedAuthorizationCache{data: data}
		d.DialContext = func(context.Context, string) (net.Conn, error) { return nil, errors.New("仅旧缓存") }
	})
	defer rogue.Stop()
	awaitRotationCacheListener(t, listening)
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
func awaitRotationCacheListener(t *testing.T, listening <-chan struct{}) {
	t.Helper()
	select {
	case <-listening:
	case <-time.After(3 * time.Second):
		t.Fatal("真实入口未接管预留监听器")
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
	port := f.h.ReserveTCPPort()
	cache.State.Rules[0].ListenPort = uint32(port)
	cache.State.ExitGroups[0].Exits[0].Credential = oldCredential
	data, err = json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	listening := make(chan struct{}, 1)
	rogue := f.h.CloneNode(f.entry, "", func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.Listen = func(network, address string) (net.Listener, error) {
			listener, err := f.h.ListenTCP(network, address)
			if err == nil {
				listening <- struct{}{}
			}
			return listener, err
		}
		d.ConfigStore = &forgedAuthorizationCache{data: data}
		d.DialContext = func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("攻击者仅持有本机缓存")
		}
	})
	defer rogue.Stop()
	awaitRotationCacheListener(t, listening)
	assertTCPFailure(t, port)
	tcpExchange(t, f.port, "T:")
}

// 两个出口同时轮换，各自的入口确认/新材料和既有业务不能相互覆盖。
func TestTunnelRotationMultipleExitsPreserveSelectionAndHealth(t *testing.T) {
	r := setupFailover(t, 2)
	primary := tcpExchange(t, r.port, "0:")
	r.targetFailed[0].Store(true)
	backup := tcpExchange(t, r.port, "1:")
	r.targetFailed[0].Store(false)
	for _, id := range r.exitIDs {
		r.c.Do("POST", "/api/nodes/"+id+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	}
	for _, id := range r.exitIDs {
		f := authorizationFixture{h: r.h, c: r.c, exitID: id}
		awaitTunnelRotation(t, &f, "complete")
	}
	r.h.Clock.Advance(time.Second)
	for _, pair := range []struct {
		conn   net.Conn
		prefix string
	}{{primary, "0:ping"}, {backup, "1:ping"}} {
		pair.conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := pair.conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, len(pair.prefix))
		if _, err := io.ReadFull(pair.conn, b); err != nil || string(b) != pair.prefix {
			t.Fatalf("多出口轮换中断既有业务: %q %v", b, err)
		}
	}
	tcpExchange(t, r.port, "0:")
	for _, id := range r.exitIDs {
		var row map[string]any
		// 首个探针样本可能先于首次链路探测；须等到真实握手结果。
		for attempt := 0; attempt < 3; attempt++ {
			row = advanceExitProbe(t, r, id)
			if row["last_probe"] == "success" {
				break
			}
		}
		if row["state"] != "healthy" || row["last_probe"] != "success" {
			t.Fatalf("轮换后新材料的健康探测未通过: %v", row)
		}
	}
	r.links[0].block(true)
	tcpExchange(t, r.port, "1:")
}

// 旧载体排空和新载体并存仍共用节点/规则业务预算，释放旧流后须回收载体。
func TestTunnelRotationDrainingCarrierKeepsResourceLimits(t *testing.T) {
	f := newAuthorizationFixtureWithLimits(t, forwarding.ResourceLimits{MaxConnectionsPerRule: 2, MaxTunnelStreams: 2})
	f.create(t)
	old := tcpExchange(t, f.port, "T:")
	f.c.Do("POST", "/api/nodes/"+f.exitID+"/tunnel/rotation", map[string]string{"kind": "both"}, 202)
	awaitTunnelRotation(t, &f, "complete")
	current := tcpExchange(t, f.port, "T:")
	assertTCPFailure(t, f.port)
	assertRotationConnection(t, old)
	assertRotationConnection(t, current)
	old.(*net.TCPConn).SetLinger(0)
	old.Close()
	limits := &tunnelLimitsFixture{h: f.h, c: f.c}
	sampleTunnelBusiness(t, limits, f.entryID, 1)
	sampleTunnelBusiness(t, limits, f.exitID, 1)
	// 仅剩当前载体，旧载体不能因引用未归还而永久驻留。
	for _, id := range []string{f.entryID, f.exitID} {
		v := f.c.Do("GET", probePath(id), nil, 200)["realtime"].(map[string]any)
		if v["tunnel_connections"] != float64(1) {
			t.Fatalf("轮换旧载体未回收: %v", v)
		}
	}
	tcpExchange(t, f.port, "T:")
	assertTCPFailure(t, f.port)
}
