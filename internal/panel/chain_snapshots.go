package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

func chainAuthorizationFingerprint(ctx context.Context, tx *sql.Tx, r forwarding.Rule, entries []string) (string, error) {
	chains, err := loadForwardChains(ctx, tx)
	if err != nil {
		return "", err
	}
	groups, err := loadChainGroups(ctx, tx)
	if err != nil {
		return "", err
	}
	hops, err := rulePathHops(r, chains, groups)
	if err != nil {
		return "", err
	}
	type member struct{ Node, Address, Digest, Source string }
	type hop struct {
		Group   string
		Members []member
	}
	path := []hop{}
	for i, h := range hops {
		item := hop{Group: h.ExitGroupID}
		for _, m := range h.Members {
			ep := &controlpb.TunnelEndpoint{NodeId: m.NodeID, AddressId: m.AddressID, Address: m.Address, Port: uint32(m.Port), Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS}
			var source string
			if err := tx.QueryRowContext(ctx, "SELECT t.server_name,t.certificate_pem,n.outbound_ipv4 FROM tunnel_exits t JOIN nodes n ON n.id=t.node_id WHERE t.node_id=?", m.NodeID).Scan(&ep.ServerName, &ep.CertificatePem, &source); err != nil {
				return "", err
			}
			if i != len(hops)-1 {
				source = ""
			}
			item.Members = append(item.Members, member{m.NodeID, m.AddressID, forwarding.EndpointConfigDigest(ep), source})
		}
		path = append(path, item)
	}
	entries = append([]string{}, entries...)
	sort.Strings(entries)
	data, err := json.Marshal(struct {
		Base, Chain string
		Path        []hop
	}{authorizationFingerprint(r, entries, nil), r.ChainID, path})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// compileChain emits only adjacent pairs. A relay has no business listener: its
// incoming grant contains the authenticated local next-group authorization.
func (p *Panel) compileChain(ctx context.Context, tx *sql.Tx, r forwarding.Rule, entries []string, groups map[string]exitGroupView, compiled map[string]*controlpb.ConfigSnapshot, plans map[[2]string]capabilityPlan, endpoints map[[2]string]*controlpb.TunnelEndpoint, pairs map[string]bool, generation uint64) error {
	chains, err := loadForwardChains(ctx, tx)
	if err != nil {
		return err
	}
	var ids []string
	for _, c := range chains {
		if c.ID == r.ChainID {
			ids = c.ExitGroupIDs
			break
		}
	}
	if len(ids) < 2 {
		return errors.New("转发链不存在或少于两跳")
	}
	fingerprint, err := chainAuthorizationFingerprint(ctx, tx, r, entries)
	if err != nil {
		return err
	}
	// Multiple actual entry nodes may share relays. Grants are keyed by the real
	// immediate predecessor, so duplicate expansion must not duplicate grants.
	seenGrant := map[[3]string]bool{}
	edges := []*controlpb.ChainEdgeAuthorization{}
	seenEdge := map[[3]string]bool{}
	for _, entry := range entries {
		plan := plans[[2]string{entry, r.ID}]
		if plan.Blocked {
			continue
		}
		previous := []string{entry}
		for i, id := range ids {
			g := groups[id]
			eligible := map[string]bool{}
			if i >= len(plan.Hops) {
				return errors.New("转发链能力路径不完整")
			}
			for _, c := range plan.Hops[i] {
				eligible[c.NodeID] = true
			}
			current := []string{}
			for _, m := range g.MemberAddresses {
				if eligible[m.NodeID] {
					current = append(current, m.NodeID)
				}
			}
			for _, from := range previous {
				cg := &controlpb.ExitGroup{Id: id, Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP}
				for _, m := range g.MemberAddresses {
					to := m.NodeID
					if !eligible[to] || !plan.Edges[[2]string{from, to}] {
						continue
					}
					base := endpoints[[2]string{to, m.AddressID}]
					if base == nil {
						return &forwardingError{400, "端点组地址不存在"}
					}
					token, err := p.pairCredential(ctx, tx, from, to)
					if err != nil {
						return err
					}
					ep := proto.Clone(base).(*controlpb.TunnelEndpoint)
					ep.EntryId, ep.Credential, ep.AddressId = from, token, m.AddressID
					ep.EndpointConfigDigest = forwarding.EndpointConfigDigest(ep)
					cg.Exits = append(cg.Exits, ep)
					edgeKey := [3]string{from, to, m.AddressID}
					if !seenEdge[edgeKey] {
						seenEdge[edgeKey] = true
						edges = append(edges, &controlpb.ChainEdgeAuthorization{FromNodeId: from, ToNodeId: to, AddressId: m.AddressID, HopIndex: uint32(i + 1), EndpointConfigDigest: ep.EndpointConfigDigest})
					}
					server := compiled[to].TunnelServer
					if !pairs[from+":"+to] {
						server.Peers = append(server.Peers, &controlpb.TunnelPeer{EntryId: from, Credential: token})
						pairs[from+":"+to] = true
					}
					key := [3]string{to, from, r.ID}
					if seenGrant[key] {
						continue
					}
					seenGrant[key] = true
					rule := r.Config()
					rule.AuthorizationGeneration = generation
					rule.ChainAuthorization = &controlpb.ChainAuthorization{Fingerprint: fingerprint}
					rule.HopIndex = uint32(i + 1)
					// Relay rules carry no listener port; the original target remains bound
					// for request authorization, but only a terminal grant may dial it.
					rule.ListenPort = 0
					grant := &controlpb.TunnelGrant{EntryId: from, Rule: rule, HopIndex: uint32(i + 1)}
					if i+1 < len(ids) {
						grant.NextExitGroupId = ids[i+1]
						rule.ExitGroupId = ids[i+1]
						for _, next := range plan.Hops[i+1] {
							if plan.Edges[[2]string{to, next.NodeID}] {
								grant.AuthorizedNextNodeIds = append(grant.AuthorizedNextNodeIds, next.NodeID)
							}
						}
					}
					server.Grants = append(server.Grants, grant)
				}
				if len(cg.Exits) > 0 {
					mergeChainGroup(compiled[from], cg, g)
				}
			}
			previous = current
		}
		for _, rule := range compiled[entry].Rules {
			if rule.Id == r.ID {
				rule.ExitGroupId = ids[0]
				rule.AuthorizationGeneration = generation
				rule.ChainAuthorization = &controlpb.ChainAuthorization{Fingerprint: fingerprint}
				rule.HopIndex = 0
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.HopIndex != b.HopIndex {
			return a.HopIndex < b.HopIndex
		}
		if a.FromNodeId != b.FromNodeId {
			return a.FromNodeId < b.FromNodeId
		}
		if a.ToNodeId != b.ToNodeId {
			return a.ToNodeId < b.ToNodeId
		}
		return a.AddressId < b.AddressId
	})

	for node, snapshot := range compiled {
		for _, rule := range snapshot.Rules {
			if rule.Id == r.ID {
				rule.ChainAuthorization.Edges = forwarding.ChainEvidenceEdges(edges, node, 0, "")
			}
		}
		for _, grant := range snapshot.GetTunnelServer().GetGrants() {
			if grant.Rule.Id == r.ID {
				grant.Rule.ChainAuthorization.Edges = forwarding.ChainEvidenceEdges(edges, node, grant.HopIndex, grant.EntryId)
			}
		}
	}

	return nil
}
func mergeChainGroup(snapshot *controlpb.ConfigSnapshot, group *controlpb.ExitGroup, definition exitGroupView) {
	for _, existing := range snapshot.ExitGroups {
		if existing.Id != group.Id {
			continue
		}
		byID := map[string]*controlpb.TunnelEndpoint{}
		for _, ep := range existing.Exits {
			byID[ep.NodeId] = ep
		}
		for _, ep := range group.Exits {
			byID[ep.NodeId] = ep
		}
		existing.Exits = nil
		for _, id := range definition.Members {
			if ep := byID[id]; ep != nil {
				existing.Exits = append(existing.Exits, ep)
			}
		}
		return
	}
	snapshot.ExitGroups = append(snapshot.ExitGroups, group)
}
