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
			for _, disconnect := range stop {
				disconnect()
			}
			for _, id := range ids {
				awaitNode(t, c, id, func(v map[string]any) bool { return v["connected"] == false })
			}
			tcpExchange(t, port, "chain:")
		})
	}
}
