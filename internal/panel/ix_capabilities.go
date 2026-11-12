package panel

import (
	"context"
	"encoding/json"
	"net/netip"

	"nbpass/internal/compatibility"
	"nbpass/internal/forwarding"
)

const (
	syncCapabilityBlocked     = "整条阻塞（需升级服务器）"
	syncCapabilityDegraded    = "可运行但备用成员能力不足"
	syncCapabilityPending     = "未同步（备用成员能力不足，需升级服务器）"
	syncCapabilityUnconfirmed = "待确认（备用成员能力不足，需升级服务器）"
)

type configurationBlocker struct {
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name"`
	AddressID  string `json:"address_id"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
}

type nodeCapabilities struct {
	name         string
	capabilities map[string]bool
}
type capabilitySet map[string]nodeCapabilities

// loadCapabilitySet reads the last authenticated admission, including when control
// is offline. Independent reports never write it. Old flags retain their existing
// protocol-1 semantics; IX always needs a current-instance, explicit declaration.
func loadCapabilitySet(ctx context.Context, q queryer) (capabilitySet, error) {
	rows, err := q.QueryContext(ctx, `SELECT n.id,n.name,n.process_instance,n.tunnel_authorization_version,n.exit_failover_version,n.tunnel_rotation_version,c.report
 FROM nodes n LEFT JOIN node_compatibility c ON c.node_id=n.id ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := capabilitySet{}
	for rows.Next() {
		var id, name, instance string
		var authorization, failover, rotation uint32
		var raw []byte
		if err := rows.Scan(&id, &name, &instance, &authorization, &failover, &rotation, &raw); err != nil {
			return nil, err
		}
		n := nodeCapabilities{name: name, capabilities: map[string]bool{"tunnel_authorization": authorization == 1, "exit_failover": failover == 1, "tunnel_rotation": rotation == 1}}
		if len(raw) > 0 {
			var c nodeCompatibility
			if err := json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			if c.ProcessInstance == instance && instance != "" && !c.Legacy && compatibility.Contains(compatibility.ControlPrevious, compatibility.ControlCurrent, c.ControlProtocol) && compatibility.Contains(c.Node.GetControlMin(), c.Node.GetControlMax(), c.ControlProtocol) && compatibility.CompatibleState(c.Node) {
				for _, cap := range []string{compatibility.IXAddressDialing, compatibility.IXMemberIdentity, compatibility.OutboundIPv4, compatibility.ForwardChains} {
					n.capabilities[cap] = c.supports(cap) && compatibility.Supports(c.Node, cap) && compatibility.Contains(c.Node.GetTunnelMin(), c.Node.GetTunnelMax(), compatibility.TunnelCurrent)
				}
			}
		}
		result[id] = n
	}
	return result, rows.Err()
}

// missing is the shared node ID + required capabilities -> missing capabilities seam.
// Unknown names and absent reports never prove support. Input order is retained.
func (c capabilitySet) missing(nodeID string, required []string) []string {
	missing := []string{}
	seen := map[string]bool{}
	for _, cap := range required {
		if !c[nodeID].capabilities[cap] && !seen[cap] {
			missing = append(missing, cap)
			seen[cap] = true
		}
	}
	return missing
}

type capabilityRequirement struct {
	NodeID, AddressID string
	Capabilities      []string
}
type capabilityCandidate struct {
	NodeID, AddressID string
	Required          []capabilityRequirement
}

// Required applies to the whole path. Every hop needs at least one eligible
// candidate; candidates can require semantics on both ends of their edge.
// 02/03 add address requirements, 04 adds final-dialer requirements, 06 adds hops.
type capabilityRequirements struct {
	Required []capabilityRequirement
	Hops     [][]capabilityCandidate
}
type capabilityPlan struct {
	Blocked  bool
	Blockers []configurationBlocker
	Hops     [][]capabilityCandidate
}

func (c capabilitySet) evaluate(required capabilityRequirements) capabilityPlan {
	plan := capabilityPlan{Blockers: []configurationBlocker{}}
	seen := map[configurationBlocker]bool{}
	check := func(requirements []capabilityRequirement) bool {
		ok := true
		for _, r := range requirements {
			for _, cap := range c.missing(r.NodeID, r.Capabilities) {
				b := configurationBlocker{NodeID: r.NodeID, NodeName: c[r.NodeID].name, AddressID: r.AddressID, Capability: cap, Reason: "服务器未声明所需能力或兼容范围不支持，需升级服务器"}
				if !seen[b] {
					plan.Blockers = append(plan.Blockers, b)
					seen[b] = true
				}
				ok = false
			}
		}
		return ok
	}
	plan.Blocked = !check(required.Required)
	for _, hop := range required.Hops {
		eligible := []capabilityCandidate{}
		for _, candidate := range hop {
			if check(candidate.Required) {
				eligible = append(eligible, candidate)
			}
		}
		plan.Hops = append(plan.Hops, eligible)
		plan.Blocked = plan.Blocked || len(eligible) == 0
	}
	return plan
}

func (p capabilityPlan) allows(nodeID string) bool {
	if p.Blocked {
		return false
	}
	for _, hop := range p.Hops {
		for _, candidate := range hop {
			if candidate.NodeID == nodeID {
				return true
			}
		}
	}
	return false
}

// ruleCapabilityRequirements is the only feature-to-executor mapping. Consumers
// must use its evaluated plan rather than reimplementing requirements in views.
func ruleCapabilityRequirements(r forwarding.Rule, entry string, group exitGroupView, legacy map[[3]string]bool, addresses map[string]tunnelExitView) capabilityRequirements {
	required := capabilityRequirements{}
	if r.Kind != forwarding.Tunnel {
		return required
	}
	if len(group.Members) > 1 {
		required.Required = append(required.Required, capabilityRequirement{NodeID: entry, Capabilities: []string{"exit_failover"}})
	}
	hop := []capabilityCandidate{}
	for _, exit := range group.Members {
		candidate := capabilityCandidate{NodeID: exit}
		if len(group.Members) != 1 || !legacy[[3]string{exit, entry, r.ID}] {
			candidate.Required = []capabilityRequirement{{NodeID: entry, Capabilities: []string{"tunnel_authorization"}}, {NodeID: exit, Capabilities: []string{"tunnel_authorization"}}}
		}
		address := addresses[exit]
		if _, err := netip.ParseAddr(address.Address); err != nil || address.PublicPort != nil {
			candidate.Required = append(candidate.Required, capabilityRequirement{NodeID: entry, AddressID: "public", Capabilities: []string{compatibility.IXAddressDialing}})
		}
		hop = append(hop, candidate)
	}
	required.Hops = append(required.Hops, hop)
	return required
}

func (p *Panel) ruleCapabilityPlans(ctx context.Context, q queryer, rules []forwarding.Rule, members map[string][]string) (map[[2]string]capabilityPlan, error) {
	caps, err := loadCapabilitySet(ctx, q)
	if err != nil {
		return nil, err
	}
	groups, err := loadExitGroups(ctx, q)
	if err != nil {
		return nil, err
	}
	byGroup := map[string]exitGroupView{}
	for _, g := range groups {
		byGroup[g.ID] = g
	}
	legacy, err := p.legacyTunnelAuthorizations(ctx, q, rules, members)
	if err != nil {
		return nil, err
	}
	exits, err := loadTunnelExits(ctx, q)
	if err != nil {
		return nil, err
	}
	addresses := map[string]tunnelExitView{}
	for _, v := range exits {
		addresses[v.NodeID] = v
	}
	plans := map[[2]string]capabilityPlan{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, entry := range forwarding.RuleEntryNodes(r, members) {
			plans[[2]string{entry, r.ID}] = caps.evaluate(ruleCapabilityRequirements(r, entry, byGroup[r.ExitGroupID], legacy, addresses))
		}
	}
	return plans, nil
}

func attachCapabilitySync(views []ruleView, plans map[[2]string]capabilityPlan) {
	for i := range views {
		v := &views[i]
		v.ConfigurationBlockers = []configurationBlocker{}
		statuses := []string{}
		seen := map[configurationBlocker]bool{}
		for j := range v.SyncMembers {
			m := &v.SyncMembers[j]
			m.ConfigurationBlockers = []configurationBlocker{}
			if v.Enabled {
				plan := plans[[2]string{m.NodeID, v.ID}]
				m.ConfigurationBlockers = plan.Blockers
				if plan.Blocked {
					m.SyncStatus = syncCapabilityBlocked
				} else if len(plan.Blockers) > 0 {
					switch m.SyncStatus {
					case "正常":
						m.SyncStatus = syncCapabilityDegraded
					case "未同步":
						m.SyncStatus = syncCapabilityPending
					case "待确认":
						m.SyncStatus = syncCapabilityUnconfirmed
					}
				}
				for _, b := range plan.Blockers {
					if !seen[b] {
						v.ConfigurationBlockers = append(v.ConfigurationBlockers, b)
						seen[b] = true
					}
				}
			}
			statuses = append(statuses, m.SyncStatus)
		}
		v.SyncStatus = aggregateSync(statuses)
	}
}
