package clusterharness_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func observedSourceTarget(t *testing.T) (int, <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	seen := make(chan string, 64)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			seen <- c.RemoteAddr().(*net.TCPAddr).IP.String()
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, seen
}
func outboundExchange(t *testing.T, port int, seen <-chan string, want string) {
	t.Helper()
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = c.Write([]byte("source")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	if _, err = io.ReadFull(c, b); err != nil || string(b) != "source" {
		t.Fatal("real data failed", string(b), err)
	}
	select {
	case ip := <-seen:
		if ip != want {
			t.Fatal("target observed wrong source", ip, want)
		}
	case <-time.After(time.Second):
		t.Fatal("no target observation")
	}
}
func TestOutboundIPv4DirectTunnelErrorsRestartAndCapabilityIsolation(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "A"), addNode(c, "B")
	aid, bid := a["id"].(string), b["id"].(string)
	reserve, listen := directPorts(t)
	target, seen := observedSourceTarget(t)
	// Use an actually enumerated loopback IPv4; no network configuration is changed.
	source := "127.0.0.1"
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	local := []netip.Addr{}
	for _, address := range interfaces {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().Is4() {
			local = append(local, prefix.Addr())
		}
	}
	if !slices.Contains(local, netip.MustParseAddr(source)) {
		t.Fatal("loopback source not assigned")
	}
	absent := netip.MustParseAddr("192.0.2.253")
	if slices.Contains(local, absent) {
		t.Fatal("fixture source unexpectedly local")
	}
	var mu sync.Mutex
	latest := map[string]*controlpb.ConfigSnapshot{}
	h.BeforeControlSend(func(_ context.Context, id string, m *controlpb.PanelMessage) error {
		if m.Snapshot != nil {
			mu.Lock()
			latest[id] = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
			mu.Unlock()
		}
		return nil
	})
	var tunnelDials atomic.Int32
	var allowUnbound atomic.Bool
	options := func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
			tunnelDials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
			if !allowUnbound.Load() {
				t.Error("configured source reached old unbound seam")
				return nil, errors.New("unbound target forbidden")
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}

	}
	an := h.StartNode(a["installation_ticket"].(string), listen, options)
	bn := h.StartNode(b["installation_ticket"].(string), listen, options)
	awaitNode(t, c, aid, connected)
	awaitNode(t, c, bid, connected)
	c.Do("PUT", "/api/nodes/"+aid+"/outbound", map[string]string{"outbound_ipv4": source}, 200)
	c.Do("PUT", "/api/nodes/"+bid+"/outbound", map[string]string{"outbound_ipv4": source}, 200)
	directPort := reserve()
	direct := directBody("", directPort, "127.0.0.1", target)
	direct["entry_node_id"] = aid
	dr := c.Do("POST", "/api/rules", direct, 201)
	awaitRule(t, c, dr["id"].(string), "正常")
	outboundExchange(t, directPort, seen, source)
	bport := reserve()
	c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": bport, "transport": "wss"}, 200)
	group := c.Do("POST", "/api/exit-groups", map[string]any{"name": "B", "members": []string{bid}, "strategy": "primary_backup"}, 201)["id"]
	tunnelPort := reserve()
	body := directBody("", tunnelPort, "127.0.0.1", target)
	body["entry_node_id"], body["kind"], body["exit_group_id"] = aid, "tunnel", group
	tr := c.Do("POST", "/api/rules", body, 201)
	awaitRule(t, c, tr["id"].(string), "正常")
	outboundExchange(t, tunnelPort, seen, source)
	if tunnelDials.Load() == 0 {
		t.Fatal("missing real WSS/final connections")
	}
	t.Logf("target observed selected source=%s; next-hop WSS/probes used separate unbound DialTunnel", source)
	c.Do("PUT", "/api/nodes/"+bid+"/outbound", map[string]string{"outbound_ipv4": absent.String()}, 200)
	awaitNode(t, c, bid, func(v map[string]any) bool { return strings.Contains(v["apply_error"].(string), "出站 IP") })
	view := awaitRule(t, c, tr["id"].(string), "同步失败")
	reason := view["sync_members"].([]any)[0].(map[string]any)["apply_error"].(string)
	if !strings.Contains(reason, "出站 IP") || !strings.Contains(reason, "B") {
		t.Fatal("lost exit source ownership", reason)
	}
	assertTCPFailure(t, tunnelPort)
	select {
	case ip := <-seen:
		t.Fatal("invalid source fell back", ip)
	default:
	}
	outboundExchange(t, directPort, seen, source)
	allowUnbound.Store(true)
	c.Do("PUT", "/api/nodes/"+bid+"/outbound", map[string]string{"outbound_ipv4": ""}, 200)
	awaitNode(t, c, bid, func(v map[string]any) bool { return v["config_status"] == "正常" })
	awaitRule(t, c, tr["id"].(string), "正常")
	outboundExchange(t, tunnelPort, seen, "127.0.0.1")
	c.Do("PUT", "/api/nodes/"+bid+"/outbound", map[string]string{"outbound_ipv4": source}, 200)
	awaitNode(t, c, bid, func(v map[string]any) bool { return v["config_status"] == "正常" })
	// An invalid source on transport-only A cannot bind or disable its next-hop WSS/probe path.
	c.Do("PUT", "/api/nodes/"+aid+"/outbound", map[string]string{"outbound_ipv4": absent.String()}, 200)
	awaitNode(t, c, aid, func(v map[string]any) bool { return strings.Contains(v["apply_error"].(string), "出站 IP") })
	assertTCPFailure(t, directPort)
	// Restart tears down the old carrier so this must establish fresh WSS/probe connections while A's source is invalid.
	awaitNode(t, c, aid, connected)
	previousInstance := nodeRow(t, c, aid)["process_instance"]
	beforeTunnel := tunnelDials.Load()
	an.Restart()
	awaitNode(t, c, aid, func(v map[string]any) bool { return connected(v) && v["process_instance"] != previousInstance })
	outboundExchange(t, tunnelPort, seen, source)
	if tunnelDials.Load() <= beforeTunnel {
		t.Fatal("business did not establish fresh unbound next-hop WSS")
	}
	c.Do("PUT", "/api/nodes/"+aid+"/outbound", map[string]string{"outbound_ipv4": source}, 200)
	awaitNode(t, c, aid, func(v map[string]any) bool { return v["config_status"] == "正常" })
	allowUnbound.Store(false)
	bn.Disconnect()
	an.Disconnect()
	bn.Restart()
	// Wait for the real B listener before restoring A's offline cached precheck.
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(bport)), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	an.Restart()
	deadline = time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(tunnelPort)), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	outboundExchange(t, directPort, seen, source)
	outboundExchange(t, tunnelPort, seen, source)
	// Old A still carries the unrelated tunnel, but cannot directly dial with a missing source capability.
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		if r.NodeId == aid && r.Compatibility != nil {
			r.Compatibility.Capabilities = slices.DeleteFunc(r.Compatibility.Capabilities, func(cap string) bool { return cap == compatibility.OutboundIPv4 })
		}
	})
	bn.Restore()
	bn.Restart()
	an.Restore()
	an.Restart()
	awaitNode(t, c, aid, connected)
	awaitRule(t, c, dr["id"].(string), "整条阻塞（需升级服务器）")
	awaitRule(t, c, tr["id"].(string), "正常")
	outboundExchange(t, tunnelPort, seen, source)
	mu.Lock()
	as := proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot)
	mu.Unlock()
	if as.OutboundIpv4 != "" || len(as.Rules) != 1 || as.Rules[0].Id != tr["id"] {
		t.Fatal("old node received source semantics", as)
	}
	// Old B's final grants must also disappear; no unbound forwarding survives omission of the field.
	h.TransformControlReports(func(r *controlpb.NodeReport) {
		if r.Compatibility != nil {
			r.Compatibility.Capabilities = slices.DeleteFunc(r.Compatibility.Capabilities, func(cap string) bool { return cap == compatibility.OutboundIPv4 })
		}
	})
	bn.Restart()
	awaitRule(t, c, tr["id"].(string), "整条阻塞（需升级服务器）")
	deadline = time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		bs := latest[bid]
		safe := bs != nil && bs.OutboundIpv4 == "" && len(bs.GetTunnelServer().GetGrants()) == 0 && len(bs.GetTunnelServer().GetPeers()) == 0
		mu.Unlock()
		if safe {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("old exit received orphan grants")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Use the complete historical executable for the manual recovery boundary, in
// addition to node's four real-reader encrypted-cache cases. No new node with a
// removed declaration stands in for the rollback target here.
func TestOutboundIPv4ActualV1ManualIsolationAndReconnect(t *testing.T) {
	const commit = "3899928362a75b58ac74ff6844652d319760b656"
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	archive := exec.Command("git", "archive", commit, "api", "internal", "cmd", "go.mod", "go.sum")
	archive.Dir = root
	data, err := archive.Output()
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	extract := exec.Command("tar", "-xf", "-", "-C", source)
	extract.Stdin = bytes.NewReader(data)
	if out, err := extract.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	binary := filepath.Join(source, "nbpass-node-state-v1")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, "./cmd/nbpass-node")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	binaryData, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binaryData)
	t.Logf("actual rollback executable source=%s sha256=%x", commit, sum)
	h, c := nodeBrowser(t)
	created := addNode(c, "真实旧读取器恢复")
	id := created["id"].(string)
	// A real TCP pass-through can deny control without changing credentials or URL.
	controlPort, err := strconv.Atoi(strings.Split(h.Control.Listener.Addr().String(), ":")[1])
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	var denied atomic.Bool
	go func() {
		for {
			incoming, err := proxy.Accept()
			if err != nil {
				return
			}
			go func() {
				defer incoming.Close()
				if denied.Load() {
					return
				}
				outgoing, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)))
				if err != nil {
					return
				}
				defer outgoing.Close()
				done := make(chan struct{})
				go func() { io.Copy(outgoing, incoming); close(done) }()
				io.Copy(incoming, outgoing)
				incoming.Close()
				outgoing.Close()
				<-done
			}()
		}
	}()
	controlURL := "https://" + proxy.Addr().String()
	dir := filepath.Join(t.TempDir(), "node")
	store := node.FileStore{Dir: dir, Random: rand.Reader}
	reserve, listen := directPorts(t)
	current := h.StartNode(created["installation_ticket"].(string), listen, func(config *node.Config, d *node.Dependencies) { config.ControlURL = controlURL; d.Store = store })
	awaitNode(t, c, id, connected)
	target, seen := observedSourceTarget(t)
	port := reserve()
	body := directBody("", port, "127.0.0.1", target)
	body["entry_node_id"] = id
	rule := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	c.Do("PUT", "/api/nodes/"+id+"/outbound", map[string]string{"outbound_ipv4": "127.0.0.1"}, 200)
	awaitRule(t, c, rule, "正常")
	outboundExchange(t, port, seen, "127.0.0.1")
	current.Stop()
	denied.Store(true)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.Control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	command := func() *exec.Cmd {
		return exec.Command(binary, "--control-url", controlURL, "--data-dir", dir, "--ca-file", ca)
	}
	rejected := command()
	out, err := rejected.CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("节点配置缓存格式或身份不匹配")) {
		t.Fatal("actual old executable failed to reject", string(out), err)
	}
	assertTCPFailure(t, port)
	select {
	case ip := <-seen:
		t.Fatal("old process connected target", ip)
	default:
	}
	t.Logf("actual old process exit=%v output=%s; no listener or target connection", err, strings.TrimSpace(string(out)))
	original, err := os.ReadFile(filepath.Join(dir, "config.enc"))
	if err != nil {
		t.Fatal(err)
	}
	isolated := filepath.Join(dir, "config.enc.isolated")
	if err = os.Rename(filepath.Join(dir, "config.enc"), isolated); err != nil {
		t.Fatal(err)
	}
	start := func() func() {
		t.Helper()
		cmd := command()
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cmd.Process.Signal(os.Interrupt)
				cmd.Wait()
				if t.Failed() {
					t.Log(output.String())
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	stopOffline := start()
	time.Sleep(150 * time.Millisecond)
	assertTCPFailure(t, port)
	select {
	case ip := <-seen:
		t.Fatal("empty offline process connected target", ip)
	default:
	}
	stopOffline()
	preserved, err := os.ReadFile(isolated)
	if err != nil || !bytes.Equal(original, preserved) {
		t.Fatal("isolation changed evidence", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "config.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("offline startup wrote fallback cache", err)
	}
	var mu sync.Mutex
	var latest *controlpb.ConfigSnapshot
	h.BeforeControlSend(func(_ context.Context, nodeID string, m *controlpb.PanelMessage) error {
		if nodeID == id && m.Snapshot != nil {
			mu.Lock()
			latest = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
			mu.Unlock()
		}
		return nil
	})
	// The same real old process is a possible entry, relay, and terminal in
	// current chain rules. Reconnection must not deliver any new chain semantics.
	other := []string{addNode(c, "回滚前驱")["id"].(string), addNode(c, "回滚末跳")["id"].(string)}
	groups := map[string]string{}
	for _, nodeID := range append([]string{id}, other...) {
		c.Do("PUT", "/api/nodes/"+nodeID+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": reserve(), "transport": "wss"}, 200)
		groups[nodeID] = c.Do("POST", "/api/exit-groups", map[string]any{"name": "回滚组" + nodeID, "members": []string{nodeID}, "strategy": "primary_backup"}, 201)["id"].(string)
	}
	chainRules := []string{}
	for index, path := range [][]string{{id, other[0], other[1]}, {other[0], id, other[1]}, {other[0], other[1], id}} {
		chain := c.Do("POST", "/api/forward-chains", map[string]any{"name": fmt.Sprintf("回滚位置%d", index), "exit_group_ids": []string{groups[path[1]], groups[path[2]]}}, 201)
		body := directBody("", reserve(), "127.0.0.1", target)
		body["entry_node_id"] = path[0]
		body["kind"] = "tunnel"
		body["chain_id"] = chain["id"]
		chainRules = append(chainRules, c.Do("POST", "/api/rules", body, 201)["id"].(string))
	}
	denied.Store(false)
	stopOnline := start()
	defer stopOnline()
	awaitNode(t, c, id, connected)
	awaitRule(t, c, rule, "整条阻塞（需升级服务器）")
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		safe := latest != nil && latest.OutboundIpv4 == "" && len(latest.Rules) == 0
		mu.Unlock()
		if safe {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual old node did not receive filtered current snapshot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	assertTCPFailure(t, port)
	// Clearing the source is an explicit current configuration change; old direct forwarding can then resume.
	c.Do("PUT", "/api/nodes/"+id+"/outbound", map[string]string{"outbound_ipv4": ""}, 200)
	awaitRule(t, c, rule, "正常")
	outboundExchange(t, port, seen, "127.0.0.1")
	for _, rid := range chainRules {
		awaitRule(t, c, rid, "整条阻塞（需升级服务器）")
	}
	mu.Lock()
	filtered := latest != nil && len(latest.Rules) == 1 && latest.Rules[0].Id == rule && latest.Rules[0].ChainId == "" && len(latest.GetTunnelServer().GetGrants()) == 0 && len(latest.ExitGroups) == 0
	mu.Unlock()
	if !filtered {
		t.Fatal("real old reconnect received chain entry/relay/terminal semantics")
	}
	t.Log("real old reconnect filtered chain entry, relay and terminal grants; compatible current direct rule remained usable")
	t.Log("manual isolation: offline empty process stayed stopped; reconnect filtered source rule; explicit clear restored only current compatible rule")
}

func TestOutboundIPv4SameSnapshotRevalidationFailureIsVisible(t *testing.T) {
	h, c := nodeBrowser(t)
	a, b := addNode(c, "重新核验A"), addNode(c, "重新核验B")
	aid, bid := a["id"].(string), b["id"].(string)
	var lostA, lostB atomic.Bool
	reserve, listen := directPorts(t)
	for _, item := range []struct {
		created map[string]any
		lost    *atomic.Bool
	}{{a, &lostA}, {b, &lostB}} {
		lost := item.lost
		h.StartNode(item.created["installation_ticket"].(string), listen, func(_ *node.Config, d *node.Dependencies) {
			d.Forwarding.LocalIPv4 = func() ([]netip.Addr, error) {
				if lost.Load() {
					return nil, nil
				}
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}
		})
		awaitNode(t, c, item.created["id"].(string), connected)
	}
	for _, id := range []string{aid, bid} {
		c.Do("PUT", "/api/nodes/"+id+"/outbound", map[string]string{"outbound_ipv4": "127.0.0.1"}, 200)
	}
	target, seen := observedSourceTarget(t)
	directPort := reserve()
	body := directBody("", directPort, "127.0.0.1", target)
	body["entry_node_id"] = aid
	direct := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": reserve(), "transport": "wss"}, 200)
	group := c.Do("POST", "/api/exit-groups", map[string]any{"name": "B", "members": []string{bid}, "strategy": "primary_backup"}, 201)["id"]
	tunnelPort := reserve()
	tunnelBody := directBody("", tunnelPort, "127.0.0.1", target)
	tunnelBody["entry_node_id"], tunnelBody["kind"], tunnelBody["exit_group_id"] = aid, "tunnel", group
	tunnel := c.Do("POST", "/api/rules", tunnelBody, 201)["id"].(string)
	awaitRule(t, c, direct, "正常")
	awaitRule(t, c, tunnel, "正常")
	outboundExchange(t, directPort, seen, "127.0.0.1")
	outboundExchange(t, tunnelPort, seen, "127.0.0.1")
	revisionA, revisionB := nodeRow(t, c, aid)["desired_revision"], nodeRow(t, c, bid)["desired_revision"]
	resend := func() {
		for range 7 {
			h.Clock.Advance(10 * time.Second)
			time.Sleep(100 * time.Millisecond)
		}
	}
	lostA.Store(true)
	resend()
	awaitNode(t, c, aid, func(v map[string]any) bool { return strings.Contains(v["apply_error"].(string), "出站 IP") })
	awaitRule(t, c, direct, "同步失败")
	assertTCPFailure(t, directPort)
	outboundExchange(t, tunnelPort, seen, "127.0.0.1")
	lostB.Store(true)
	resend()
	awaitNode(t, c, bid, func(v map[string]any) bool { return strings.Contains(v["apply_error"].(string), "出站 IP") })
	row := awaitRule(t, c, tunnel, "同步失败")
	if !strings.Contains(row["sync_members"].([]any)[0].(map[string]any)["apply_error"].(string), "重新核验B") {
		t.Fatal("lost final source ownership", row)
	}
	assertTCPFailure(t, tunnelPort)
	if nodeRow(t, c, aid)["desired_revision"] != revisionA || nodeRow(t, c, bid)["desired_revision"] != revisionB {
		t.Fatal("revalidation spuriously changed desired revision")
	}
	lostA.Store(false)
	lostB.Store(false)
	resend()
	awaitRule(t, c, direct, "正常")
	awaitNode(t, c, bid, func(v map[string]any) bool { return v["config_status"] == "正常" })
	awaitRule(t, c, tunnel, "正常")
	outboundExchange(t, directPort, seen, "127.0.0.1")
	outboundExchange(t, tunnelPort, seen, "127.0.0.1")
}

// A is a single release: obtain format 1 from the unchanged actual old process,
// re-enroll the same identity using the actual current executable, then restore
// while control is unavailable. No cache version or State is synthesized.
func TestIXSameIdentityActualV1ToV2Reinstallation(t *testing.T) {
	const commit = "3899928362a75b58ac74ff6844652d319760b656"
	ctx := context.Background()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	archive := exec.Command("git", "archive", commit, "api", "internal", "cmd", "go.mod", "go.sum")
	archive.Dir = root
	data, err := archive.Output()
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	extract := exec.Command("tar", "-xf", "-", "-C", source)
	extract.Stdin = bytes.NewReader(data)
	if out, err := extract.CombinedOutput(); err != nil {
		t.Fatal(string(out), err)
	}
	oldBinary, newBinary := filepath.Join(source, "old-node"), filepath.Join(t.TempDir(), "new-node")
	for _, build := range []struct{ dir, path string }{{source, oldBinary}, {root, newBinary}} {
		command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", build.path, "./cmd/nbpass-node")
		command.Dir = build.dir
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	oldBytes, err := os.ReadFile(oldBinary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual historical executable source=%s sha256=%x", commit, sha256.Sum256(oldBytes))
	h, c := nodeBrowser(t)
	created := addNode(c, "跨格式同身份")
	id := created["id"].(string)
	dir := filepath.Join(t.TempDir(), "node")
	store := node.FileStore{Dir: dir, Random: rand.Reader}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.Control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	// The control address/identity stays unchanged. A physical TCP proxy is
	// closed only after both the old process and enrollment have terminated.
	proxy, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	var denied atomic.Bool
	var deniedConnections atomic.Int32
	go func() {
		for {
			incoming, err := proxy.Accept()
			if err != nil {
				return
			}
			go func() {
				defer incoming.Close()
				if denied.Load() {
					deniedConnections.Add(1)
					return
				}
				outgoing, err := net.Dial("tcp4", h.Control.Listener.Addr().String())
				if err != nil {
					return
				}
				defer outgoing.Close()
				done := make(chan struct{})
				go func() { io.Copy(outgoing, incoming); close(done) }()
				io.Copy(incoming, outgoing)
				incoming.Close()
				outgoing.Close()
				<-done
			}()
		}
	}()
	controlURL := "https://" + proxy.Addr().String()
	command := func(binary string, extra ...string) *exec.Cmd {
		return exec.Command(binary, append([]string{"--control-url", controlURL, "--data-dir", dir, "--ca-file", ca}, extra...)...)
	}
	enroll := func(binary, ticket string) {
		t.Helper()
		if out, err := command(binary, "--ticket", ticket, "--enroll-only").CombinedOutput(); err != nil {
			t.Fatal("enrollment", string(out), err)
		}
	}
	start := func(binary string) func() {
		t.Helper()
		cmd := command(binary)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				cmd.Process.Signal(os.Interrupt)
				err := cmd.Wait()
				if err != nil {
					t.Errorf("process stop: %v %s", err, output.String())
				}
			})
		}
		t.Cleanup(stop)
		return stop
	}
	enroll(oldBinary, created["installation_ticket"].(string))
	stopOld := start(oldBinary)
	awaitNode(t, c, id, connected)
	reserve := func() int {
		t.Helper()
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return port
	}
	target, seen := observedSourceTarget(t)
	port, revokedPort := reserve(), reserve()
	body := directBody("", port, "127.0.0.1", target)
	body["entry_node_id"] = id
	rule := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	body = directBody("", revokedPort, "127.0.0.1", target)
	body["entry_node_id"] = id
	revoked := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, c, rule, "正常")
	awaitRule(t, c, revoked, "正常")
	outboundExchange(t, port, seen, "127.0.0.1")
	outboundExchange(t, revokedPort, seen, "127.0.0.1")
	c.Do("DELETE", "/api/rules/"+revoked, nil, 204)
	awaitNode(t, c, id, func(row map[string]any) bool { return row["desired_revision"] == row["applied_revision"] })
	stopOld()
	type cache struct {
		Version    int              `json:"version"`
		NodeID     string           `json:"node_id"`
		ControlURL string           `json:"control_url"`
		State      forwarding.State `json:"state"`
	}
	readCache := func() cache {
		t.Helper()
		raw, err := store.LoadConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var v cache
		if err = json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := readCache()
	if before.Version != 1 || before.NodeID != id || before.ControlURL != controlURL || len(before.State.Rules) != 1 || before.State.Rules[0].Id != rule || before.State.HighestRevision == 0 {
		t.Fatal("old actual cache/revocation boundary", before.Version, before.State.HighestRevision)
	}
	oldCredentials, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := node.OpenEventQueue(ctx, store, h.Clock, rand.Reader, node.EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err = queue.Record(ctx, "sync_failed", "同身份重装必须保留的事件", before.State.HighestRevision); err != nil {
		t.Fatal(err)
	}
	preserved := map[string][]byte{}
	for _, name := range []string{"node.key", "config.enc", "events.enc", "traffic.enc", "rotation.enc", "self-check.enc"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		preserved[name] = raw
	}
	reset := c.Do("POST", "/api/nodes/"+id+"/token/reset", nil, 200)
	enroll(newBinary, reset["installation_ticket"].(string))
	for name, raw := range preserved {
		after, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(raw, after) {
			t.Fatal("same identity enrollment altered", name, err)
		}
	}
	newCredentials, err := store.Load(ctx)
	if err != nil || newCredentials.NodeID != oldCredentials.NodeID || newCredentials.ControlURL != oldCredentials.ControlURL || newCredentials.Token == oldCredentials.Token {
		t.Fatal("fresh same-identity credential missing", err)
	}
	if out, err := command(newBinary, "--ticket", reset["installation_ticket"].(string), "--enroll-only").CombinedOutput(); err == nil {
		t.Fatal("consumed ticket reused", string(out))
	}
	denied.Store(true)
	stopNew := start(newBinary)
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := store.LoadConfig(ctx)
		var v cache
		if err == nil && json.Unmarshal(raw, &v) == nil && v.Version == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual new offline restore did not write format 2")
		}
		time.Sleep(time.Millisecond)
	}
	// A denied control dial proves Run passed rotation/self-check/traffic
	// startup; cache persistence alone is earlier and cannot be a stop barrier.
	for deniedConnections.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("offline process did not finish local initialization")
		}
		time.Sleep(time.Millisecond)
	}
	outboundExchange(t, port, seen, "127.0.0.1")
	assertTCPFailure(t, revokedPort)
	stopNew()
	after := readCache()
	if after.State.HighestRevision != before.State.HighestRevision || after.State.HighestDigest != before.State.HighestDigest || after.State.AppliedRevision != before.State.AppliedRevision || len(after.State.Rules) != 1 || after.State.Rules[0].Id != rule {
		t.Fatal("format migration changed high-water/applied/revocation state")
	}
	key, err := os.ReadFile(filepath.Join(dir, "node.key"))
	if err != nil || !bytes.Equal(key, preserved["node.key"]) {
		t.Fatal("key changed", err)
	}
	events, err := store.LoadEvents(ctx)
	if err != nil || !bytes.Contains(events, []byte("同身份重装必须保留的事件")) {
		t.Fatal("same identity event lost", err)
	}
	if out, err := command(oldBinary).CombinedOutput(); err == nil || !bytes.Contains(out, []byte("节点配置缓存格式或身份不匹配")) {
		t.Fatal("actual old executable accepted converted cache", string(out), err)
	}
	assertTCPFailure(t, port)
	assertTCPFailure(t, revokedPort)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config-") || strings.HasPrefix(entry.Name(), "config-v1") {
			t.Fatal("left fallback/temporary configuration", entry.Name())
		}
	}
	t.Logf("actual old SHA256=%x; same identity reset/enroll preserved %d state files; offline 1->2 retained revision %d and only current rule; revoked listener absent and actual old rejects 2", sha256.Sum256(oldBytes), len(preserved), after.State.HighestRevision)
}
