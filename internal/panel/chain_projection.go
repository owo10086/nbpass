package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"time"
)

// Public projection includes all configured candidates; Members stays the
// entry-only set used by the publication coordinator.
type chainEdgeEvidence struct {
	ChainID              string     `json:"chain_id"`
	ChainFingerprint     string     `json:"chain_fingerprint"`
	RuleID               string     `json:"rule_id"`
	FromNodeID           string     `json:"from_node_id"`
	ToNodeID             string     `json:"to_node_id"`
	AddressID            string     `json:"address_id"`
	EndpointConfigDigest string     `json:"endpoint_config_digest"`
	HopIndex             int        `json:"hop_index"`
	Generation           uint64     `json:"rule_generation"`
	GrantGeneration      uint64     `json:"grant_generation"`
	DesiredRevision      uint64     `json:"desired_revision"`
	AppliedRevision      uint64     `json:"applied_revision"`
	ApplyError           string     `json:"apply_error,omitempty"`
	State                string     `json:"state"`
	Fresh                bool       `json:"fresh"`
	LocalReady           bool       `json:"local_ready"`
	SuffixReady          bool       `json:"suffix_ready"`
	ObservedAt           *time.Time `json:"observed_at"`
}
type chainRuntime struct {
	instances map[string]string
	inputs    map[string]entryReadinessInputs
	results   map[[3]string]*controlpb.TunnelGrantApplyResult
	health    map[[3]string]*controlpb.ExitHealth
	fresh     map[[3]string]bool
}

func (p *Panel) loadChainRuntime(ctx context.Context, q queryer, connected map[string]bool, now time.Time) (chainRuntime, error) {
	v := chainRuntime{inputs: map[string]entryReadinessInputs{}, health: map[[3]string]*controlpb.ExitHealth{}, fresh: map[[3]string]bool{}}
	instances := map[string]string{}
	v.instances = instances
	err := operationRows(ctx, q, `SELECT n.id,n.process_instance,s.snapshot,s.encrypted FROM nodes n JOIN node_snapshots s ON s.node_id=n.id`, func(rows *sql.Rows) error {
		var id, instance string
		var data []byte
		var encrypted bool
		if e := rows.Scan(&id, &instance, &data, &encrypted); e != nil {
			return e
		}
		s := &controlpb.ConfigSnapshot{}
		if e := p.decodeSnapshot(id, data, encrypted, s); e != nil {
			return e
		}
		instances[id] = instance
		v.inputs[id] = entryReadinessInputs{sessionLive: connected[id], snapshot: s, health: map[[3]string]bool{}}
		return nil
	})
	if err != nil {
		return v, err
	}
	v.results, err = loadChainGrantResults(ctx, q)
	if err != nil {
		return v, err
	}
	err = operationRows(ctx, q, `SELECT node_id,process_instance,received_at,exit_health_json FROM probe_realtime`, func(rows *sql.Rows) error {
		var id, instance string
		var at int64
		var data []byte
		if e := rows.Scan(&id, &instance, &at, &data); e != nil {
			return e
		}
		input, ok := v.inputs[id]
		if !ok {
			return nil
		}
		var hs []*controlpb.ExitHealth
		if e := json.Unmarshal(data, &hs); e != nil {
			return e
		}
		fresh := connected[id] && instances[id] == instance && !now.Before(time.Unix(0, at)) && now.Sub(time.Unix(0, at)) < 10*time.Second
		for _, h := range hs {
			if !matchesEndpointHealth(input.snapshot, h) {
				continue
			}
			key := [3]string{id, h.ExitId, forwarding.NormalizeAddressID(h.AddressId)}
			v.health[key] = h
			age := now.Sub(time.Unix(h.CheckedAtUnix, 0))
			v.fresh[key] = fresh && age >= 0 && age < 15*time.Second
			if v.fresh[key] && h.LastProbe == "success" && h.State == "healthy" {
				input.health[[3]string{h.ExitId, forwarding.NormalizeAddressID(h.AddressId), h.EndpointConfigDigest}] = true
			}
		}
		return nil
	})
	return v, err
}
func (p *Panel) chainRuleEvidence(ctx context.Context, q queryer, r forwarding.Rule, entries []string, hops []forwarding.ChainHop, runtime chainRuntime) ([]chainEdgeEvidence, error) {
	var gen uint64
	var fp string
	err := operationRows(ctx, q, "SELECT generation,fingerprint FROM rule_authorizations WHERE rule_id=?", func(rows *sql.Rows) error { return rows.Scan(&gen, &fp) }, r.ID)
	if err != nil {
		return nil, err
	}
	out := []chainEdgeEvidence{}
	previous := entries
	for i, hop := range hops {
		for _, from := range previous {
			for _, member := range hop.Members {
				target := runtime.inputs[member.NodeID]
				source := runtime.inputs[from]
				e := chainEdgeEvidence{ChainID: r.ChainID, ChainFingerprint: fp, RuleID: r.ID, FromNodeID: from, ToNodeID: member.NodeID, AddressID: member.AddressID, HopIndex: i + 1, Generation: gen, DesiredRevision: target.snapshot.GetRevision(), State: "unknown"}
				if server := target.snapshot.GetTunnelServer(); server != nil {
					e.EndpointConfigDigest = forwarding.EndpointConfigDigest(&controlpb.TunnelEndpoint{NodeId: member.NodeID, AddressId: member.AddressID, Address: member.Address, Port: uint32(member.Port), Transport: server.Transport, ServerName: server.ServerName})
				}
				for _, g := range source.snapshot.GetExitGroups() {
					if g.Id == hop.ExitGroupID {
						for _, ep := range g.Exits {
							if ep.NodeId == member.NodeID && forwarding.NormalizeAddressID(ep.AddressId) == member.AddressID {
								e.EndpointConfigDigest = ep.EndpointConfigDigest
							}
						}
					}
				}
				result := runtime.results[[3]string{member.NodeID, from, r.ID}]
				if result != nil {
					e.AppliedRevision = result.AppliedRevision
					e.GrantGeneration = result.Generation
					e.ApplyError = result.ApplyError
				}
				var grant *controlpb.TunnelGrant
				for _, g := range target.snapshot.GetTunnelServer().GetGrants() {
					if g.EntryId == from && g.Rule.Id == r.ID && g.Rule.AuthorizationGeneration == gen && g.Rule.GetChainAuthorization().GetFingerprint() == fp {
						grant = g
					}
				}
				confirmed := target.sessionLive && grant != nil && result != nil && result.Generation == gen && result.AppliedRevision > 0 && result.AppliedRevision <= e.DesiredRevision && result.ApplyError == ""
				if h := runtime.health[[3]string{from, member.NodeID, member.AddressID}]; h != nil && h.EndpointConfigDigest == e.EndpointConfigDigest {
					at := time.Unix(h.CheckedAtUnix, 0).UTC()
					e.ObservedAt = &at
					e.State = "historical"
				}
				e.LocalReady = source.health[[3]string{member.NodeID, member.AddressID, e.EndpointConfigDigest}]
				e.Fresh = runtime.fresh[[3]string{from, member.NodeID, member.AddressID}]
				if e.Fresh && !e.LocalReady {
					e.State = "edge_unhealthy"
				}
				if confirmed && e.LocalReady {
					e.SuffixReady = grant.NextExitGroupId == "" || chainSuffixReady(member.NodeID, grant.NextExitGroupId, grant.AuthorizedNextNodeIds, grant.Rule, runtime.inputs, runtime.results, map[string]bool{})
					e.State = "suffix_unknown"
					if e.SuffixReady {
						e.State = "ready"
					}
				}
				if result != nil && result.Generation == gen && result.ApplyError != "" {
					e.State = "grant_failed"
				}
				out = append(out, e)
			}
		}
		previous = nil
		for _, m := range hop.Members {
			previous = append(previous, m.NodeID)
		}
	}
	return out, nil
}
func (p *Panel) connectedControls() map[string]bool {
	out := map[string]bool{}
	for id, s := range p.controls {
		select {
		case <-s.done:
		default:
			out[id] = true
		}
	}
	return out
}
