package clusterharness_test

import (
	"context"
	"errors"
	"fmt"
	"nbpass/internal/node"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestForwardChainHTTPRealMultiHopAndOffline(t *testing.T) {
	for _, hops := range []int{2, 3} {
		t.Run(fmt.Sprint(hops), func(t *testing.T) {
			h, c := nodeBrowser(t)

			ids := []string{}
			groups := []string{}
			stop := []func(){}
			for i := 0; i <= hops; i++ {
				n := addNode(c, fmt.Sprint("hop", i))
				id := n["id"].(string)
				ids = append(ids, id)
				index := i
				live := h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
					d.Forwarding.LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
						if index == hops && host == "chain-target.test" {
							return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
						}
						return nil, errors.New("target resolution forbidden before terminal")
					}
					if index < hops {
						d.Forwarding.DialTarget = func(context.Context, string) (net.Conn, error) {
							return nil, errors.New("target dialing forbidden before terminal")
						}
					}
				})
				stop = append(stop, live.Disconnect)
				awaitNode(t, c, id, connected)
				if i > 0 {
					c.Do("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": h.ReserveTCPPort(), "transport": "wss"}, 200)
					groups = append(groups, c.Do("POST", "/api/exit-groups", map[string]any{"name": fmt.Sprint(i), "members": []string{id}, "strategy": "primary_backup"}, 201)["id"].(string))
				}
			}
			chain := c.Do("POST", "/api/forward-chains", map[string]any{"name": "real chain", "exit_group_ids": groups}, 201)["id"]
			port := h.ReserveTCPPort()
			body := directBody(entryGroup(c, ids[0])["id"].(string), port, "chain-target.test", echoTarget(t, "chain:"))
			body["kind"] = "tunnel"
			body["chain_id"] = chain
			rule := c.Do("POST", "/api/rules", body, 201)["id"].(string)
			awaitRule(t, c, rule, "正常")
			tcpExchange(t, port, "chain:")
			for _, id := range ids {
				awaitUptime(t, c, id, 0)
			}
			h.Clock.Advance(5 * time.Second)
			var current map[string]any
			for tick := 0; tick < 35; tick++ {
				current = forwardChainRow(t, c, chain.(string))
				ready := true
				for i := 0; i < hops; i++ {
					o := chainEdgeRow(t, current, rule, ids[i], ids[i+1])
					if o["state"] != "ready" || o["fresh"] != true {
						ready = false
					}
				}
				if ready {
					break
				}
				awaitControlIdle(t, h)
				h.Clock.Advance(time.Second)
				time.Sleep(2 * time.Millisecond)
			}
			if current["rule_count"] != float64(1) || len(current["hops"].([]any)) != hops {
				t.Fatal("management GET lost real multi-hop reference/path", current)
			}
			for i, raw := range current["hops"].([]any) {
				hop := raw.(map[string]any)
				o := chainEdgeRow(t, current, rule, ids[i], ids[i+1])
				if hop["position"] != float64(i+1) || hop["exit_group_id"] != groups[i] || len(hop["observations"].([]any)) != 1 || o["address_id"] != "public" || o["endpoint_config_digest"] == "" || o["chain_fingerprint"] == "" || o["generation"].(float64) < 1 || o["state"] != "ready" || o["fresh"] != true || o["sampled_at"] == "" {
					t.Fatal("management GET lost current real multi-hop evidence", hop)
				}
			}
			for _, disconnect := range stop {
				disconnect()
			}
			for _, id := range ids {
				awaitNode(t, c, id, func(v map[string]any) bool { return v["connected"] == false })
			}
			tcpExchange(t, port, "chain:")
			history := forwardChainRow(t, c, chain.(string))
			portHistory := ruleRow(t, c, rule)["path"].(map[string]any)
			if len(portHistory["edges"].([]any)) != hops {
				t.Fatal("port lost offline path edges", portHistory)
			}
			for _, raw := range portHistory["edges"].([]any) {
				edge := raw.(map[string]any)
				if edge["state"] != "historical" || edge["fresh"] != false || edge["observed_at"] == nil || edge["rule_generation"] != edge["grant_generation"] {
					t.Fatal("port claimed offline cached forwarding was current readiness", edge)
				}
			}

			for i := 0; i < hops; i++ {
				old := chainEdgeRow(t, current, rule, ids[i], ids[i+1])
				o := chainEdgeRow(t, history, rule, ids[i], ids[i+1])
				if o["state"] != "historical" || o["fresh"] != false || o["sampled_at"] == "" || o["endpoint_config_digest"] != old["endpoint_config_digest"] || o["chain_fingerprint"] != old["chain_fingerprint"] || o["generation"] != old["generation"] {
					t.Fatal("management GET claimed offline cached forwarding was fresh readiness", o, old)
				}
			}
		})
	}
}
