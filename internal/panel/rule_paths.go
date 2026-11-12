package panel

import (
	"context"
	"nbpass/internal/forwarding"
	"time"
)

type rulePathEntry struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
}
type rulePathTarget struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}
type rulePathView struct {
	Edges   []chainEdgeEvidence   `json:"edges"`
	Entries []rulePathEntry       `json:"entries"`
	Hops    []forwarding.ChainHop `json:"hops"`
	Target  rulePathTarget        `json:"target"`
}

func (p *Panel) attachRulePaths(ctx context.Context, views []ruleView, members map[string][]string) error {
	chains, err := loadForwardChains(ctx, p.db)
	if err != nil {
		return err
	}
	groups, err := loadChainGroups(ctx, p.db)
	if err != nil {
		return err
	}
	runtime, err := p.loadChainRuntime(ctx, p.db, p.connectedControls(), p.deps.Clock.Now())
	if err != nil {
		return err
	}
	for i := range views {
		v := &views[i]
		hops, err := rulePathHops(v.Rule, chains, groups)
		if err != nil {
			return err
		}
		v.Path = rulePathView{Entries: []rulePathEntry{}, Hops: hops, Target: rulePathTarget{v.TargetAddress, v.TargetPort}}
		if v.ChainID != "" {
			v.Path.Edges, err = p.chainRuleEvidence(ctx, p.db, v.Rule, forwarding.RuleEntryNodes(v.Rule, members), hops, runtime)
			if err != nil {
				return err
			}
			attachHopEvidence(v.Path.Hops, v.Path.Edges)
		}
		for _, entry := range v.SyncMembers {
			v.Path.Entries = append(v.Path.Entries, rulePathEntry{entry.NodeID, entry.NodeName})
		}
	}
	return nil
}

func attachHopEvidence(hops []forwarding.ChainHop, edges []chainEdgeEvidence) {
	for i := range hops {
		for _, e := range edges {
			if e.HopIndex != hops[i].Position {
				continue
			}
			at := ""
			if e.ObservedAt != nil {
				at = e.ObservedAt.Format(time.RFC3339Nano)
			}
			hops[i].Observations = append(hops[i].Observations, forwarding.ChainObservation{FromNodeID: e.FromNodeID, ToNodeID: e.ToNodeID, AddressID: e.AddressID, EndpointConfigDigest: e.EndpointConfigDigest, ChainFingerprint: e.ChainFingerprint, RuleID: e.RuleID, Generation: e.Generation, State: e.State, Fresh: e.Fresh, SampledAt: at})
		}
	}
}
