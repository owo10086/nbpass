package panel

import (
	"context"
	"database/sql"
	"fmt"

	"nbpass/internal/forwarding"
)

// pathChanges defines the mutation-to-dependency seam. This is deliberately
// invoked by saves, never by compileSnapshots: untouched legacy rules remain
// compilable during unrelated saves, reconnects and authorization confirmation.
// Chain references include disabled rules and every primary/backup entry.
type pathChanges struct {
	ChainIDs      []string
	ExitGroupIDs  []string
	EntryGroupIDs []string
	RuleIDs       []string
}

func idSet(ids []string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func affectedPaths(changes pathChanges, chains []forwarding.ForwardChain, rules []forwarding.Rule) ([]forwarding.ForwardChain, []forwarding.Rule) {
	chainIDs, exitIDs, entryIDs, ruleIDs := idSet(changes.ChainIDs), idSet(changes.ExitGroupIDs), idSet(changes.EntryGroupIDs), idSet(changes.RuleIDs)
	affectedChains := []forwarding.ForwardChain{}
	for _, c := range chains {
		affected := chainIDs[c.ID]
		for _, id := range c.ExitGroupIDs {
			affected = affected || exitIDs[id]
		}
		if affected {
			affectedChains = append(affectedChains, c)
			chainIDs[c.ID] = true
		}
	}
	affectedRules := []forwarding.Rule{}
	for _, r := range rules {
		if chainIDs[r.ChainID] || ruleIDs[r.ID] || entryIDs[r.EntryGroupID] || exitIDs[r.ExitGroupID] {
			affectedRules = append(affectedRules, r)
		}
	}
	return affectedChains, affectedRules
}

func validateRulePath(r forwarding.Rule, members map[string][]string, groups map[string]forwarding.ChainGroup, chains []forwarding.ForwardChain) error {
	if r.Kind != forwarding.Tunnel {
		return nil
	}
	hops, err := rulePathHops(r, chains, groups)
	if err != nil {
		return &forwardingError{400, err.Error()}
	}
	if err := forwarding.ValidatePathNodes(forwarding.RuleEntryNodes(r, members), hops); err != nil {
		return &forwardingError{400, fmt.Sprintf("规则“%s”：%s", r.Name, err)}
	}

	return nil
}

func validatePathChanges(ctx context.Context, tx *sql.Tx, changes pathChanges) error {
	chains, err := loadForwardChains(ctx, tx)
	if err != nil {
		return err
	}
	rules, err := loadRules(ctx, tx)
	if err != nil {
		return err
	}
	allChains := chains
	chains, rules = affectedPaths(changes, chains, rules)
	if len(chains) == 0 && len(rules) == 0 {
		return nil
	}
	groups, err := loadChainGroups(ctx, tx)
	if err != nil {
		return err
	}
	for _, chain := range chains {
		if _, err = forwarding.ExpandForwardChain(chain, groups); err != nil {
			return &forwardingError{400, fmt.Sprintf("转发链“%s”：%s", chain.Name, err)}
		}
	}
	members, err := loadEntryGroupMembers(ctx, tx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if err = validateRulePath(r, members, groups, allChains); err != nil {
			return err
		}
	}
	return nil
}

// Expands definitions without the new uniqueness check. Compilation and views
// must continue to describe unaffected legacy paths.
func rulePathHops(r forwarding.Rule, chains []forwarding.ForwardChain, groups map[string]forwarding.ChainGroup) ([]forwarding.ChainHop, error) {
	ids := []string{r.ExitGroupID}
	if r.Kind != forwarding.Tunnel {
		return []forwarding.ChainHop{}, nil
	}
	if r.ChainID != "" {
		ids = nil
		for _, c := range chains {
			if c.ID == r.ChainID {
				ids = c.ExitGroupIDs
				break
			}
		}
		if len(ids) < 2 {
			return nil, fmt.Errorf("转发链不存在或少于两跳")
		}
	}
	hops := []forwarding.ChainHop{}
	for i, id := range ids {
		g, ok := groups[id]
		if !ok || len(g.Members) == 0 {
			return nil, fmt.Errorf("端点组不存在或为空：%s", id)
		}
		hops = append(hops, forwarding.ChainHop{Position: i + 1, ExitGroupID: id, Name: g.Name, Members: append([]forwarding.ChainMember{}, g.Members...), Observations: []forwarding.ChainObservation{}})
	}
	return hops, nil
}
