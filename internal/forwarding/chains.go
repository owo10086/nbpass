package forwarding

import "fmt"

// ForwardChain is a reusable, ordered path of endpoint groups. It carries no
// runtime authorization; saving a path alone must not configure any node.
type ForwardChain struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	ExitGroupIDs []string `json:"exit_group_ids"`
}

type ChainMember struct {
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name"`
	AddressID string `json:"address_id"`
	Address   string `json:"address"`
	Port      int    `json:"port"`
}

type ChainGroup struct {
	ID      string
	Name    string
	Members []ChainMember
}

// ChainObservation is scoped to an exact rule authorization and endpoint
// configuration. The management API returns an empty list until edge evidence
// is available; a successful local handshake alone cannot populate this view.
type ChainObservation struct {
	FromNodeID           string `json:"from_node_id"`
	ToNodeID             string `json:"to_node_id"`
	AddressID            string `json:"address_id"`
	EndpointConfigDigest string `json:"endpoint_config_digest"`
	ChainFingerprint     string `json:"chain_fingerprint"`
	RuleID               string `json:"rule_id"`
	Generation           uint64 `json:"generation"`
	State                string `json:"state"`
	Fresh                bool   `json:"fresh"`
	SampledAt            string `json:"sampled_at"`
}

type ChainHop struct {
	Position     int                `json:"position"`
	ExitGroupID  string             `json:"exit_group_id"`
	Name         string             `json:"name"`
	Members      []ChainMember      `json:"members"`
	Observations []ChainObservation `json:"observations"`
}

// ExpandForwardChain is shared by management, validation and future rule
// compilation. Group order and every primary/backup member are preserved.
func ExpandForwardChain(chain ForwardChain, groups map[string]ChainGroup) ([]ChainHop, error) {
	if len(chain.ExitGroupIDs) < 2 {
		return nil, fmt.Errorf("转发链至少需要两个端点组")
	}
	hops := make([]ChainHop, 0, len(chain.ExitGroupIDs))
	for i, id := range chain.ExitGroupIDs {
		group, ok := groups[id]
		if !ok || len(group.Members) == 0 {
			return nil, fmt.Errorf("第 %d 跳端点组不存在或为空：%s", i+1, id)
		}
		hops = append(hops, ChainHop{Position: i + 1, ExitGroupID: id, Name: group.Name, Members: append([]ChainMember{}, group.Members...), Observations: []ChainObservation{}})
	}
	if err := ValidatePathNodes(nil, hops); err != nil {
		return nil, err
	}
	return hops, nil
}

// Entry nodes are alternatives in the same entry group, so each is compared
// against all hops, but alternatives within a group are not separate hops.
func ValidatePathNodes(entries []string, hops []ChainHop) error {
	seen := map[string]int{}
	for _, id := range entries {
		seen[id] = 0
	}
	for _, hop := range hops {
		for _, m := range hop.Members {
			if position, ok := seen[m.NodeID]; ok {
				return fmt.Errorf("路径服务器不能重复：%s（第 %d 跳与第 %d 跳）", m.NodeID, position, hop.Position)
			}
			seen[m.NodeID] = hop.Position
		}
	}
	return nil
}
