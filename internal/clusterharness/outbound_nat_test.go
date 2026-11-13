package clusterharness_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/node"
)

func TestOutboundNATPublicInputThroughTunnelRestartAndRevalidation(t *testing.T) {
	const public = "89.43.141.167"
	// The NAT observer is the external dependency; the selected local source,
	// WSS tunnel, target TCP connection, panel API and cache are all real.
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var local netip.Addr
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().Is4() && prefix.Addr().IsGlobalUnicast() {
			local = prefix.Addr()
			break
		}
	}
	if !local.IsValid() {
		t.Skip("requires an assigned non-loopback IPv4")
	}
	h, c := nodeBrowser(t)
	a, b := addNode(c, "NAT入口A"), addNode(c, "NAT出口B")
	aid, bid := a["id"].(string), b["id"].(string)
	reserve, listen := directPorts(t)
	var changed atomic.Bool
	h.StartNode(a["installation_ticket"].(string), listen)
	bn := h.StartNode(b["installation_ticket"].(string), listen, func(_ *node.Config, d *node.Dependencies) {
		d.Forwarding.LocalIPv4 = func() ([]netip.Addr, error) { return []netip.Addr{local}, nil }
		d.Forwarding.PublicIPv4From = func(ctx context.Context, source string) (string, error) {
			if source != local.String() {
				return "", errors.New("wrong probe source")
			}
			if changed.Load() {
				return "163.53.52.167", nil
			}
			return public, nil
		}
		d.Forwarding.DialTarget = func(context.Context, string) (net.Conn, error) {
			t.Error("NAT exit used unbound target dial")
			return nil, errors.New("unbound target forbidden")
		}
	})
	awaitNode(t, c, aid, connected)
	awaitNode(t, c, bid, connected)
	target, seen := observedSourceTarget(t)
	c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": reserve(), "transport": "wss"}, 200)
	c.Do("PUT", "/api/nodes/"+bid+"/outbound", map[string]string{"outbound_ipv4": public}, 200)
	group := c.Do("POST", "/api/exit-groups", map[string]any{"name": "NAT出口", "members": []string{bid}, "strategy": "primary_backup"}, 201)["id"]
	port := reserve()
	body := directBody("", port, "127.0.0.1", target)
	body["entry_node_id"], body["kind"], body["exit_group_id"] = aid, "tunnel", group
	rule := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, c, rule, "正常")
	outboundExchange(t, port, seen, local.String())
	if got := c.Do("GET", "/api/nodes/"+bid+"/outbound", nil, 200)["outbound_ipv4"]; got != public {
		t.Fatal("panel lost configured public address", got)
	}
	instance := nodeRow(t, c, bid)["process_instance"]
	bn.Restart()
	awaitNode(t, c, bid, func(v map[string]any) bool {
		return connected(v) && v["process_instance"] != instance && v["config_status"] == "正常"
	})
	awaitRule(t, c, rule, "正常")
	outboundExchange(t, port, seen, local.String())
	// Re-deliver the same desired snapshot: changed NAT must withdraw success
	// and attribute the failure to B, even though it is shown on A's rule.
	revision := nodeRow(t, c, bid)["desired_revision"]
	changed.Store(true)
	for range 7 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(100 * time.Millisecond)
	}
	awaitNode(t, c, bid, func(v map[string]any) bool { return strings.Contains(v["apply_error"].(string), "NAT") })
	row := awaitRule(t, c, rule, "同步失败")
	if !strings.Contains(row["sync_members"].([]any)[0].(map[string]any)["apply_error"].(string), "NAT出口B") {
		t.Fatal("NAT failure lost B ownership", row)
	}
	assertTCPFailure(t, port)
	if nodeRow(t, c, bid)["desired_revision"] != revision {
		t.Fatal("NAT revalidation changed desired revision")
	}
	changed.Store(false)
	for range 7 {
		h.Clock.Advance(10 * time.Second)
		time.Sleep(100 * time.Millisecond)
	}
	awaitNode(t, c, bid, func(v map[string]any) bool { return v["config_status"] == "正常" })
	awaitRule(t, c, rule, "正常")
	outboundExchange(t, port, seen, local.String())
}
