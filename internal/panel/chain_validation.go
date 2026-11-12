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
// Chain rule references are added when that rule mode is implemented.
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
		}
	}
	affectedRules := []forwarding.Rule{}
	for _, r := range rules {
		if ruleIDs[r.ID] || entryIDs[r.EntryGroupID] || exitIDs[r.ExitGroupID] {
			affectedRules = append(affectedRules, r)
		}
	}
	return affectedChains, affectedRules
}

func validateRulePath(r forwarding.Rule, members map[string][]string, groups map[string]forwarding.ChainGroup) error {
	if r.Kind != forwarding.Tunnel {
		return nil
	}
	g, ok := groups[r.ExitGroupID]
	if !ok || len(g.Members) == 0 {
		return &forwardingError{400, "出口组不存在或为空"}
	}
	if err := forwarding.ValidatePathNodes(forwarding.RuleEntryNodes(r, members), []forwarding.ChainHop{{Position: 1, Members: g.Members}}); err != nil {
		return &forwardingError{409, fmt.Sprintf("规则“%s”：%s", r.Name, err)}
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
			return &forwardingError{409, fmt.Sprintf("转发链“%s”：%s", chain.Name, err)}
		}
	}
	members, err := loadEntryGroupMembers(ctx, tx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if err = validateRulePath(r, members, groups); err != nil {
			return err
		}
	}
	return nil
}
