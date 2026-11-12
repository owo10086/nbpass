package clusterharness_test

import (
	"testing"
	"time"

	"nbpass/internal/clusterharness"
)

func forwardChainRow(t *testing.T, c *clusterharness.Client, id string) map[string]any {
	t.Helper()
	for _, raw := range c.Do("GET", "/api/forward-chains", nil, 200)["chains"].([]any) {
		row := raw.(map[string]any)
		if row["id"] == id {
			return row
		}
	}
	t.Fatal("missing chain management row", id)
	return nil
}

func chainEdgeRow(t *testing.T, chain map[string]any, rule, from, to string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, raw := range chain["hops"].([]any) {
		for _, raw := range raw.(map[string]any)["observations"].([]any) {
			o := raw.(map[string]any)
			if o["rule_id"] == rule && o["from_node_id"] == from && o["to_node_id"] == to {
				if found != nil {
					t.Fatal("duplicate rule/edge observation", found, o)
				}
				found = o
			}
		}
	}
	if found == nil {
		t.Fatal("missing chain management edge", rule, from, to, chain)
	}
	return found
}

func (r *chainRig) awaitChain(t *testing.T, predicate func(map[string]any) bool) map[string]any {
	t.Helper()
	var row map[string]any
	for i := 0; i < 35; i++ {
		row = forwardChainRow(t, r.c, r.chain)
		if predicate(row) {
			return row
		}
		r.advance(t, 1)
	}
	t.Fatal("chain management observations did not converge", row)
	return nil
}

// Expected identities come from the real node configurations and grants that
// were sent over control, independently of the management/operations projection.
func (r *chainRig) assertChainContract(t *testing.T, row map[string]any, rules ...string) {
	t.Helper()
	if row["rule_count"] != float64(len(rules)) {
		t.Fatal("chain management lost real rule references", row)
	}
	hops := row["hops"].([]any)
	groups := row["exit_group_ids"].([]any)
	if len(hops) != len(r.layers) || len(groups) != len(r.groups) {
		t.Fatal("chain management lost ordered hops", row)
	}
	previous := make([]int, r.entries)
	for i := range previous {
		previous[i] = i
	}
	for i, raw := range hops {
		hop := raw.(map[string]any)
		if hop["position"] != float64(i+1) || hop["exit_group_id"] != r.groups[i] || groups[i] != r.groups[i] {
			t.Fatal("chain management reordered groups", hop, groups)
		}
		members := hop["members"].([]any)
		if len(members) != len(r.layers[i]) || len(hop["observations"].([]any)) != len(previous)*len(members)*len(rules) {
			t.Fatal("chain management omitted candidate/edge/rule", hop)
		}
		for j, raw := range members {
			member := raw.(map[string]any)
			to := r.layers[i][j]
			if member["node_id"] != r.ids[to] || member["address_id"] != r.addresses[to] || member["port"] != float64(r.mapped[to]) {
				t.Fatal("chain management lost candidate order or selected mapped address", member)
			}
			for _, from := range previous {
				for _, rule := range rules {
					o := chainEdgeRow(t, row, rule, r.ids[from], r.ids[to])
					var digest, fingerprint string
					var generation uint64
					for _, group := range r.snapshot(from).ExitGroups {
						if group.Id == r.groups[i] {
							for _, ep := range group.Exits {
								if ep.NodeId == r.ids[to] && ep.AddressId == r.addresses[to] {
									digest = ep.EndpointConfigDigest
									if member["address"] != ep.Address || member["port"] != float64(ep.Port) {
										t.Fatal("chain preview address differs from actual next-hop configuration", member, ep)
									}
								}
							}
						}
					}
					for _, grant := range r.snapshot(to).GetTunnelServer().GetGrants() {
						if grant.EntryId == r.ids[from] && grant.GetRule().GetId() == rule {
							generation = grant.Rule.AuthorizationGeneration
							fingerprint = grant.Rule.GetChainAuthorization().GetFingerprint()
						}
					}
					if digest == "" || fingerprint == "" || generation == 0 || o["address_id"] != r.addresses[to] || o["endpoint_config_digest"] != digest || o["chain_fingerprint"] != fingerprint || o["generation"] != float64(generation) {
						t.Fatal("chain management evidence differs from real endpoint/grant identity", o, digest, fingerprint, generation)
					}
					if o["fresh"] == true {
						at, err := time.Parse(time.RFC3339Nano, o["sampled_at"].(string))
						age := r.h.Clock.Now().Sub(at)
						if err != nil || age < 0 || age >= 15*time.Second {
							t.Fatal("fresh observation lacks current sample", o, age, err)
						}
					}
				}
			}
		}
		previous = r.layers[i]
	}
}
