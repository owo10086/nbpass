package panel

import (
	"context"
	"nbpass/internal/forwarding"
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
	for i := range views {
		v := &views[i]
		hops, err := rulePathHops(v.Rule, chains, groups)
		if err != nil {
			return err
		}
		v.Path = rulePathView{Entries: []rulePathEntry{}, Hops: hops, Target: rulePathTarget{v.TargetAddress, v.TargetPort}}
		for _, entry := range v.SyncMembers {
			v.Path.Entries = append(v.Path.Entries, rulePathEntry{entry.NodeID, entry.NodeName})
		}
	}
	return nil
}
