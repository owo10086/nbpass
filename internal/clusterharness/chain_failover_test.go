package clusterharness_test

import (
	"context"
	"errors"
	"fmt"
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
	"nbpass/internal/panel"
	"nbpass/internal/rotation"
)

type chainRig struct {
	h                  *clusterharness.Harness
	c                  *clusterharness.Client
	dns                *clusterharness.DNSPod
	ids                []string
	nodes              []*clusterharness.Node
	links              [][]*exitLink
	ports              []int
	addresses          []string
	sites              []*domesticSites
	mapped             []int
	groups             []string
	entryPorts         []atomic.Int64
	targetCalls        []atomic.Int32
	entries            int
	layers             [][]int
	group, chain, rule string
	body               map[string]any
	mu                 sync.Mutex
	latest             map[string]*controlpb.ConfigSnapshot
	rotationCalls      atomic.Int32
}

func newChainRig(t *testing.T, entries int, sizes []int, mode string, options ...func(int, *node.Dependencies)) *chainRig {
	t.Helper()
	r := &chainRig{entries: entries, dns: clusterharness.NewDNSPod(), latest: map[string]*controlpb.ConfigSnapshot{}}
	r.h, r.c = telegramBrowser(t, &fakeTelegram{}, func(d *panel.Dependencies) { d.DNSProvider = r.dns })
	r.h.BeforeControlSend(func(_ context.Context, id string, m *controlpb.PanelMessage) error {
		if m.Snapshot != nil {
			r.mu.Lock()
			r.latest[id] = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
			r.mu.Unlock()
		}
		return nil
	})
	count := entries
	for _, size := range sizes {
		layer := []int{}
		for j := 0; j < size; j++ {
			layer = append(layer, count)
			count++
		}
		r.layers = append(r.layers, layer)
	}
	r.ports = make([]int, count)
	r.addresses = make([]string, count)
	r.mapped = make([]int, count)
	r.entryPorts = make([]atomic.Int64, entries)
	r.targetCalls = make([]atomic.Int32, count)
	r.links = make([][]*exitLink, count)
	portOwner := map[string]int{}
	for i := 0; i < count; i++ {
		r.ports[i] = r.h.ReserveTCPPort()
		portOwner[strconv.Itoa(r.ports[i])] = i
		if i >= entries {
			r.mapped[i], _ = ixMappedProxy(t, r.ports[i])
			portOwner[strconv.Itoa(r.mapped[i])] = i
		}
		r.links[i] = make([]*exitLink, count)
		for j := 0; j < count; j++ {
			r.links[i][j] = &exitLink{}
		}
	}
	businessPort := r.h.ReserveTCPPort()
	target := echoTarget(t, "chain:")
	terminal := map[int]bool{}
	for _, i := range r.layers[len(r.layers)-1] {
		terminal[i] = true
	}
	for i := 0; i < count; i++ {
		created := addNode(r.c, fmt.Sprint("chain-node-", i))
		id := created["id"].(string)
		r.ids = append(r.ids, id)
		sites := &domesticSites{}
		sites.mode.Store(2)
		r.sites = append(r.sites, sites)
		n := r.h.StartNode(created["installation_ticket"].(string), sites.dependencies, func(_ *node.Config, d *node.Dependencies) {
			if i < entries {
				d.PublicIPv4 = func(context.Context) (string, error) { return []string{"8.8.8.8", "1.1.1.1"}[i], nil }
				d.RotationIPv4Sources = [2]func(context.Context) (string, error){d.PublicIPv4, d.PublicIPv4}
				d.RotationLocalIPv4 = func(context.Context) ([]string, error) {
					ip, e := d.PublicIPv4(context.Background())
					return []string{ip}, e
				}
				d.RotationHTTP = func(context.Context, rotation.Config, rotation.Variables) error { r.rotationCalls.Add(1); return nil }
				listen := d.Forwarding.Listen
				d.Forwarding.Listen = func(network, address string) (net.Listener, error) {
					_, port, _ := net.SplitHostPort(address)
					if port == strconv.Itoa(businessPort) {
						l, e := net.Listen(network, "127.0.0.1:0")
						if e == nil {
							r.entryPorts[i].Store(int64(l.Addr().(*net.TCPAddr).Port))
						}
						return l, e
					}
					return listen(network, address)
				}
			}
			d.Forwarding.LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
				for j := entries; j < count; j++ {
					if host == fmt.Sprintf("ix-node-%d.test", j) {
						return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
					}
				}
				if host == "chain-target.test" && terminal[i] {
					return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
				}
				return nil, errors.New("only terminal resolves target")
			}
			d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
				if !terminal[i] {
					return nil, errors.New("no target access before terminal")
				}
				r.targetCalls[i].Add(1)
				return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
			}
			d.Forwarding.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(address)
				j, ok := portOwner[port]
				if !ok {
					return nil, errors.New("unconfigured next hop")
				}
				return r.links[i][j].dial(ctx, address)
			}
			for _, option := range options {
				option(i, d)
			}
		})
		r.nodes = append(r.nodes, n)
		awaitNode(t, r.c, id, connected)
		if i < entries {
			r.c.Do("PUT", "/api/nodes/"+id+"/self-check", map[string]bool{"enabled": true}, 204)
			awaitSelfCheck(t, r.c, id, 1, "normal", 0)
		}
		if i >= entries {
			r.c.Do("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": fmt.Sprintf("ix-node-%d.test", i), "listen_port": r.ports[i], "public_port": r.mapped[i], "transport": "wss"}, 200)
			r.addresses[i] = "public"
		}
	}
	groups := []string{}
	for i, layer := range r.layers {
		members := []map[string]string{}
		for _, index := range layer {
			members = append(members, map[string]string{"node_id": r.ids[index], "address_id": r.addresses[index]})
		}
		groups = append(groups, r.c.Do("POST", "/api/exit-groups", map[string]any{"name": fmt.Sprint("layer", i), "member_addresses": members, "strategy": "primary_backup"}, 201)["id"].(string))
	}
	groupBody := map[string]any{"name": "chain entries", "members": r.ids[:entries]}
	if mode == "all" {
		groupBody["publication_mode"] = "all"
	}
	r.groups = groups
	r.group = r.c.Do("POST", "/api/entry-groups", groupBody, 201)["id"].(string)
	r.chain = r.c.Do("POST", "/api/forward-chains", map[string]any{"name": "chain", "exit_group_ids": groups}, 201)["id"].(string)
	r.body = directBody(r.group, businessPort, "chain-target.test", target)
	r.body["kind"] = "tunnel"
	r.body["chain_id"] = r.chain
	r.rule = r.c.Do("POST", "/api/rules", r.body, 201)["id"].(string)
	awaitRule(t, r.c, r.rule, "正常")
	for _, id := range r.ids {
		awaitUptime(t, r.c, id, 0)
	}
	return r
}
func (r *chainRig) advance(t *testing.T, seconds int) {
	t.Helper()
	for i := 0; i < seconds; i++ {
		awaitControlIdle(t, r.h)
		r.h.Clock.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
}
func (r *chainRig) ready(t *testing.T, want ...bool) {
	t.Helper()
	for i := 0; i < 110; i++ {
		r.advance(t, 1)
		matches := true
		for index, value := range want {
			if entryState(t, r.c, r.group, r.ids[index])["ready"] != value {
				matches = false
			}
		}
		if matches {
			return
		}
	}
	for index := range want {
		t.Log(entryState(t, r.c, r.group, r.ids[index]))
	}
	t.Fatal("chain readiness did not converge", want)
}
func (r *chainRig) exchange(t *testing.T, entry int) net.Conn {
	t.Helper()
	return tcpExchange(t, int(r.entryPorts[entry].Load()), "chain:")
}
func (r *chainRig) operation(t *testing.T) map[string]any {
	t.Helper()
	for _, raw := range r.c.Do("GET", "/api/overview", nil, 200)["rules"].([]any) {
		v := raw.(map[string]any)
		if v["id"] == r.rule {
			return v
		}
	}
	t.Fatal("missing operations rule")
	return nil
}

func (r *chainRig) awaitOperation(t *testing.T, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	var view map[string]any
	for i := 0; i < 35; i++ {
		r.advance(t, 1)
		deadline := time.Now().Add(30 * time.Millisecond)
		for time.Now().Before(deadline) {
			view = r.operation(t)
			if predicate(view) {
				return view
			}
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("operation evidence did not converge", view)
	return nil
}

func TestChainFailoverAsymmetricEdgesAndRecovery(t *testing.T) {
	r := newChainRig(t, 1, []int{2, 2}, "primary_backup")
	r.ready(t, true)
	r.exchange(t, 0)
	for _, terminal := range r.layers[1] {
		r.links[1][terminal].block(true)
	}
	for i := 0; i < 3; i++ {
		r.exchange(t, 0)
	}
	r.advance(t, 15)
	r.ready(t, true)
	op := r.awaitOperation(t, func(v map[string]any) bool {
		for _, raw := range v["chain_edges"].([]any) {
			e := raw.(map[string]any)
			if e["from_node_id"] == r.ids[0] && e["to_node_id"] == r.ids[1] {
				return e["local_ready"] == true && e["suffix_ready"] == false
			}
		}
		return false
	})
	found := false
	for _, raw := range op["chain_edges"].([]any) {
		e := raw.(map[string]any)
		if e["from_node_id"] == r.ids[0] && e["to_node_id"] == r.ids[1] {
			found = true
			if e["local_ready"] != true || e["suffix_ready"] != false {
				t.Fatal("downstream failure polluted first edge", e)
			}
		}
	}
	if !found {
		t.Fatal("missing first edge", op)
	}
	view := r.awaitChain(t, func(v map[string]any) bool {
		return chainEdgeRow(t, v, r.rule, r.ids[0], r.ids[1])["state"] == "suffix_unknown"
	})
	r.assertChainContract(t, view, r.rule)
	first := chainEdgeRow(t, view, r.rule, r.ids[0], r.ids[1])
	backup := chainEdgeRow(t, view, r.rule, r.ids[0], r.ids[2])
	if first["fresh"] != true || backup["state"] != "ready" || backup["fresh"] != true {
		t.Fatal("management page conflated reachable edge/downstream failure with backup readiness", first, backup)
	}
	for _, terminal := range r.layers[1] {
		if e := chainEdgeRow(t, view, r.rule, r.ids[1], r.ids[terminal]); e["state"] != "edge_unhealthy" || e["fresh"] != true {
			t.Fatal("management page lost actual downstream bad edge", e)
		}
	}
	portPath := ruleRow(t, r.c, r.rule)["path"].(map[string]any)
	for _, raw := range portPath["edges"].([]any) {
		edge := raw.(map[string]any)
		if edge["from_node_id"] == r.ids[0] && edge["to_node_id"] == r.ids[1] && (edge["state"] != "suffix_unknown" || edge["local_ready"] != true || edge["suffix_ready"] != false || edge["fresh"] != true) {
			t.Fatal("port conflated healthy local edge and broken suffix", edge)
		}
		if edge["from_node_id"] == r.ids[1] && (edge["state"] != "edge_unhealthy" || edge["fresh"] != true) {
			t.Fatal("port lost actual bad downstream edge", edge)
		}
		if edge["from_node_id"] == r.ids[0] && edge["to_node_id"] == r.ids[2] && (edge["state"] != "ready" || edge["fresh"] != true) {
			t.Fatal("port lost independent backup suffix readiness", edge)
		}
	}
	for _, terminal := range r.layers[1] {
		r.links[2][terminal].block(true)
	}
	assertTCPFailure(t, int(r.entryPorts[0].Load()))
	r.ready(t, false)
	for _, relay := range r.layers[0] {
		for _, terminal := range r.layers[1] {
			r.links[relay][terminal].recover()
		}
	}
	r.ready(t, true)
	r.exchange(t, 0)
	for _, i := range []int{0, 1, 2} {
		if r.targetCalls[i].Load() != 0 {
			t.Fatal("nonterminal dialed target", i)
		}
	}
}

func TestChainFailoverEachHopCandidateRecoveryAndTargetClassification(t *testing.T) {
	var targetFailure atomic.Bool
	r := newChainRig(t, 1, []int{2, 2}, "primary_backup", func(i int, d *node.Dependencies) {
		if i >= 3 {
			dial := d.Forwarding.DialTarget
			d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
				if targetFailure.Load() {
					return nil, errors.New("target unavailable")
				}
				return dial(ctx, address)
			}
		}
	})
	for _, candidate := range []int{1, 2, 3, 4} {
		predecessors := []int{0}
		if candidate >= 3 {
			predecessors = []int{1, 2}
		}
		for _, from := range predecessors {
			r.links[from][candidate].block(true)
		}
		r.advance(t, 15)
		r.ready(t, true)
		r.exchange(t, 0)
		failedView := r.awaitChain(t, func(v map[string]any) bool {
			for _, from := range predecessors {
				e := chainEdgeRow(t, v, r.rule, r.ids[from], r.ids[candidate])
				if e["state"] != "edge_unhealthy" || e["fresh"] != true {
					return false
				}
			}
			return true
		})
		r.assertChainContract(t, failedView, r.rule)
		for _, from := range predecessors {
			r.links[from][candidate].recover()
		}
		r.advance(t, 75)
		r.ready(t, true)
		r.exchange(t, 0)
		r.awaitOperation(t, func(v map[string]any) bool {
			for _, raw := range v["chain_edges"].([]any) {
				e := raw.(map[string]any)
				if e["to_node_id"] == r.ids[candidate] && (e["local_ready"] != true || e["fresh"] != true) {
					return false
				}
			}
			return true
		})
		recoveredView := r.awaitChain(t, func(v map[string]any) bool {
			for _, from := range predecessors {
				e := chainEdgeRow(t, v, r.rule, r.ids[from], r.ids[candidate])
				if e["state"] != "ready" || e["fresh"] != true {
					return false
				}
			}
			return true
		})
		r.assertChainContract(t, recoveredView, r.rule)
	}
	// Start the independent target-failure phase only after every current edge
	// has recovered. Keep the exact endpoint/generation identities for the
	// post-failure assertion, so stale or replacement samples cannot pass it.
	healthy := func(v map[string]any) bool {
		edges := v["chain_edges"].([]any)
		if len(edges) != 6 {
			return false
		}
		for _, raw := range edges {
			e := raw.(map[string]any)
			if e["local_ready"] != true || e["fresh"] != true {
				return false
			}
		}
		return true
	}
	before := r.awaitOperation(t, healthy)
	r.assertChainContract(t, forwardChainRow(t, r.c, r.chain), r.rule)
	edgeIdentity := func(e map[string]any) string {
		return fmt.Sprint(e["from_node_id"], e["to_node_id"], e["address_id"], e["endpoint_config_digest"], e["rule_generation"], e["grant_generation"], e["chain_fingerprint"])
	}
	expected := map[string]bool{}
	for _, raw := range before["chain_edges"].([]any) {
		expected[edgeIdentity(raw.(map[string]any))] = true
	}
	targetFailure.Store(true)
	assertTCPFailure(t, int(r.entryPorts[0].Load()))
	r.advance(t, 10)
	after := r.awaitOperation(t, func(v map[string]any) bool {
		if !healthy(v) {
			return false
		}
		for _, raw := range v["chain_edges"].([]any) {
			if !expected[edgeIdentity(raw.(map[string]any))] {
				return false
			}
		}
		for _, raw := range v["reasons"].([]any) {
			reason := raw.(map[string]any)
			if failure, ok := reason["failure"].(map[string]any); ok && failure["Class"] == "exit_target" {
				return true
			}
		}
		return false
	})
	found := false
	for _, raw := range after["reasons"].([]any) {
		reason := raw.(map[string]any)
		if failure, ok := reason["failure"].(map[string]any); ok && failure["Class"] == "exit_target" {
			found = true
		}
	}
	if !found {
		t.Fatal("target error lost its distinct failure category", r.operation(t))
	}
	for _, raw := range after["chain_edges"].([]any) {
		e := raw.(map[string]any)
		if e["local_ready"] != true || e["fresh"] != true || !expected[edgeIdentity(e)] {
			t.Fatal("target failure poisoned previous physical edge", e)
		}
	}
	view := forwardChainRow(t, r.c, r.chain)
	r.assertChainContract(t, view, r.rule)
	for _, raw := range view["hops"].([]any) {
		for _, raw := range raw.(map[string]any)["observations"].([]any) {
			if e := raw.(map[string]any); e["state"] != "ready" || e["fresh"] != true {
				t.Fatal("management GET confused final target failure with chain readiness", e)
			}
		}
	}
	targetFailure.Store(false)
	r.exchange(t, 0)
	r.advance(t, 10)
}
