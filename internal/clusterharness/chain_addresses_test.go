package clusterharness_test

import (
	"nbpass/internal/controlpb"
	"testing"
)

func incidentForChainRule(t *testing.T, r *chainRig, rule string, open bool) map[string]any {
	t.Helper()
	for i := 0; i < 35; i++ {
		for _, raw := range incidentRows(r.c) {
			v := raw.(map[string]any)
			if (v["ended_at"] == nil) != open {
				continue
			}
			for _, raw := range v["refs"].([]any) {
				ref := raw.(map[string]any)
				if ref["kind"] == "rule" && ref["id"] == rule {
					return v
				}
			}
		}
		r.advance(t, 1)
	}
	t.Fatal("missing chain rule incident", rule, open)
	return nil
}
func TestChainFailoverAddressIsolationLateDigestAndIncidentBoundary(t *testing.T) {
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup")
	r.ready(t, true)
	r.exchange(t, 0)
	// Public and dedicated addresses on the same B have independent physical
	// endpoints; the rule selecting the good dedicated address remains ready.
	var exit map[string]any
	for _, raw := range r.c.Do("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any) {
		v := raw.(map[string]any)
		if v["node_id"] == r.ids[1] {
			exit = v
		}
	}
	ix := exit["dedicated_addresses"].([]any)[0].(map[string]any)
	badPort := r.h.ReserveTCPPort()
	r.c.Do("PUT", "/api/nodes/"+r.ids[1]+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": r.ports[1], "public_port": badPort, "transport": "wss", "dedicated_addresses": []any{ix}}, 200)
	bg := r.c.Do("POST", "/api/exit-groups", map[string]any{"name": "bad public B", "members": []string{r.ids[1]}, "strategy": "primary_backup"}, 201)["id"].(string)
	bc := r.c.Do("POST", "/api/forward-chains", map[string]any{"name": "bad address chain", "exit_group_ids": []string{bg, r.groups[1]}}, 201)["id"].(string)
	eg := entryGroup(r.c, r.ids[0])["id"].(string)
	body := directBody(eg, r.h.ReserveTCPPort(), "chain-target.test", r.body["target_port"].(int))
	body["kind"], body["chain_id"] = "tunnel", bc
	bad := r.c.Do("POST", "/api/rules", body, 201)["id"].(string)
	awaitRule(t, r.c, bad, "同步失败")
	r.ready(t, true)
	r.exchange(t, 0)
	if entryState(t, r.c, eg, r.ids[0])["ready"] != false {
		t.Fatal("bad public borrowed dedicated readiness")
	}
	goodView := r.awaitChain(t, func(v map[string]any) bool {
		return chainEdgeRow(t, v, r.rule, r.ids[0], r.ids[1])["state"] == "ready"
	})
	r.assertChainContract(t, goodView, r.rule)
	goodEdge := chainEdgeRow(t, goodView, r.rule, r.ids[0], r.ids[1])
	badView := forwardChainRow(t, r.c, bc)
	badEdge := chainEdgeRow(t, badView, bad, r.ids[0], r.ids[1])
	if badView["rule_count"] != float64(1) || badEdge["address_id"] != "public" || badEdge["endpoint_config_digest"] == goodEdge["endpoint_config_digest"] || badEdge["state"] == "ready" {
		t.Fatal("management page borrowed dedicated address evidence for public address", badEdge, goodEdge)
	}
	r.links[1][2].block(true)
	assertTCPFailure(t, int(r.entryPorts[0].Load()))
	r.ready(t, false)
	old := incidentForChainRule(t, r, r.rule, true)
	oldID := old["id"].(string)
	oldDigest := ""
	for _, group := range r.snapshot(0).ExitGroups {
		for _, ep := range group.Exits {
			if ep.NodeId == r.ids[1] && ep.AddressId == r.addresses[1] {
				oldDigest = ep.EndpointConfigDigest
			}
		}
	}
	if oldDigest == "" {
		t.Fatal("missing dedicated endpoint digest")
	}
	r.h.TransformControlReports(func(report *controlpb.NodeReport) {
		if report.NodeId == r.ids[0] && report.Probe != nil {
			for _, health := range report.Probe.ExitHealth {
				if health.ExitId == r.ids[1] && health.AddressId == r.addresses[1] {
					health.EndpointConfigDigest = oldDigest
					health.State = "healthy"
					health.LastProbe = "success"
					health.CheckedAtUnix = r.h.Clock.Now().Unix()
				}
			}
		}
	})
	ix["address"] = "127.0.0.1"
	r.c.Do("PUT", "/api/nodes/"+r.ids[1]+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": r.ports[1], "public_port": badPort, "transport": "wss", "dedicated_addresses": []any{ix}}, 200)
	r.advance(t, 15)
	old = r.c.Do("GET", "/api/incidents/"+oldID, nil, 200)
	if old["closure"] != "新版本替代" {
		t.Fatal("same address ID changed endpoint without replacing incident boundary", old)
	}
	current := incidentForChainRule(t, r, r.rule, true)
	if current["id"] == oldID {
		t.Fatal("new config reused old incident")
	}
	r.links[1][2].recover()
	r.advance(t, 75)
	if entryState(t, r.c, r.group, r.ids[0])["ready"] != false {
		t.Fatal("late old digest made current chain ready")
	}
	current = r.c.Do("GET", "/api/incidents/"+current["id"].(string), nil, 200)
	if current["ended_at"] != nil {
		t.Fatal("unknown current address evidence cleared incident", current)
	}
	view := forwardChainRow(t, r.c, r.chain)
	edge := chainEdgeRow(t, view, r.rule, r.ids[0], r.ids[1])
	if edge["address_id"] != r.addresses[1] || edge["endpoint_config_digest"] == oldDigest || edge["chain_fingerprint"] == goodEdge["chain_fingerprint"] || edge["generation"].(float64) <= goodEdge["generation"].(float64) || edge["state"] != "unknown" || edge["fresh"] != false || edge["sampled_at"] != "" {
		t.Fatal("management page accepted late old digest as current evidence", edge, goodEdge)
	}
	r.h.TransformControlReports(nil)
	r.ready(t, true)
	r.exchange(t, 0)
	r.advance(t, 10)
	view = r.awaitChain(t, func(v map[string]any) bool {
		return chainEdgeRow(t, v, r.rule, r.ids[0], r.ids[1])["state"] == "ready"
	})
	r.assertChainContract(t, view, r.rule)
	if old = r.c.Do("GET", "/api/incidents/"+oldID, nil, 200); old["closure"] != "新版本替代" {
		t.Fatal("late observations rewrote old boundary")
	}
}
