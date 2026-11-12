package panel

import (
	"context"
	"database/sql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// Grant acknowledgement is independent of unrelated business listener errors.
// The caller has authenticated the current connection/process. No success is
// inferred from the aggregate digest: the complete per-grant list is mandatory.
func (p *Panel) confirmChainGrants(ctx context.Context, tx *sql.Tx, id string, report *controlpb.NodeReport, desired uint64, digest string) (bool, error) {
	if desired == 0 || digest == "" {
		return false, nil
	}
	attempted := report.AttemptedRevision == desired && report.AttemptedDigest == digest
	if !attempted {
		return false, nil
	}
	s, err := p.exitHealthSnapshot(ctx, tx, id)
	if err != nil {
		return false, err
	}
	expected := map[[2]string]*controlpb.TunnelGrant{}
	hasChain := false
	for _, g := range s.GetTunnelServer().GetGrants() {
		expected[[2]string{g.EntryId, g.Rule.Id}] = g
		hasChain = hasChain || g.Rule.ChainId != ""
	}
	if !hasChain && len(report.TunnelGrantResults) == 0 {
		return false, nil
	}
	invalid := func() (bool, error) {
		return false, status.Error(codes.InvalidArgument, "逐授权报告不完整或身份、代次、版本无效")
	}
	if len(expected) != len(report.TunnelGrantResults) {
		return invalid()
	}
	for _, r := range report.TunnelGrantResults {
		if r == nil {
			return invalid()
		}
		key := [2]string{r.EntryId, r.RuleId}
		g := expected[key]
		if g == nil || r.Generation != g.Rule.AuthorizationGeneration || r.AppliedRevision > desired || len(r.ApplyError) > 1024 || (r.ApplyError == "" && (r.AppliedRevision != desired || !report.RevocationsApplied)) {
			return invalid()
		}
		delete(expected, key)
	}
	// A failed revocation checkpoint never replaces previously effective grants.
	if !report.RevocationsApplied {
		return false, nil
	}
	changed := false
	for _, r := range report.TunnelGrantResults {
		if r.Generation == 0 {
			continue
		} // Legacy single-hop grants do not enter migration 51.
		result, e := tx.ExecContext(ctx, `INSERT INTO node_tunnel_grant_results(node_id,entry_id,rule_id,generation,applied_revision,apply_error) VALUES(?,?,?,?,?,?)
 ON CONFLICT(node_id,entry_id,rule_id) DO UPDATE SET generation=excluded.generation,applied_revision=excluded.applied_revision,apply_error=excluded.apply_error
 WHERE generation<>excluded.generation OR applied_revision<>excluded.applied_revision OR apply_error<>excluded.apply_error`, id, r.EntryId, r.RuleId, r.Generation, r.AppliedRevision, r.ApplyError)
		if e != nil {
			return false, e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return false, e
		}
		changed = changed || n > 0
	}
	// Removed grants are excluded by the desired snapshot even before cleanup.
	rows, e := tx.QueryContext(ctx, "SELECT entry_id,rule_id FROM node_tunnel_grant_results WHERE node_id=?", id)
	if e != nil {
		return false, e
	}
	stale := [][2]string{}
	for rows.Next() {
		var entry, rule string
		if e = rows.Scan(&entry, &rule); e != nil {
			rows.Close()
			return false, e
		}
		found := false
		for _, r := range report.TunnelGrantResults {
			found = found || r.EntryId == entry && r.RuleId == rule
		}
		if !found {
			stale = append(stale, [2]string{entry, rule})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, key := range stale {
		if _, e = tx.ExecContext(ctx, "DELETE FROM node_tunnel_grant_results WHERE node_id=? AND entry_id=? AND rule_id=?", id, key[0], key[1]); e != nil {
			return false, e
		}
		changed = true
	}
	return changed, nil
}

// Build from the terminal backwards. An intermediate grant cannot authorize a
// successor until that exact successor grant is durable. Checking grant content
// as well as revision avoids reusing an ACK when candidate membership changes.
func (p *Panel) gateChainSnapshots(ctx context.Context, tx *sql.Tx, compiled map[string]*controlpb.ConfigSnapshot) error {
	old := map[string]*controlpb.ConfigSnapshot{}
	results := map[[3]string]*controlpb.TunnelGrantApplyResult{}
	rows, err := tx.QueryContext(ctx, "SELECT node_id,entry_id,rule_id,generation,applied_revision,apply_error FROM node_tunnel_grant_results")
	if err != nil {
		return err
	}
	for rows.Next() {
		var node string
		r := &controlpb.TunnelGrantApplyResult{}
		if err = rows.Scan(&node, &r.EntryId, &r.RuleId, &r.Generation, &r.AppliedRevision, &r.ApplyError); err != nil {
			rows.Close()
			return err
		}
		results[[3]string{node, r.EntryId, r.RuleId}] = r
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	grants := map[[3]string]*controlpb.TunnelGrant{}
	for node, s := range compiled {
		for _, g := range s.GetTunnelServer().GetGrants() {
			if g.Rule.ChainId != "" {
				grants[[3]string{node, g.EntryId, g.Rule.Id}] = g
			}
		}
		old[node], err = p.exitHealthSnapshot(ctx, tx, node)
		if err != nil {
			return err
		}
	}
	done := map[[3]string]bool{}
	ready := map[[3]string]bool{}
	var visit func([3]string) bool
	visit = func(key [3]string) bool {
		if done[key] {
			return ready[key]
		}
		done[key] = true
		g := grants[key]
		if g == nil {
			return false
		}
		if g.NextExitGroupId != "" {
			allowed := []string{}
			for _, next := range g.AuthorizedNextNodeIds {
				if visit([3]string{next, key[0], key[2]}) {
					allowed = append(allowed, next)
				}
			}
			g.AuthorizedNextNodeIds = allowed
			if len(allowed) == 0 {
				return false
			}
		}
		r := results[key]
		s := old[key[0]]
		if r == nil || r.ApplyError != "" || (r.AppliedRevision == 0 || r.AppliedRevision > s.Revision) || r.Generation != g.Rule.AuthorizationGeneration {
			return false
		}
		for _, previous := range s.GetTunnelServer().GetGrants() {
			if proto.Equal(previous, g) {
				ready[key] = true
				break
			}
		}
		return ready[key]
	}
	for key := range grants {
		visit(key)
	}
	for node, s := range compiled {
		if server := s.TunnelServer; server != nil {
			kept := server.Grants[:0]
			for _, g := range server.Grants {
				if g.Rule.ChainId == "" || g.NextExitGroupId == "" || len(g.AuthorizedNextNodeIds) > 0 {
					kept = append(kept, g)
				}
			}
			server.Grants = kept
		}
		for _, r := range s.Rules {
			if r.ChainId == "" {
				continue
			}
			r.AuthorizedExitIds = nil
			for _, group := range s.ExitGroups {
				if group.Id != r.ExitGroupId {
					continue
				}
				for _, ep := range group.Exits {
					if ready[[3]string{ep.NodeId, node, r.Id}] {
						r.AuthorizedExitIds = append(r.AuthorizedExitIds, ep.NodeId)
					}
				}
			}
		}
	}

	// ACKs describe a particular grant, not every unrelated setting in the node
	// snapshot. Keep the real applied revision when grant content is unchanged;
	// invalidate changed/removed grants before persisting the new snapshot.
	for key := range results {
		if key[0] == "" {
			continue
		}
		unchanged := false
		for _, previous := range old[key[0]].GetTunnelServer().GetGrants() {
			if previous.EntryId != key[1] || previous.Rule.Id != key[2] {
				continue
			}
			for _, next := range compiled[key[0]].GetTunnelServer().GetGrants() {
				if proto.Equal(previous, next) {
					unchanged = true
					break
				}
			}
		}
		if !unchanged {
			if _, err := tx.ExecContext(ctx, "DELETE FROM node_tunnel_grant_results WHERE node_id=? AND entry_id=? AND rule_id=?", key[0], key[1], key[2]); err != nil {
				return err
			}
		}
	}
	return nil
}
