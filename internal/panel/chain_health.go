package panel

import (
	"context"
	"nbpass/internal/controlpb"
)

func loadChainGrantResults(ctx context.Context, q queryer) (map[[3]string]*controlpb.TunnelGrantApplyResult, error) {
	rows, err := q.QueryContext(ctx, "SELECT node_id,entry_id,rule_id,generation,applied_revision,apply_error FROM node_tunnel_grant_results")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[[3]string]*controlpb.TunnelGrantApplyResult{}
	for rows.Next() {
		var id string
		r := &controlpb.TunnelGrantApplyResult{}
		if err = rows.Scan(&id, &r.EntryId, &r.RuleId, &r.Generation, &r.AppliedRevision, &r.ApplyError); err != nil {
			return nil, err
		}
		result[[3]string{id, r.EntryId, r.RuleId}] = r
	}
	return result, rows.Err()
}

// Each source contributes only its own endpoint observation. The downstream
// node must independently confirm the exact rule generation and every suffix.
// Offline control never revokes cached permission, but cannot assert readiness.
func chainPathReady(node, rule string, inputs map[string]entryReadinessInputs, results map[[3]string]*controlpb.TunnelGrantApplyResult) bool {
	for _, r := range inputs[node].snapshot.GetRules() {
		if r.Id == rule && r.ChainId != "" {
			return chainSuffixReady(node, r.ExitGroupId, r.AuthorizedExitIds, r, inputs, results, map[string]bool{})
		}
	}
	return false
}
func chainSuffixReady(from, group string, allowed []string, rule *controlpb.ForwardRule, inputs map[string]entryReadinessInputs, results map[[3]string]*controlpb.TunnelGrantApplyResult, seen map[string]bool) bool {
	source := inputs[from]
	if !source.sessionLive || seen[from] {
		return false
	}
	seen[from] = true
	defer delete(seen, from)
	for _, g := range source.snapshot.GetExitGroups() {
		if g.Id != group {
			continue
		}
		for _, ep := range g.Exits {
			if !containsEntryMember(allowed, ep.NodeId) || !source.health[[3]string{ep.NodeId, ep.AddressId, ep.EndpointConfigDigest}] {
				continue
			}
			target := inputs[ep.NodeId]
			result := results[[3]string{ep.NodeId, from, rule.Id}]
			if !target.sessionLive || target.snapshot == nil || result == nil || result.Generation != rule.AuthorizationGeneration || (result.AppliedRevision == 0 || result.AppliedRevision > target.snapshot.Revision) || result.ApplyError != "" {
				continue
			}
			for _, grant := range target.snapshot.GetTunnelServer().GetGrants() {
				if grant.EntryId != from || grant.Rule.Id != rule.Id || grant.Rule.AuthorizationGeneration != rule.AuthorizationGeneration || grant.Rule.GetChainAuthorization().GetFingerprint() != rule.GetChainAuthorization().GetFingerprint() {
					continue
				}
				if grant.NextExitGroupId == "" || chainSuffixReady(ep.NodeId, grant.NextExitGroupId, grant.AuthorizedNextNodeIds, grant.Rule, inputs, results, seen) {
					return true
				}
			}
		}
	}
	return false
}
