package forwarding

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

func hasTerminalGrants(server *controlpb.TunnelServer) bool {
	for _, g := range server.GetGrants() {
		if g.NextExitGroupId == "" {
			return true
		}
	}
	return false
}
func (e *Engine) setExitGroups(groups map[string]*controlpb.ExitGroup) {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	e.exitGroups = map[string]*controlpb.ExitGroup{}
	for id, g := range groups {
		e.exitGroups[id] = proto.Clone(g).(*controlpb.ExitGroup)
	}
}
func (s *tunnelServer) streamGrant(auth tunnelAuth, request tunnelRequest) *controlpb.TunnelGrant {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	for _, g := range s.config.Grants {
		if g.EntryId == auth.EntryID && g.Rule.Id == request.RuleID && matchStreamAuthorization(g.Rule, auth, request) != nil {
			return proto.Clone(g).(*controlpb.TunnelGrant)
		}
	}
	return nil
}
func grantAllowsSelected(g *controlpb.TunnelGrant, request tunnelRequest) bool {
	if request.selectedNext == nil {
		return true
	}
	if g.NextExitGroupId != request.nextGroup {
		return false
	}
	selected := request.selectedNext.Load()
	if selected == nil {
		return true
	}
	for _, id := range g.AuthorizedNextNodeIds {
		if id == selected.NodeId {
			return true
		}
	}
	return false
}
func (e *Engine) relaySelection(g *controlpb.TunnelGrant, selected *atomic.Pointer[controlpb.TunnelEndpoint]) *tunnelSelection {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	rule := proto.Clone(g.Rule).(*controlpb.ForwardRule)
	rule.ExitGroupId = g.NextExitGroupId
	rule.AuthorizedExitIds = append([]string{}, g.AuthorizedNextNodeIds...)
	selection := e.selectTunnel(rule, e.exitGroups[g.NextExitGroupId])
	selection.selected = selected
	return selection
}
func chainEdgeFailure(rule *controlpb.ForwardRule, ep *controlpb.TunnelEndpoint) *ChainFailure {
	return &ChainFailure{FromNodeID: ep.EntryId, ToNodeID: ep.NodeId, AddressID: ep.AddressId, HopIndex: nextChainHop(rule), RuleGeneration: rule.AuthorizationGeneration, GrantGeneration: rule.AuthorizationGeneration, ChainFingerprint: rule.GetChainAuthorization().GetFingerprint(), EndpointConfigDigest: ep.EndpointConfigDigest}
}
func (s *tunnelServer) chainLocalResult(rule *controlpb.ForwardRule, class failureClass) tunnelResult {
	result := tunnelResult{Failure: class}
	if rule.GetChainId() != "" {
		result.Edge = &ChainFailure{FromNodeID: s.engine.deps.NodeID, HopIndex: rule.HopIndex, RuleGeneration: rule.AuthorizationGeneration, GrantGeneration: rule.AuthorizationGeneration, ChainFingerprint: rule.GetChainAuthorization().GetFingerprint()}
	}
	return result
}

// Local grant installation is independent of subsequent-path readiness. The
// latter is established by 06c; a relay cannot claim its targets were probed.
func (e *Engine) buildGrantResults(revision uint64, server *controlpb.TunnelServer) []*controlpb.TunnelGrantApplyResult {
	results := []*controlpb.TunnelGrantApplyResult{}
	for _, g := range server.GetGrants() {
		r := &controlpb.TunnelGrantApplyResult{EntryId: g.EntryId, RuleId: g.Rule.Id, Generation: g.Rule.AuthorizationGeneration, AppliedRevision: revision}
		if e.tunnelServer == nil {
			r.ApplyError = e.tunnelError
		}
		if g.NextExitGroupId == "" && e.outboundBinding().err != nil {
			r.ApplyError = e.outboundBinding().err.Error()
		}
		if r.ApplyError != "" {
			r.AppliedRevision = 0
			r.ApplyError = ErrorSummary(r.ApplyError, 1024)
		}
		results = append(results, r)
	}
	return results
}
func (e *Engine) setGrantResults(results []*controlpb.TunnelGrantApplyResult) {
	e.grantResultsMu.Lock()
	defer e.grantResultsMu.Unlock()
	e.grantResults = results
}
func failedGrantResults(results []*controlpb.TunnelGrantApplyResult, err error) []*controlpb.TunnelGrantApplyResult {
	for _, r := range results {
		r.AppliedRevision = 0
		r.ApplyError = ErrorSummary(err.Error(), 1024)
	}
	return results
}
func (e *Engine) TunnelGrantResults() []*controlpb.TunnelGrantApplyResult {
	e.grantResultsMu.Lock()
	defer e.grantResultsMu.Unlock()
	out := make([]*controlpb.TunnelGrantApplyResult, 0, len(e.grantResults))
	for _, r := range e.grantResults {
		out = append(out, proto.Clone(r).(*controlpb.TunnelGrantApplyResult))
	}
	return out
}

// ChainFailureObservation is a stream-establishment observation, separate from
// next-edge handshake health. Evidence remains bound to its exact generation.
type ChainFailureObservation struct {
	RuleID     string
	ChainID    string
	Class      string
	Edge       ChainFailure
	ObservedAt time.Time
}

func (e *Engine) recordChainFailure(rule *controlpb.ForwardRule, err error) {
	var failure *tunnelFailure
	if !errors.As(err, &failure) || failure.edge == nil {
		return
	}
	e.chainFailuresMu.Lock()
	defer e.chainFailuresMu.Unlock()
	current := e.chainRules[rule.Id]
	if current == nil || current.AuthorizationGeneration != rule.AuthorizationGeneration || current.GetChainAuthorization().GetFingerprint() != rule.GetChainAuthorization().GetFingerprint() {
		return
	}
	if e.chainFailures == nil {
		e.chainFailures = map[string]ChainFailureObservation{}
	}
	// One latest outcome per local rule; delayed old generations never replace new.
	previous := e.chainFailures[rule.Id]
	if previous.Edge.RuleGeneration > rule.AuthorizationGeneration {
		return
	}
	e.chainFailures[rule.Id] = ChainFailureObservation{RuleID: rule.Id, ChainID: rule.ChainId, Class: string(failure.class), Edge: *failure.edge, ObservedAt: e.deps.Now()}
}
func (e *Engine) ChainFailures() []ChainFailureObservation {
	e.chainFailuresMu.Lock()
	defer e.chainFailuresMu.Unlock()
	out := make([]ChainFailureObservation, 0, len(e.chainFailures))
	for _, f := range e.chainFailures {
		out = append(out, f)
	}
	return out
}

func chainEstablishmentContext(parent context.Context, e *Engine, request tunnelRequest, receivedAt time.Time) (context.Context, context.CancelFunc) {
	if request.ChainID != "" {
		return ChainBudget(parent, e.resources.limits.DialTimeout, time.Duration(request.RemainingBudgetMS)*time.Millisecond-time.Since(receivedAt))
	}
	return e.tunnelTimeout(parent, e.resources.limits.DialTimeout)
}
func validateChainGrant(g *controlpb.TunnelGrant, groups []*controlpb.ExitGroup) error {
	r := g.Rule
	if r.ChainId == "" {
		if g.NextExitGroupId != "" || len(g.AuthorizedNextNodeIds) > 0 || g.HopIndex != 0 {
			return errors.New("普通隧道不能包含链式下一跳")
		}
		return nil
	}
	if r.AuthorizationGeneration == 0 || r.HopIndex == 0 || g.HopIndex != r.HopIndex || r.GetChainAuthorization().GetFingerprint() == "" {
		return errors.New("链式授权身份无效")
	}
	incoming := false
	local := ""
	for _, edge := range r.GetChainAuthorization().GetEdges() {
		if edge.HopIndex == r.HopIndex && edge.FromNodeId == g.EntryId {
			incoming = true
			local = edge.ToNodeId
		}
	}
	if !incoming {
		return errors.New("链式授权缺少实际前驱边")
	}
	if g.NextExitGroupId == "" {
		for _, edge := range r.GetChainAuthorization().GetEdges() {
			if edge.HopIndex > r.HopIndex {
				return errors.New("中间跳不能恢复为最终目标拨号")
			}
		}
		if len(g.AuthorizedNextNodeIds) > 0 {
			return errors.New("末跳不能指定下一成员")
		}
		return nil
	}
	var group *controlpb.ExitGroup
	for _, candidate := range groups {
		if candidate.Id == g.NextExitGroupId {
			group = candidate
		}
	}
	if group == nil {
		return fmt.Errorf("链式下一组缺失: %s", g.NextExitGroupId)
	}
	seen := map[string]bool{}
	for _, id := range g.AuthorizedNextNodeIds {
		found := false
		for _, ep := range group.Exits {
			if ep.NodeId == id && ep.EntryId == local {
				for _, edge := range r.GetChainAuthorization().GetEdges() {
					if edge.FromNodeId == ep.EntryId && edge.ToNodeId == id && edge.HopIndex == r.HopIndex+1 && edge.AddressId == ep.AddressId && edge.EndpointConfigDigest == ep.EndpointConfigDigest {
						found = true
					}
				}
			}
		}
		if !found || seen[id] {
			return errors.New("链式下一成员不属于可信授权边")
		}
		seen[id] = true
	}
	return nil
}

// An online entry also owns outgoing member permission. A cached/offline next
// node cannot keep a stream alive after this local permission is withdrawn.
func (r *listenerRule) watchSelectedExit(ctx context.Context, cancel context.CancelFunc, stream net.Conn, selected *atomic.Pointer[tunnelClient]) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		timer := r.engine.deps.Clock.NewTimer(time.Second)
		defer func() { timer.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C():
				selectedClient := selected.Load()
				if selectedClient != nil {
					allowed := false
					for _, c := range r.selection.Load().clients {
						// applyTunnelClients retains this exact client only across
						// unchanged routes or explicit rotation transitions. Ordinary
						// material/route replacement closes and replaces the client.
						if c == selectedClient {
							allowed = true
							break
						}
					}
					if !allowed {
						cancel()
						stream.Close()
						return
					}
				}
				timer = r.engine.deps.Clock.NewTimer(time.Second)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (e *Engine) setChainRules(snapshot *controlpb.ConfigSnapshot) {
	e.chainFailuresMu.Lock()
	defer e.chainFailuresMu.Unlock()
	e.chainRules = map[string]*controlpb.ForwardRule{}
	rules := append([]*controlpb.ForwardRule{}, snapshot.Rules...)
	for _, g := range snapshot.GetTunnelServer().GetGrants() {
		rules = append(rules, g.Rule)
	}
	for _, r := range rules {
		if r.ChainId != "" {
			e.chainRules[r.Id] = proto.Clone(r).(*controlpb.ForwardRule)
		}
	}
	for id, old := range e.chainFailures {
		r := e.chainRules[id]
		if r == nil || r.AuthorizationGeneration != old.Edge.RuleGeneration || r.GetChainAuthorization().GetFingerprint() != old.Edge.ChainFingerprint {
			delete(e.chainFailures, id)
		}
	}
}
