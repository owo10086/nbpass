package clusterharness_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"nbpass/internal/node"
)

// Verify the port GET consumed by Rules independently against real node
// snapshots/grants, preserving every entry, ordered group and backup address.
func (r *chainRig) assertPortPath(t *testing.T, row map[string]any) {
	t.Helper()
	path := row["path"].(map[string]any)
	entries := path["entries"].([]any)
	sync := row["sync_members"].([]any)
	if len(entries) != r.entries || len(sync) != r.entries || len(row["entry_states"].([]any)) != r.entries {
		t.Fatal("port lost actual entries or invented relay listeners", row)
	}
	for i, raw := range entries {
		entry := raw.(map[string]any)
		if entry["node_id"] != r.ids[i] || entry["node_name"] == "" || sync[i].(map[string]any)["node_id"] != r.ids[i] || sync[i].(map[string]any)["listen_port"] != float64(r.body["listen_port"].(int)) {
			t.Fatal("port entry identity/listen port", entry, sync)
		}
	}
	target := path["target"].(map[string]any)
	if target["address"] != r.body["target_address"] || target["port"] != float64(r.body["target_port"].(int)) {
		t.Fatal("port lost final target", target)
	}
	hops := path["hops"].([]any)
	edges := path["edges"].([]any)
	if len(hops) != len(r.layers) {
		t.Fatal("incomplete port path", path)
	}
	previous := make([]int, r.entries)
	for i := range previous {
		previous[i] = i
	}
	count := 0
	for i, raw := range hops {
		hop := raw.(map[string]any)
		members := hop["members"].([]any)
		if hop["position"] != float64(i+1) || hop["exit_group_id"] != r.groups[i] || len(members) != len(r.layers[i]) {
			t.Fatal("port reordered/omitted group", hop)
		}
		for j, raw := range members {
			member := raw.(map[string]any)
			to := r.layers[i][j]
			if member["node_id"] != r.ids[to] || member["address_id"] != r.addresses[to] || member["port"] != float64(r.mapped[to]) {
				t.Fatal("port lost ordered candidate/address/mapped port", member)
			}
			for _, from := range previous {
				count++
				var edge map[string]any
				for _, raw := range edges {
					e := raw.(map[string]any)
					if e["from_node_id"] == r.ids[from] && e["to_node_id"] == r.ids[to] && e["hop_index"] == float64(i+1) {
						if edge != nil {
							t.Fatal("duplicate port edge", e)
						}
						edge = e
					}
				}
				if edge == nil {
					t.Fatal("missing port edge", from, to, edges)
				}
				matched := false
				for _, group := range r.snapshot(from).ExitGroups {
					if group.Id == r.groups[i] {
						for _, ep := range group.Exits {
							if ep.NodeId == r.ids[to] && ep.AddressId == r.addresses[to] {
								matched = member["address"] == ep.Address && member["port"] == float64(ep.Port) && edge["endpoint_config_digest"] == ep.EndpointConfigDigest
							}
						}
					}
				}
				if !matched {
					t.Fatal("port path differs from real endpoint", member, edge)
				}
				matched = false
				for _, grant := range r.snapshot(to).GetTunnelServer().GetGrants() {
					if grant.EntryId == r.ids[from] && grant.Rule.Id == row["id"] {
						matched = edge["rule_generation"] == float64(grant.Rule.AuthorizationGeneration) && edge["grant_generation"] == float64(grant.Rule.AuthorizationGeneration) && edge["chain_fingerprint"] == grant.Rule.GetChainAuthorization().GetFingerprint()
					}
				}
				if !matched || edge["rule_id"] != row["id"] || edge["chain_id"] != r.chain || edge["state"] != "ready" || edge["fresh"] != true || edge["local_ready"] != true || edge["suffix_ready"] != true || edge["applied_revision"].(float64) <= 0 || edge["applied_revision"].(float64) > edge["desired_revision"].(float64) {
					t.Fatal("port lost exact grant/current health evidence", edge)
				}
			}
		}
		previous = r.layers[i]
	}
	if count != len(edges) {
		t.Fatal("unexpected/omitted edges", count, edges)
	}
}

func TestPortChainHTTPModesPathTransferAndTraffic(t *testing.T) {
	// This scenario permits each real process to dial the same loopback target so
	// changing to direct/single exercises real forwarding, without fake results.
	r := newChainRig(t, 2, []int{2, 2}, "primary_backup", func(_ int, d *node.Dependencies) {
		d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	})
	r.ready(t, true, true)
	r.exchange(t, 0).Close()
	awaitTraffic(t, r.h, r.c, r.rule, 4, 10, float64(0))
	r.body["target_address"] = "127.0.0.1"
	// All six directed transitions; wire values stay direct/tunnel.
	for step, mode := range []string{"direct", "single", "chain", "single", "direct", "chain"} {
		delete(r.body, "chain_id")
		delete(r.body, "exit_group_id")
		r.body["kind"] = "tunnel"
		if mode == "chain" {
			r.body["chain_id"] = r.chain
		} else if mode == "single" {
			r.body["exit_group_id"] = r.groups[1]
		} else {
			r.body["kind"] = "direct"
		}
		r.c.Do("PUT", "/api/rules/"+r.rule, r.body, 200)
		row := awaitRule(t, r.c, r.rule, "正常")
		if row["kind"] != r.body["kind"] || row["chain_id"] != r.body["chain_id"] || row["exit_group_id"] != r.body["exit_group_id"] {
			t.Fatal("mode change left stale reference", mode, row)
		}
		r.exchange(t, 0).Close()
		awaitTraffic(t, r.h, r.c, r.rule, float64(8+4*step), float64(20+10*step), float64(0))
		path := ruleRow(t, r.c, r.rule)["path"].(map[string]any)
		wantHops := 0
		if mode == "single" {
			wantHops = 1
		}
		if mode == "chain" {
			wantHops = 2
		}
		if len(path["hops"].([]any)) != wantHops {
			t.Fatal("wrong mode path", mode, path)
		}
	}
	r.ready(t, true, true)
	r.awaitOperation(t, chainEdgesReady)
	r.assertPortPath(t, ruleRow(t, r.c, r.rule))
	// A format-3 overwrite round trip retains the shared reference and totals.
	doc := r.c.Do("GET", "/api/rules/export?id="+r.rule, nil, 200)
	if doc["format_version"] != float64(3) {
		t.Fatal("not format 3", doc)
	}
	exported := doc["rules"].([]any)[0].(map[string]any)
	if exported["chain_id"] != r.chain || exported["exit_group_id"] != nil || exported["traffic"] != nil || exported["path"] != nil {
		t.Fatal("definition export lost chain or leaked runtime", doc)
	}
	importRules(r.c, "/preflight", "overwrite", doc, 200)
	importRules(r.c, "", "overwrite", doc, 200)
	awaitRule(t, r.c, r.rule, "正常")
	awaitTraffic(t, r.h, r.c, r.rule, 28, 70, float64(0))
	// Missing references fail atomically and cannot create chains/resources.
	missing := map[string]any{}
	for k, v := range exported {
		missing[k] = v
	}
	missing["chain_id"] = "missing-chain"
	missing["listen_port"] = r.h.ReserveTCPPort()
	before := forwardChainRow(t, r.c, r.chain)["rule_count"]
	for _, path := range []string{"/preflight", ""} {
		importRules(r.c, path, "create", map[string]any{"format_version": 3, "rules": []any{missing}}, 400)
	}
	if forwardChainRow(t, r.c, r.chain)["rule_count"] != before || len(r.c.Do("GET", "/api/forward-chains", nil, 200)["chains"].([]any)) != 1 {
		t.Fatal("import created missing references")
	}
	// Old formats still create real direct/single rules, never accept chain_id.
	legacyGroup := entryGroup(r.c, r.ids[0])["id"].(string)
	for _, version := range []int{1, 2} {
		for _, single := range []bool{false, true} {
			body := directBody(legacyGroup, r.h.ReserveTCPPort(), "127.0.0.1", r.body["target_port"].(int))
			body["name"] = fmt.Sprintf("legacy-%d-%v", version, single)
			if single {
				body["kind"] = "tunnel"
				body["exit_group_id"] = r.groups[1]
			}
			legacy := map[string]any{"format_version": version, "rules": []any{body}}
			importRules(r.c, "/preflight", "create", legacy, 200)
			result := importRules(r.c, "", "create", legacy, 200)
			id := result["results"].([]any)[0].(map[string]any)["id"].(string)
			awaitRule(t, r.c, id, "正常")
			tcpExchange(t, body["listen_port"].(int), "chain:").Close()
		}
	}
	for _, version := range []int{1, 2} {
		importRules(r.c, "/preflight", "create", map[string]any{"format_version": version, "rules": []any{exported}}, 400)
	}
	awaitTraffic(t, r.h, r.c, r.rule, 28, 70, float64(0))
}

func assertPortCapabilityBlocker(t *testing.T, row map[string]any, nodeID, addressID string) {
	t.Helper()
	for _, raw := range row["configuration_blockers"].([]any) {
		b := raw.(map[string]any)
		if b["node_id"] == nodeID && b["address_id"] == addressID && b["capability"] == "forward_chains" && b["node_name"] != "" && b["reason"] != "" {
			return
		}
	}
	t.Fatal("port omitted concrete capability blocker", nodeID, addressID, row)
}

func TestPortChainHTTPGrantFailureReason(t *testing.T) {
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup")
	r.ready(t, true)
	r.exchange(t, 0).Close()
	r.c.Do("PUT", "/api/nodes/"+r.ids[2]+"/outbound", map[string]string{"outbound_ipv4": "198.51.100.99"}, 200)
	awaitNode(t, r.c, r.ids[2], func(v map[string]any) bool { return v["config_status"] == "同步失败" })
	failedNode := nodeRow(t, r.c, r.ids[2])
	if !strings.Contains(failedNode["apply_error"].(string), "198.51.100.99") {
		t.Fatal("node lost underlying source-binding reason", failedNode)
	}
	found := false
	for i := 0; i < 35; i++ {
		path := ruleRow(t, r.c, r.rule)["path"].(map[string]any)
		for _, raw := range path["edges"].([]any) {
			edge := raw.(map[string]any)
			reason, _ := edge["apply_error"].(string)
			if edge["from_node_id"] == r.ids[1] && edge["to_node_id"] == r.ids[2] && edge["state"] == "grant_failed" && reason == "授权未持久应用" && edge["grant_generation"] == edge["rule_generation"] && edge["applied_revision"] == float64(0) {
				found = true
			}
		}
		if found {
			break
		}
		r.advance(t, 1)
	}
	if !found {
		t.Fatal("port GET lost concrete terminal grant application failure", ruleRow(t, r.c, r.rule))
	}
	r.c.Do("PUT", "/api/nodes/"+r.ids[2]+"/outbound", map[string]string{"outbound_ipv4": ""}, 200)
	awaitRule(t, r.c, r.rule, "正常")
	r.exchange(t, 0).Close()
}
