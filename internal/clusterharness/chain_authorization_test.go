package clusterharness_test

import (
	"context"
	"io"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
	"net"
	"net/netip"
	"testing"
	"time"
)

func chainStreamExchange(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetDeadline(time.Now().Add(time.Second))
	if _, e := c.Write([]byte("ping")); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 10)
	if _, e := io.ReadFull(c, b); e != nil || string(b) != "chain:ping" {
		t.Fatal("cached or rotating chain stream failed", string(b), e)
	}
}
func chainStreamClosed(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("ping"))
	b := make([]byte, 10)
	if _, e := io.ReadFull(c, b); e == nil {
		t.Fatal("revoked existing stream still forwards")
	}
}
func (r *chainRig) restore(t *testing.T, index int) {
	t.Helper()
	r.nodes[index].Restore()
	for i := 0; i < 20; i++ {
		r.advance(t, 1)
		if connected(nodeRow(t, r.c, r.ids[index])) {
			return
		}
	}
	t.Fatal("node did not reconnect", index)
}
func (r *chainRig) snapshot(index int) *controlpb.ConfigSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latest[r.ids[index]]
}

func TestChainAuthorizationOfflineRestartAndDownstreamRevocation(t *testing.T) {
	for _, offline := range []int{0, 1} {
		t.Run([]string{"entry", "relay"}[offline], func(t *testing.T) {
			r := newChainRig(t, 1, []int{1, 1}, "primary_backup")
			r.exchange(t, 0)
			for i, n := range r.nodes {
				n.Disconnect()
				awaitNode(t, r.c, r.ids[i], func(v map[string]any) bool { return v["connected"] == false })
			}
			r.entryPorts[0].Store(0)
			for i := len(r.nodes) - 1; i >= 0; i-- {
				r.nodes[i].Restart()
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if r.entryPorts[0].Load() > 0 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			// A new process restores the same persisted complete chain while all control
			// sockets remain denied. No grant acquires a wall-clock expiry.
			for i, n := range r.nodes {
				select {
				case err := <-n.Done:
					t.Fatalf("restart node %d: %v", i, err)
				default:
				}
			}
			r.advance(t, 3)
			existing := r.exchange(t, 0)
			for i := range r.nodes {
				if i != offline {
					r.restore(t, i)
				}
			}
			before := map[int]float64{}
			for i := range r.nodes {
				if i != offline {
					before[i] = nodeRow(t, r.c, r.ids[i])["desired_revision"].(float64)
				}
			}
			r.body["enabled"] = false
			r.c.Do("PUT", "/api/rules/"+r.rule, r.body, 200)
			// Reconnection only establishes a control socket. The safety contract starts
			// after the online endpoint has persisted the newer revoked authorization.
			// Wait for that exact report without advancing business time; a blocked
			// queue/config write must fail this barrier, not be hidden by extra ticks.
			for i, previous := range before {
				awaitNode(t, r.c, r.ids[i], func(row map[string]any) bool {
					revision := row["applied_revision"].(float64)
					if revision <= previous || revision != row["desired_revision"].(float64) {
						return false
					}
					snapshot := r.snapshot(i)
					if snapshot == nil || float64(snapshot.Revision) != revision {
						return false
					}
					for _, rule := range snapshot.Rules {
						if rule.Id == r.rule {
							return false
						}
					}
					for _, grant := range snapshot.GetTunnelServer().GetGrants() {
						if grant.GetRule().GetId() == r.rule {
							return false
						}
					}
					return true
				})
			}
			assertTCPFailure(t, int(r.entryPorts[0].Load()))
			r.advance(t, 1)
			chainStreamClosed(t, existing)
			r.restore(t, offline)
			awaitRule(t, r.c, r.rule, "正常")
			r.body["enabled"] = true
			r.c.Do("PUT", "/api/rules/"+r.rule, r.body, 200)
			awaitRule(t, r.c, r.rule, "正常")
			r.exchange(t, 0)
		})
	}
}

func TestChainAuthorizationTerminalDiskFailureIsolatesOtherPorts(t *testing.T) {
	store := &faultyConfigStore{}
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup", func(i int, d *node.Dependencies) {
		if i == 2 {
			store.ConfigStore = d.Store.(node.ConfigStore)
			d.ConfigStore = store
		}
	})
	store.fail.Store(true)
	body := map[string]any{}
	for k, v := range r.body {
		body[k] = v
	}
	port := r.h.ReserveTCPPort()
	body["listen_port"] = port
	second := r.c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitNode(t, r.c, r.ids[2], func(v map[string]any) bool { return v["config_status"] == "同步失败" })
	r.advance(t, 2)
	for _, rule := range r.snapshot(0).Rules {
		if rule.Id == second {
			t.Fatal("entry received rule before terminal durability")
		}
	}
	r.exchange(t, 0)
	store.fail.Store(false)
	r.advance(t, 12)
	awaitRule(t, r.c, second, "正常")
	tcpExchange(t, port, "chain:")
	r.exchange(t, 0)
}

func TestChainAuthorizationRotationWaitsForEveryDirectPredecessor(t *testing.T) {
	for _, hop := range []int{1, 2} {
		t.Run([]string{"relay", "terminal"}[hop-1], func(t *testing.T) {
			r := newChainRig(t, 1, []int{1, 1}, "primary_backup")
			existing := r.exchange(t, 0)
			before := r.snapshot(hop).GetTunnelServer().CertificatePem
			predecessor := hop - 1
			r.nodes[predecessor].Disconnect()
			awaitNode(t, r.c, r.ids[predecessor], func(v map[string]any) bool { return v["connected"] == false })
			path := "/api/nodes/" + r.ids[hop] + "/tunnel/rotation"
			r.c.Do("POST", path, map[string]string{"kind": "both"}, 202)
			v := r.c.Do("GET", path, nil, 200)
			for i := 0; i < 20 && v["phase"] == "preparing"; i++ {
				r.advance(t, 1)
				v = r.c.Do("GET", path, nil, 200)
			}
			if v["phase"] != "distributing" || v["pending"] != float64(1) {
				t.Fatal("rotation failed to wait for actual offline predecessor", v, nodeRow(t, r.c, r.ids[hop]))
			}
			found := false
			for _, raw := range v["entries"].([]any) {
				e := raw.(map[string]any)
				if e["node_id"] == r.ids[predecessor] && e["confirmed"] == false {
					found = true
				}
			}
			if !found {
				t.Fatal("rotation did not list direct predecessor", v)
			}
			chainStreamExchange(t, existing)
			r.exchange(t, 0)
			r.restore(t, predecessor)
			for i := 0; i < 25; i++ {
				r.advance(t, 1)
				v = r.c.Do("GET", path, nil, 200)
				if v["phase"] == "complete" {
					break
				}
			}
			if v["phase"] != "complete" {
				t.Fatal("rotation failed to converge", v)
			}
			r.advance(t, 3)
			chainStreamExchange(t, existing)
			r.exchange(t, 0)
			if string(before) == string(r.snapshot(hop).GetTunnelServer().CertificatePem) {
				t.Fatal("rotation retained old certificate")
			}
		})
	}
}

func TestChainAuthorizationReconnectResendsCompleteGrantResults(t *testing.T) {
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup")
	for _, restart := range []bool{false, true} {
		r.nodes[2].Disconnect()
		awaitNode(t, r.c, r.ids[2], func(v map[string]any) bool { return v["connected"] == false })
		if restart {
			r.nodes[2].Restart()
		}
		r.restore(t, 2)
		awaitRule(t, r.c, r.rule, "正常")
		r.ready(t, true)
		r.exchange(t, 0)
	}
	// Lose the first server reply of a reconnect. A later same-process session
	// must resend its independent attempted frame, never infer ACK from heartbeat.
	var lost bool
	r.h.BeforeControlSend(func(_ context.Context, id string, m *controlpb.PanelMessage) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		if m.Snapshot != nil {
			r.latest[id] = m.Snapshot
		}
		if id == r.ids[2] && !lost {
			lost = true
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	r.nodes[2].Disconnect()
	awaitNode(t, r.c, r.ids[2], func(v map[string]any) bool { return v["connected"] == false })
	r.restore(t, 2)
	r.advance(t, 12)
	awaitRule(t, r.c, r.rule, "正常")
	r.exchange(t, 0)
	r.mu.Lock()
	didLose := lost
	r.mu.Unlock()
	if !didLose {
		t.Fatal("ACK loss seam unused")
	}
}

func TestChainAuthorizationSharedEditMoveModeAndDeleteIsolation(t *testing.T) {
	r := newChainRig(t, 1, []int{2, 1}, "primary_backup")
	secondBody := map[string]any{}
	for k, v := range r.body {
		secondBody[k] = v
	}
	secondPort := r.h.ReserveTCPPort()
	secondBody["listen_port"] = secondPort
	second := r.c.Do("POST", "/api/rules", secondBody, 201)["id"].(string)
	awaitRule(t, r.c, second, "正常")
	ordinaryPort := r.h.ReserveTCPPort()
	ordinaryBody := directBody("", ordinaryPort, "127.0.0.1", r.body["target_port"].(int))
	ordinaryBody["entry_node_id"] = r.ids[3]
	ordinary := r.c.Do("POST", "/api/rules", ordinaryBody, 201)["id"].(string)
	awaitRule(t, r.c, ordinary, "正常")
	old1, old2 := r.exchange(t, 0), tcpExchange(t, secondPort, "chain:")
	before := map[string]uint64{}
	for _, rule := range r.snapshot(0).Rules {
		before[rule.Id] = rule.AuthorizationGeneration
	}
	r.c.Do("PUT", "/api/exit-groups/"+r.groups[0], map[string]any{"name": "reordered chain candidates", "strategy": "primary_backup", "member_addresses": []map[string]string{{"node_id": r.ids[2], "address_id": r.addresses[2]}, {"node_id": r.ids[1], "address_id": r.addresses[1]}}}, 200)
	awaitRule(t, r.c, r.rule, "正常")
	awaitRule(t, r.c, second, "正常")
	r.advance(t, 2)
	for _, rule := range r.snapshot(0).Rules {
		if rule.AuthorizationGeneration <= before[rule.Id] {
			t.Fatal("shared chain edit did not revise all referring grants", rule.Id)
		}
	}
	chainStreamClosed(t, old1)
	chainStreamClosed(t, old2)
	r.exchange(t, 0)
	keep := tcpExchange(t, secondPort, "chain:")
	tcpExchange(t, ordinaryPort, "chain:")
	r.body["enabled"] = false
	r.c.Do("PUT", "/api/rules/"+r.rule, r.body, 200)
	awaitRule(t, r.c, r.rule, "正常")
	r.advance(t, 2)
	chainStreamExchange(t, keep)
	tcpExchange(t, ordinaryPort, "chain:")
	moved := addNode(r.c, "moved chain entry")
	mid := moved["id"].(string)
	r.h.StartNode(moved["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.LookupIPv4 = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
	})
	awaitNode(t, r.c, mid, connected)
	// Move the still-enabled shared port to another entry, then switch its mode.
	delete(secondBody, "entry_group_id")
	secondBody["entry_node_id"] = mid
	r.c.Do("PUT", "/api/rules/"+second, secondBody, 200)
	awaitRule(t, r.c, second, "正常")
	r.advance(t, 2)
	chainStreamClosed(t, keep)
	tcpExchange(t, secondPort, "chain:")
	secondBody["kind"] = "direct"
	delete(secondBody, "chain_id")
	r.c.Do("PUT", "/api/rules/"+second, secondBody, 200)
	awaitRule(t, r.c, second, "正常")
	tcpExchange(t, secondPort, "chain:")
	r.c.Do("DELETE", "/api/rules/"+r.rule, nil, 204)
	r.c.Do("DELETE", "/api/forward-chains/"+r.chain, nil, 204)
	r.advance(t, 2)
	for i := 1; i < len(r.ids); i++ {
		for _, g := range r.snapshot(i).GetTunnelServer().GetGrants() {
			if g.Rule.ChainId != "" {
				t.Fatal("mode change/delete left obsolete grant")
			}
		}
	}
	tcpExchange(t, secondPort, "chain:")
	tcpExchange(t, ordinaryPort, "chain:")
}

func TestChainAuthorizationHTTPRuleTrafficCountedOnce(t *testing.T) {
	r := newChainRig(t, 1, []int{1, 1, 1}, "primary_backup")
	connection := r.exchange(t, 0)
	awaitTraffic(t, r.h, r.c, r.rule, 4, 10, float64(1))
	connection.Close()
	awaitTraffic(t, r.h, r.c, r.rule, 4, 10, float64(0))
	// Real reports from every relay are already flowing; repeated collection
	// must not multiply bytes by three executor hops.
	r.advance(t, 12)
	awaitTraffic(t, r.h, r.c, r.rule, 4, 10, float64(0))
}
