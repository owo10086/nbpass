package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
)

type authorizationFixture struct {
	h               *clusterharness.Harness
	c               *clusterharness.Client
	entryID, exitID string
	entry, exit     *clusterharness.Node
	entryCache      node.ConfigStore
	port            int
	listeners       *atomic.Int32
	body            map[string]any
}

func newAuthorizationFixture(t *testing.T, options ...func(*node.Config, *node.Dependencies)) authorizationFixture {
	t.Helper()
	h, c := nodeBrowser(t)
	e, x := addNode(c, "授权入口"), addNode(c, "授权出口")
	f := authorizationFixture{h: h, c: c, entryID: e["id"].(string), exitID: x["id"].(string), port: h.ReserveTCPPort(), listeners: &atomic.Int32{}}
	f.entry = h.StartNode(e["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		f.entryCache = d.Store.(node.ConfigStore)
		d.Forwarding.Listen = func(network, address string) (net.Listener, error) {
			listener, err := h.ListenTCP(network, address)
			if err != nil {
				return nil, err
			}
			f.listeners.Add(1)
			return &authorizationListener{Listener: listener, active: f.listeners}, nil
		}
	})
	f.exit = h.StartNode(x["installation_ticket"].(string), options...)
	awaitNode(t, c, f.entryID, connected)
	awaitNode(t, c, f.exitID, connected)
	c.Do("PUT", "/api/nodes/"+f.exitID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
	awaitRevision(t, c, f.exitID, 0)
	xg := c.Do("POST", "/api/exit-groups", map[string]any{"name": "授权出口组", "members": []string{f.exitID}, "strategy": "primary_backup"}, 201)
	f.body = directBody(entryGroup(c, f.entryID)["id"].(string), f.port, "127.0.0.1", echoTarget(t, "T:"))
	f.body["kind"], f.body["exit_group_id"] = "tunnel", xg["id"]
	return f
}

func (f *authorizationFixture) create(t *testing.T) string {
	t.Helper()
	r := f.c.Do("POST", "/api/rules", f.body, 201)
	f.body["id"] = r["id"]
	awaitRevision(t, f.c, f.exitID, 0)
	awaitRule(t, f.c, r["id"].(string), "正常")
	return r["id"].(string)
}

// 预留 socket 仍由测试台持有；核对实际交给节点的监听器数，不能把
// 预留 socket 的 TCP 握手误认为节点已经启用入口。关闭也必须实际完成。
type authorizationListener struct {
	net.Listener
	active *atomic.Int32
	once   sync.Once
}

func (l *authorizationListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { l.active.Add(-1) })
	return err
}
func assertNoAuthorizationListener(t *testing.T, f authorizationFixture) {
	t.Helper()
	if n := f.listeners.Load(); n != 0 {
		t.Fatalf("出口尚未确认授权，入口仍持有 %d 个监听器", n)
	}
}

// 若把已发送快照当成应用确认，新规则和新代次会提前监听。
func TestTunnelAuthorizationWaitsForDurableExitConfirmation(t *testing.T) {
	store := &faultyConfigStore{}
	f := newAuthorizationFixture(t, func(_ *node.Config, d *node.Dependencies) {
		store.ConfigStore = d.Store.(node.ConfigStore)
		d.ConfigStore = store
	})
	store.fail.Store(true)
	r := f.c.Do("POST", "/api/rules", f.body, 201)
	awaitNode(t, f.c, f.exitID, func(v map[string]any) bool { return v["config_status"] == "同步失败" })
	// 等入口处理完当前快照，再检查实际交接的监听器；TCP 交换失败不足以证明门禁。
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["desired_revision"] == v["applied_revision"] })
	assertNoAuthorizationListener(t, f)
	store.fail.Store(false)
	f.h.Clock.Advance(10 * time.Second)
	awaitRule(t, f.c, r["id"].(string), "正常")
	conn := tcpExchange(t, f.port, "T:")
	store.fail.Store(true)
	f.body["target_port"] = echoTarget(t, "B:")
	f.c.Do("PUT", "/api/rules/"+r["id"].(string), f.body, 200)
	awaitNode(t, f.c, f.exitID, func(v map[string]any) bool { return v["config_status"] == "同步失败" })
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["desired_revision"] == v["applied_revision"] })
	assertNoAuthorizationListener(t, f)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("变化前的入口流未撤销")
	}
	store.fail.Store(false)
	f.h.Clock.Advance(10 * time.Second)
	awaitRule(t, f.c, r["id"].(string), "正常")
	tcpExchange(t, f.port, "B:")
}

// 入口失联仍持有缓存，只有出口逐流复查能撤销已有流并拒绝新流。
func TestTunnelAuthorizationRevokedAtOnlineExitWhileEntryOffline(t *testing.T) {
	for _, action := range []string{"delete", "disable"} {
		t.Run(action, func(t *testing.T) {
			f := newAuthorizationFixture(t)
			id := f.create(t)
			conn := tcpExchange(t, f.port, "T:")
			f.entry.Disconnect()
			awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
			previous := nodeRow(t, f.c, f.exitID)["desired_revision"].(float64)
			if action == "delete" {
				f.c.Do("DELETE", "/api/rules/"+id, nil, 204)
			} else {
				f.body["enabled"] = false
				f.c.Do("PUT", "/api/rules/"+id, f.body, 200)
			}
			awaitRevision(t, f.c, f.exitID, previous)
			f.h.Clock.Advance(time.Second)
			conn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
			if _, err := conn.Read(make([]byte, 1)); err == nil {
				t.Fatal("出口撤权后已有流仍存活")
			} else if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("一秒授权复查未断开已有流")
			}
			assertTCPFailure(t, f.port)
			// 离线重启恢复缓存也不能绕过在线出口的撤销。
			f.entry.Restart()
			assertTCPFailure(t, f.port)
			f.entry.Restore()
			for i := 0; i < 5; i++ {
				f.h.Clock.Advance(time.Second)
				time.Sleep(10 * time.Millisecond)
			}
			awaitNode(t, f.c, f.entryID, connected)
			awaitRevision(t, f.c, f.entryID, 0)
			assertNoAuthorizationListener(t, f)
		})
	}
}

// 无关规则和名称改动会改变节点 revision，不能撤销原规则授权或重建原流。
func TestTunnelAuthorizationSurvivesUnrelatedConfigAndPanelRestart(t *testing.T) {
	f := newAuthorizationFixture(t)
	id := f.create(t)
	conn := tcpExchange(t, f.port, "T:")
	previous := nodeRow(t, f.c, f.exitID)["desired_revision"].(float64)
	f.c.Do("POST", "/api/rules", directBody(f.body["entry_group_id"].(string), f.h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "D:")), 201)
	f.body["name"] = "只改名称"
	f.c.Do("PUT", "/api/rules/"+id, f.body, 200)
	awaitRule(t, f.c, id, "正常")
	if nodeRow(t, f.c, f.exitID)["desired_revision"] != previous {
		t.Fatal("改名或入口直连规则改变了出口授权快照")
	}
	// 让出口自身 revision 变化，原有流仍须通过授权复查。
	exitGroup := entryGroup(f.c, f.exitID)["id"].(string)
	f.c.Do("POST", "/api/rules", directBody(exitGroup, f.h.ReserveTCPPort(), "127.0.0.1", echoTarget(t, "D:")), 201)
	awaitRevision(t, f.c, f.exitID, previous)
	f.h.Clock.Advance(time.Second)
	if err := f.h.Restart(); err != nil {
		t.Fatal(err)
	}
	f.c = f.h.Client()
	login(f.c)
	for i := 0; i < 5; i++ {
		f.h.Clock.Advance(time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	awaitNode(t, f.c, f.entryID, connected)
	awaitNode(t, f.c, f.exitID, connected)
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte("ping"))
	data := make([]byte, 6)
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "T:ping" {
		t.Fatalf("无关变更或面板重启断开已有流: %q %v", data, err)
	}
	tcpExchange(t, f.port, "T:")
}

type forgedAuthorizationCache struct{ data []byte }

func (s *forgedAuthorizationCache) LoadConfig(context.Context) ([]byte, error) { return s.data, nil }
func (s *forgedAuthorizationCache) SaveConfig(_ context.Context, data []byte) error {
	s.data = append([]byte{}, data...)
	return nil
}

// 驱动真实节点从被篡改的本机缓存启动；不读取面板内部状态，不替换数据面。
// 若出口只信任节点对凭据或规则 ID，篡改目标/身份和旧代次重放就能成功交换 TCP 数据。
func TestTunnelCompromisedEntryCannotChangeAuthorization(t *testing.T) {
	f := newAuthorizationFixture(t)
	id := f.create(t)
	tcpExchange(t, f.port, "T:")
	data, err := f.entryCache.LoadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Version    int              `json:"version"`
		NodeID     string           `json:"node_id"`
		ControlURL string           `json:"control_url"`
		State      forwarding.State `json:"state"`
	}
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"target", "entry", "generation"} {
		t.Run(mutation, func(t *testing.T) {
			var cache = original
			// JSON 深拷贝，避免篡改下一场景的原始缓存。
			if err := json.Unmarshal(data, &cache); err != nil {
				t.Fatal(err)
			}
			port := f.h.ReserveTCPPort()
			cache.State.Rules[0].ListenPort = uint32(port)
			switch mutation {
			case "target":
				cache.State.Rules[0].TargetPort = uint32(echoTarget(t, "X:"))
			case "entry":
				cache.State.ExitGroups[0].Exits[0].EntryId = "冒用的其他入口"
			case "generation":
				// 保持目标不变，停用再启用必须换代；旧缓存也不能复用旧授权。
				f.body["enabled"] = false
				f.c.Do("PUT", "/api/rules/"+id, f.body, 200)
				awaitRule(t, f.c, id, "正常")
				f.body["enabled"] = true
				f.c.Do("PUT", "/api/rules/"+id, f.body, 200)
				awaitRule(t, f.c, id, "正常")
			}
			encoded, err := json.Marshal(cache)
			if err != nil {
				t.Fatal(err)
			}
			// 克隆自身凭据模拟被盗入口；控制连接阻断，防止合法快照覆盖篡改缓存。
			listening := make(chan struct{}, 1)
			rogue := f.h.CloneNode(f.entry, "", func(_ *node.Config, d *node.Dependencies) {
				d.Forwarding.Listen = func(network, address string) (net.Listener, error) {
					listener, err := f.h.ListenTCP(network, address)
					if err == nil {
						listening <- struct{}{}
					}
					return listener, err
				}
				d.ConfigStore = &forgedAuthorizationCache{data: encoded}
				d.DialContext = func(context.Context, string) (net.Conn, error) { return nil, errors.New("被盗入口仅持有缓存") }
			})
			defer rogue.Stop()
			// 等待节点真正接管预留监听器，不能把预留端口当作缓存已恢复。
			select {
			case <-listening:
			case <-time.After(2 * time.Second):
				t.Fatal("篡改缓存的真实节点未恢复监听")
			}
			assertTCPFailure(t, port)
		})
	}
	tcpExchange(t, f.port, "T:")
}

// 同一载体上的另一条规则仍获授权，撤销不能连带关闭它的业务流。
func TestTunnelRevocationKeepsOtherRuleOnSharedSession(t *testing.T) {
	f := newAuthorizationFixture(t)
	id := f.create(t)
	first := tcpExchange(t, f.port, "T:")
	other := map[string]any{}
	for key, value := range f.body {
		if key != "id" {
			other[key] = value
		}
	}
	otherPort := f.h.ReserveTCPPort()
	other["listen_port"] = otherPort
	r := f.c.Do("POST", "/api/rules", other, 201)
	awaitRule(t, f.c, r["id"].(string), "正常")
	second := tcpExchange(t, otherPort, "T:")
	f.entry.Disconnect()
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
	previous := nodeRow(t, f.c, f.exitID)["desired_revision"].(float64)
	f.c.Do("DELETE", "/api/rules/"+id, nil, 204)
	awaitRevision(t, f.c, f.exitID, previous)
	f.h.Clock.Advance(time.Second)
	first.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("被撤销的共享会话流仍存活")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("被撤销的共享会话流未在复查后结束")
	}
	second.SetDeadline(time.Now().Add(time.Second))
	second.Write([]byte("ping"))
	data := make([]byte, 6)
	if _, err := io.ReadFull(second, data); err != nil || string(data) != "T:ping" {
		t.Fatalf("撤销连带断开其他规则: %q %v", data, err)
	}
	tcpExchange(t, otherPort, "T:")
}

// 即便两个控制连接均长期离线，缓存授权也没有按时间失效的租约。
func TestTunnelAuthorizationDoesNotExpireWhileBothNodesOffline(t *testing.T) {
	f := newAuthorizationFixture(t)
	f.create(t)
	conn := tcpExchange(t, f.port, "T:")
	f.entry.Disconnect()
	f.exit.Disconnect()
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
	awaitNode(t, f.c, f.exitID, func(v map[string]any) bool { return v["connected"] == false })
	f.h.Clock.Advance(48 * time.Hour)
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte("ping"))
	data := make([]byte, 6)
	if _, err := io.ReadFull(conn, data); err != nil || string(data) != "T:ping" {
		t.Fatalf("离线缓存授权错误到期: %q %v", data, err)
	}
	tcpExchange(t, f.port, "T:")
}

// 出口正在拨号也属于已有业务流，撤权必须取消它，不能等拨号成功后交付。
func TestTunnelAuthorizationRevocationCancelsPendingTargetDial(t *testing.T) {
	started, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	f := newAuthorizationFixture(t, func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.DialTarget = func(ctx context.Context, _ string) (net.Conn, error) {
			started <- struct{}{}
			<-ctx.Done()
			cancelled <- struct{}{}
			return nil, ctx.Err()
		}
	})
	id := f.create(t)
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("ping"))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("真实隧道流未进入目标拨号")
	}
	f.entry.Disconnect()
	awaitNode(t, f.c, f.entryID, func(v map[string]any) bool { return v["connected"] == false })
	previous := nodeRow(t, f.c, f.exitID)["desired_revision"].(float64)
	f.c.Do("DELETE", "/api/rules/"+id, nil, 204)
	awaitRevision(t, f.c, f.exitID, previous)
	f.h.Clock.Advance(time.Second)
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("一秒复查未取消已撤权流的目标拨号")
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("已撤权的拨号向客户端交付业务")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("已撤权的拨号未结束客户端连接")
	}
}
