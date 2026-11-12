package forwarding

import (
	"context"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"time"
)

const ChainMaxCandidates = 3
const ChainMaxEstablishmentBudget = 10 * time.Second

// ChainFailure contains bounded, public provenance only. Never forward response
// bodies or credentials. Rule and grant generations must both match the local
// authenticated snapshot before this evidence is usable for health or retries.
type ChainFailure struct {
	FromNodeID           string `json:"from_node_id"`
	ToNodeID             string `json:"to_node_id,omitempty"`
	AddressID            string `json:"address_id,omitempty"`
	HopIndex             uint32 `json:"hop_index"`
	RuleGeneration       uint64 `json:"rule_generation"`
	GrantGeneration      uint64 `json:"grant_generation"`
	ChainFingerprint     string `json:"chain_fingerprint"`
	EndpointConfigDigest string `json:"endpoint_config_digest,omitempty"`
}

// ChainBudget is shared across resolution, handshakes, target dialing and all
// retries. Incoming is the predecessor's remaining duration, not an absolute
// timestamp. A receiver rejects zero; an entry starts with the 10-second cap.
// time.Until uses the local monotonic component of the context deadline.
func ChainBudget(parent context.Context, localLimit, incoming time.Duration) (context.Context, context.CancelFunc) {
	budget := min(ChainMaxEstablishmentBudget, incoming)
	if localLimit > 0 {
		budget = min(budget, localLimit)
	}
	return context.WithTimeout(parent, budget)
}
func ChainRemainingBudgetMS(ctx context.Context) uint32 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remaining := min(time.Until(deadline), ChainMaxEstablishmentBudget)
	if remaining < time.Millisecond {
		return 0
	}
	return uint32(remaining / time.Millisecond)
}

// ValidateChainFailure checks evidence against panel-authored allowed edges.
// A terminal target has no node/address destination; its owning terminal node
// must be provable from the last authorized incoming edge.
func ValidateChainFailure(rule *controlpb.ForwardRule, class string, f *ChainFailure) bool {
	if rule.GetChainId() == "" || rule.GetAuthorizationGeneration() == 0 || f == nil {
		return false
	}
	auth := rule.GetChainAuthorization()
	if auth.GetFingerprint() == "" || f.ChainFingerprint != auth.GetFingerprint() || f.RuleGeneration != rule.AuthorizationGeneration || f.GrantGeneration != rule.AuthorizationGeneration {
		return false
	}
	if class != "entry_exit" && class != "downstream_next_hop" && class != "exit_target" && class != "local_resource" && class != "configuration_authorization" {
		return false
	}
	maxHop := uint32(0)
	for _, edge := range auth.GetEdges() {
		maxHop = max(maxHop, edge.HopIndex)
	}
	if f.ToNodeID == "" {
		if f.AddressID != "" || f.EndpointConfigDigest != "" || (class == "exit_target" && f.HopIndex != maxHop) {
			return false
		}
		if class != "exit_target" && class != "local_resource" && class != "configuration_authorization" {
			return false
		}
		for _, edge := range auth.GetEdges() {
			if edge.HopIndex == f.HopIndex && edge.ToNodeId == f.FromNodeID {
				return true
			}
		}
		return false
	}
	if class == "exit_target" {
		return false
	}
	for _, edge := range auth.GetEdges() {
		if edge.FromNodeId == f.FromNodeID && edge.ToNodeId == f.ToNodeID && edge.AddressId == f.AddressID && edge.HopIndex == f.HopIndex && edge.EndpointConfigDigest == f.EndpointConfigDigest {
			return true
		}
	}
	return false
}

// Called only before stream establishment and before any business bytes. A
// downstream/target failure is retryable without blaming this healthy edge.
func chainRetryable(class failureClass) bool {
	return class == failureLink || class == failureDownstream || class == failureTarget
}
func upstreamChainResult(result tunnelResult) tunnelResult {
	if result.Failure == failureLink {
		result.Failure = failureDownstream
	}
	return result
}

// ChainEvidenceEdges retains the current authenticated incoming edge and only
// the suffix reachable from this node. Sibling relays do not acquire each
// other's error authority merely because they have the same hop index.
func ChainEvidenceEdges(edges []*controlpb.ChainEdgeAuthorization, node string, hop uint32, previous string) []*controlpb.ChainEdgeAuthorization {
	out := []*controlpb.ChainEdgeAuthorization{}
	reachable := map[string]uint32{node: hop}
	maxHop := hop
	for _, edge := range edges {
		maxHop = max(maxHop, edge.HopIndex)
	}
	for i := hop; i <= maxHop; i++ {
		for _, edge := range edges {
			incoming := i == hop && edge.HopIndex == hop && edge.FromNodeId == previous && edge.ToNodeId == node
			before, ok := reachable[edge.FromNodeId]
			outgoing := edge.HopIndex == i && ok && edge.HopIndex == before+1
			if incoming || outgoing {
				out = append(out, proto.Clone(edge).(*controlpb.ChainEdgeAuthorization))
				if outgoing {
					reachable[edge.ToNodeId] = edge.HopIndex
				}
			}
		}
	}
	return out
}

// ValidateChainFailureFromNext is the remote-response boundary. Pass the exact
// endpoint selected for this attempt, including its authenticated predecessor
// and configuration digest; membership somewhere in the full chain is not enough.
func ValidateChainFailureFromNext(rule *controlpb.ForwardRule, selected *controlpb.TunnelEndpoint, class string, f *ChainFailure) bool {
	if selected == nil || rule == nil || !ValidateChainFailure(rule, class, f) {
		return false
	}
	hop := rule.HopIndex + 1
	found := false
	for _, edge := range rule.GetChainAuthorization().GetEdges() {
		if edge.HopIndex == hop && edge.FromNodeId == selected.EntryId && edge.ToNodeId == selected.NodeId && edge.AddressId == selected.AddressId && edge.EndpointConfigDigest == selected.EndpointConfigDigest {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	scoped := proto.Clone(rule).(*controlpb.ForwardRule)
	scoped.ChainAuthorization.Edges = ChainEvidenceEdges(rule.ChainAuthorization.Edges, selected.NodeId, hop, selected.EntryId)
	return ValidateChainFailure(scoped, class, f)
}
