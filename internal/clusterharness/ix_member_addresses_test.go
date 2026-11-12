package clusterharness_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/clusterharness"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
)

func ixMappedProxy(t *testing.T, target int) (int, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	count := &atomic.Int32{}
	go func() {
		for {
			incoming, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer incoming.Close()
				outgoing, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(target)))
				if err != nil {
					return
				}
				defer outgoing.Close()
				count.Add(1)
				done := make(chan struct{})
				go func() { io.Copy(outgoing, incoming); close(done) }()
				io.Copy(incoming, outgoing)
				outgoing.Close()
				incoming.Close()
				<-done
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, count
}

func ixMemberHealth(c *clusterharness.Client, entry, exit, address string) map[string]any {
	for _, raw := range c.Do("GET", "/api/nodes/"+entry+"/exit-health", nil, 200)["exits"].([]any) {
		h := raw.(map[string]any)
		id, _ := h["address_id"].(string)
		if id == "" {
			id = "public"
		}
		if h["exit_id"] == exit && id == address {
			return h
		}
	}
	return nil
}

func awaitIXMemberEvidence(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, condition func() bool) {
	t.Helper()
	for range 40 {
		awaitControlIdle(t, h)
		h.Clock.Advance(time.Second)
		deadline := time.Now().Add(40 * time.Millisecond)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("未收到预期的地址健康/就绪证据")
}

func TestIXMemberAddressesRealTrafficIsolationRotationLateDigestAndRestart(t *testing.T) {
	rig := newExitBlockedRig(t, 0)
	f := rig.f
	h, c, bid := f.h, f.c, f.id
	var saved map[string]any
	for _, raw := range c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any) {
		v := raw.(map[string]any)
		if v["node_id"] == bid {
			saved = v
		}
	}
	bport := int(saved["listen_port"].(float64))
	mapped, forwarded := ixMappedProxy(t, bport)
	saved = c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": saved["address"], "listen_port": bport, "transport": "wss", "dedicated_addresses": []any{map[string]any{"name": "沪日 IX", "address": "ix-b.test", "public_port": mapped}}}, 200)
	dedicated := saved["dedicated_addresses"].([]any)[0].(map[string]any)
	ix := dedicated["id"].(string)
	reserve, listen := directPorts(t)
	a := addNode(c, "双地址入口 A")
	aid := a["id"].(string)
	link := &exitLink{}
	var mu sync.Mutex
	dials := map[string]int{}
	latest := map[string]*controlpb.ConfigSnapshot{}
	h.BeforeControlSend(func(_ context.Context, id string, m *controlpb.PanelMessage) error {
		if m.Snapshot != nil {
			mu.Lock()
			latest[id] = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
			mu.Unlock()
		}
		return nil
	})
	options := func(cfg *node.Config, d *node.Dependencies) {
		listen(cfg, d)
		d.Forwarding.LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "ix-b.test" {
				return nil, errors.New("入口不能解析目标")
			}
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		d.Forwarding.DialTarget = func(context.Context, string) (net.Conn, error) { return nil, errors.New("入口禁止直拨目标") }
		d.Forwarding.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
			mu.Lock()
			dials[address]++
			mu.Unlock()
			host, port, _ := net.SplitHostPort(address)
			if port == strconv.Itoa(bport) {
				if host != f.ip.Load().(string) {
					return nil, errors.New("旧公网地址")
				}
				return link.dial(ctx, net.JoinHostPort("127.0.0.1", port))
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	}
	an := h.StartNode(a["installation_ticket"].(string), options)
	awaitNode(t, c, aid, connected)
	var groups, entryGroups, rules []string
	var ports []int
	for _, id := range []string{"public", ix} {
		g := c.Do("POST", "/api/exit-groups", map[string]any{"name": id, "strategy": "primary_backup", "member_addresses": []any{map[string]any{"node_id": bid, "address_id": id}}}, 201)["id"].(string)
		eg := entryGroup(c, aid)["id"].(string)
		port := reserve()
		body := directBody(eg, port, "127.0.0.1", 12345)
		body["kind"], body["exit_group_id"] = "tunnel", g
		rule := c.Do("POST", "/api/rules", body, 201)["id"].(string)
		awaitRule(t, c, rule, "正常")
		tcpExchange(t, port, "出口:")
		groups = append(groups, g)
		entryGroups = append(entryGroups, eg)
		rules = append(rules, rule)
		ports = append(ports, port)
	}
	oldIX := tcpExchange(t, ports[1], "出口:")
	mu.Lock()
	publicDials, ixDials := dials[net.JoinHostPort(f.ip.Load().(string), strconv.Itoa(bport))], dials[net.JoinHostPort("127.0.0.1", strconv.Itoa(mapped))]
	mu.Unlock()
	if publicDials == 0 || ixDials == 0 || forwarded.Load() == 0 {
		t.Fatal("没有走实际公网拨号及专线映射", publicDials, ixDials, forwarded.Load())
	}
	link.block(true)
	assertTCPFailure(t, ports[0])
	tcpExchange(t, ports[1], "出口:")
	awaitIXMemberEvidence(t, h, c, func() bool {
		p, i := ixMemberHealth(c, aid, bid, "public"), ixMemberHealth(c, aid, bid, ix)
		return p != nil && p["last_probe"] == "failure" && i != nil && i["last_probe"] == "success"
	})
	fr := &failoverRig{h: h, c: c, entryID: aid}
	if row := sampleTunnelEntryState(t, fr, entryGroups[0]); row["ready"] != false {
		t.Fatal("好专线让坏公网就绪", row)
	}
	if row := entryState(t, c, entryGroups[1], aid); row["ready"] != true {
		t.Fatal("公网坏污染专线", row)
	}
	// A dedicated-only consumer must never become a public-IP confirmation participant.
	d := addNode(c, "仅专线 D")
	did := d["id"].(string)
	h.StartNode(d["installation_ticket"].(string), options)
	awaitNode(t, c, did, connected)
	body := directBody(entryGroup(c, did)["id"].(string), reserve(), "127.0.0.1", 12345)
	body["kind"], body["exit_group_id"] = "tunnel", groups[1]
	drule := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, c, drule, "正常")
	beforeHealth := ixMemberHealth(c, aid, bid, ix)
	rig.sites.mode.Store(2)
	c.Do("POST", f.path+"/actions", nil, 202)
	awaitRotation(t, c, f.path, "observing")
	f.ip.Store("8.8.8.8")
	advanceHealthyRotation(t, f, time.Minute)
	awaitRotation(t, c, f.path, "changed")
	progress := c.Do("GET", rig.path, nil, 200)["entries"].([]any)
	if len(progress) != 1 || progress[0].(map[string]any)["node_id"] != aid {
		t.Fatal("公网换址等待了专线消费者", progress)
	}
	oldIX.SetDeadline(time.Now().Add(time.Second))
	oldIX.Write([]byte("ping"))
	data := make([]byte, len("出口:ping"))
	if _, err := io.ReadFull(oldIX, data); err != nil || string(data) != "出口:ping" {
		t.Fatal("公网换址中断专线已有会话", string(data), err)
	}
	tcpExchange(t, ports[1], "出口:")
	if current := ixMemberHealth(c, aid, bid, ix); current == nil || current["endpoint_config_digest"] != beforeHealth["endpoint_config_digest"] || current["address_version"] != nil {
		t.Fatal("换公网替换专线健康", current)
	}
	mu.Lock()
	snapshot := proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot)
	mu.Unlock()
	var old *controlpb.TunnelEndpoint
	for _, g := range snapshot.ExitGroups {
		if g.Id == groups[1] {
			old = proto.Clone(g.Exits[0]).(*controlpb.TunnelEndpoint)
		}
	}
	// Same stable address ID, different mapped port: late success is fresh in time but obsolete in content.
	newMapped, _ := ixMappedProxy(t, bport)
	h.TransformControlReports(func(report *controlpb.NodeReport) {
		if report.Probe != nil {
			for _, health := range report.Probe.ExitHealth {
				if health.ExitId == bid && health.AddressId == ix {
					health.EndpointConfigDigest = old.EndpointConfigDigest
					health.LastProbe = "success"
					health.CheckedAtUnix = h.Clock.Now().Unix()
				}
			}
		}
	})
	dedicated["public_port"] = newMapped
	c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": "8.8.8.8", "listen_port": bport, "transport": "wss", "dedicated_addresses": []any{dedicated}}, 200)
	awaitRule(t, c, rules[1], "正常")
	row := sampleTunnelEntryState(t, fr, entryGroups[1])
	if row["ready"] != false || ixMemberHealth(c, aid, bid, ix) != nil {
		t.Fatal("迟到旧摘要使当前专线就绪", row)
	}
	h.TransformControlReports(nil)
	awaitIXMemberEvidence(t, h, c, func() bool {
		i := ixMemberHealth(c, aid, bid, ix)
		return i != nil && i["endpoint_config_digest"] != old.EndpointConfigDigest && i["last_probe"] == "success"
	})
	if row = sampleTunnelEntryState(t, fr, entryGroups[1]); row["ready"] != true {
		t.Fatal("新摘要握手未使专线就绪", row)
	}
	// Restore the real nodes from their caches while control is disconnected.
	an.Disconnect()
	f.n.Disconnect()
	f.n.Restart()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(bport)), 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("缓存未恢复出口监听", err)
		}
		time.Sleep(time.Millisecond)
	}
	an.Restart()
	deadline = time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(ports[1])), 50*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("缓存未恢复专线监听", err)
		}
		time.Sleep(time.Millisecond)
	}
	tcpExchange(t, ports[1], "出口:")
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	f.c = c
	listed := c.Do("GET", "/api/exit-groups", nil, 200)["exit_groups"].([]any)
	for _, raw := range listed {
		g := raw.(map[string]any)
		if g["id"] == groups[1] {
			m := g["member_addresses"].([]any)[0].(map[string]any)
			if m["address_id"] != ix {
				t.Fatal("重启丢失地址选择", g)
			}
		}
	}
	tcpExchange(t, ports[1], "出口:")
}
