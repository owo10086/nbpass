package clusterharness_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
)

// The external port is a real TCP proxy into B's WSS listener. Only B can dial
// the tunnel's target; A's resolver must resolve the public domain itself.
func TestIXAddressDomainMappedPortAndOldDialerIsolation(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "A"), addNode(c, "B")
	aid, bid := a["id"].(string), b["id"].(string)
	reserve, listen := directPorts(t)
	bport := reserve()
	proxy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	mapped := proxy.Addr().(*net.TCPAddr).Port
	var forwarded atomic.Int32
	go func() {
		for {
			incoming, err := proxy.Accept()
			if err != nil {
				return
			}
			go func() {
				defer incoming.Close()
				outgoing, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(bport)))
				if err != nil {
					return
				}
				defer outgoing.Close()
				forwarded.Add(1)
				done := make(chan struct{})
				go func() { io.Copy(outgoing, incoming); close(done) }()
				io.Copy(incoming, outgoing)
				outgoing.Close()
				incoming.Close()
				<-done
			}()
		}
	}()
	var mu sync.Mutex
	latest := map[string]*controlpb.ConfigSnapshot{}
	h.BeforeControlSend(func(_ context.Context, id string, message *controlpb.PanelMessage) error {
		if message.Snapshot != nil {
			mu.Lock()
			latest[id] = proto.Clone(message.Snapshot).(*controlpb.ConfigSnapshot)
			mu.Unlock()
		}
		return nil
	})
	directTarget := echoTarget(t, "direct:")
	var lookups atomic.Int32
	an := h.StartNode(a["installation_ticket"].(string), listen, func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "public-b.test" {
				return nil, errors.New("A must not resolve target")
			}
			lookups.Add(1)
			return []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("127.0.0.1")}, nil
		}
		d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
			if address != net.JoinHostPort("127.0.0.1", strconv.Itoa(directTarget)) {
				return nil, errors.New("A cannot dial tunnel target")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	})
	bn := h.StartNode(b["installation_ticket"].(string), listen)
	awaitNode(t, c, aid, connected)
	awaitNode(t, c, bid, connected)
	saved := c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": "public-b.test", "listen_port": bport, "public_port": mapped, "transport": "wss"}, 200)
	group := c.Do("POST", "/api/exit-groups", map[string]any{"name": "B", "members": []string{bid}, "strategy": "primary_backup"}, 201)["id"]
	port := reserve()
	body := directBody(entryGroup(c, aid)["id"].(string), port, "127.0.0.1", echoTarget(t, "tunnel:"))
	body["kind"], body["exit_group_id"] = "tunnel", group
	rule := c.Do("POST", "/api/rules", body, 201)
	awaitRule(t, c, rule["id"].(string), "正常")
	tcpExchange(t, port, "tunnel:")
	if lookups.Load() == 0 || forwarded.Load() == 0 {
		t.Fatal("未经过前驱解析及真实 NAT 代理")
	}
	mu.Lock()
	as, bs := proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot), proto.Clone(latest[bid]).(*controlpb.ConfigSnapshot)
	mu.Unlock()
	ep := as.ExitGroups[0].Exits[0]
	if ep.Port != uint32(mapped) || bs.TunnelServer.ListenPort != uint32(bport) || ep.ServerName != saved["server_name"] || ep.EndpointConfigDigest == "" || ep.AddressId != "public" {
		t.Fatal("映射改变监听、TLS 身份或当前节点未收到公网地址身份")
	}
	directPort := reserve()
	directRule := c.Do("POST", "/api/rules", directBody(entryGroup(c, aid)["id"].(string), directPort, "127.0.0.1", directTarget), 201)
	awaitRule(t, c, directRule["id"].(string), "正常")
	tcpExchange(t, directPort, "direct:")
	// Same-version offline cache retains the domain, mapping and TLS identity.
	bn.Disconnect()
	an.Disconnect()
	an.Restart()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("离线缓存未恢复业务监听", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	tcpExchange(t, port, "tunnel:")
	an.Stop()
	bn.Restore()
	bn.Restart()
	awaitNode(t, c, bid, connected)
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		if r.Compatibility != nil {
			r.Compatibility.Capabilities = slices.DeleteFunc(r.Compatibility.Capabilities, func(cap string) bool { return cap == compatibility.IXAddressDialing })
		}
	})
	an.Restore()
	an.Restart()
	awaitNode(t, c, aid, connected)
	awaitNode(t, c, bid, connected)
	awaitRule(t, c, rule["id"].(string), "整条阻塞（需升级服务器）")
	awaitRule(t, c, directRule["id"].(string), "正常")
	tcpExchange(t, directPort, "direct:")
	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		as = proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot)
		bs = proto.Clone(latest[bid]).(*controlpb.ConfigSnapshot)
		mu.Unlock()
		if len(bs.TunnelServer.Grants) == 0 && len(bs.TunnelServer.Peers) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("出口未收到撤销孤立授权的快照")
		}
		h.Clock.Advance(time.Second)
		time.Sleep(5 * time.Millisecond)
	}
	if len(as.ExitGroups) != 0 || len(as.Rules) != 1 || as.Rules[0].Id != directRule["id"] || len(bs.TunnelServer.Grants) != 0 || len(bs.TunnelServer.Peers) != 0 {
		t.Fatalf("旧声明收到新语义或孤立凭据: groups=%d rules=%d grants=%d peers=%d", len(as.ExitGroups), len(as.Rules), len(bs.TunnelServer.Grants), len(bs.TunnelServer.Peers))
	}
	assertTCPFailure(t, port)
}

func TestIXAddressRotationPreservesDedicatedPortsAndIdentity(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "automatic"}[automatic], func(t *testing.T) {
			r := newExitBlockedRig(t, 0)
			f := r.f
			var before map[string]any
			for _, raw := range f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any) {
				v := raw.(map[string]any)
				if v["node_id"] == f.id {
					before = v
				}
			}
			saved := f.c.Do("PUT", "/api/nodes/"+f.id+"/tunnel", map[string]any{"address": before["address"], "listen_port": before["listen_port"], "public_port": 2443, "transport": "wss", "dedicated_addresses": []any{map[string]any{"name": "沪日 IX", "address": "10.0.0.2", "public_port": 3443}, map[string]any{"name": "内网域名", "address": "ix.test"}}}, 200)
			if automatic {
				r.enable(t)
				r.sites.mode.Store(2)
				f.ip.Store("8.8.8.8")
			} else {
				f.c.Do("POST", f.path+"/actions", nil, 202)
				awaitRotation(t, f.c, f.path, "observing")
				f.ip.Store("8.8.8.8")
			}
			advanceHealthyRotation(t, f, time.Minute)
			awaitRotation(t, f.c, f.path, "changed")
			assertSaved := func() {
				t.Helper()
				for _, raw := range f.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any) {
					v := raw.(map[string]any)
					if v["node_id"] != f.id {
						continue
					}
					if v["address"] != "8.8.8.8" {
						t.Fatal("公网未更新", v)
					}
					for _, key := range []string{"dedicated_addresses", "public_port", "listen_port", "certificate_pem", "server_name"} {
						if !reflect.DeepEqual(v[key], saved[key]) {
							t.Fatal("换公网 IP 改变专线、端口或身份", key, v)
						}
					}
				}
			}
			assertSaved()
			if err := f.h.Restart(); err != nil {
				t.Fatal(err)
			}
			f.c = f.h.Client()
			login(f.c)
			assertSaved()
		})
	}
}
