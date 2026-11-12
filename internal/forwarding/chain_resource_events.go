package forwarding

import (
	"encoding/json"
	"nbpass/internal/eventlog"
	"unicode/utf8"
)

// Match the existing durable event queue budget. Origins are pending delivery,
// not an unbounded history of every process that ever owned an edge.
const chainResourceCandidateCount = 4096
const chainResourceCandidateBytes = 4 << 20
const ChainResourceGapMessage = "链式资源证据存在缺口"

// Scope summaries are independent of origin candidates and bounded across all
// pending configurations. Extra unknown attribution remains an ordinary gap.
const ChainResourceScopeCount = 4096
const ChainResourceScopeBytes = 2 << 20

type ChainResourceScope struct {
	RuleID      string `json:"rule_id"`
	Generation  uint64 `json:"generation"`
	Fingerprint string `json:"fingerprint"`
}

func (s ChainResourceScope) Valid() bool {
	return s.RuleID != "" && len(s.RuleID) <= 128 && utf8.ValidString(s.RuleID) && s.Generation > 0 && s.Generation <= 1<<63-1 && s.Fingerprint != "" && len(s.Fingerprint) <= 64 && utf8.ValidString(s.Fingerprint)
}
func ResourceScope(o ChainFailureObservation) ChainResourceScope {
	return ChainResourceScope{RuleID: o.RuleID, Generation: o.Edge.RuleGeneration, Fingerprint: o.Edge.ChainFingerprint}
}
func (s ChainResourceScope) Message() string {
	raw, _ := json.Marshal(struct {
		Message string `json:"chain_resource_gap"`
		ChainResourceScope
	}{ChainResourceGapMessage, s})
	return string(raw)
}
func DecodeChainResourceScope(message string) (ChainResourceScope, bool) {
	var v struct {
		Message string `json:"chain_resource_gap"`
		ChainResourceScope
	}
	if json.Unmarshal([]byte(message), &v) != nil || v.Message != ChainResourceGapMessage || !v.ChainResourceScope.Valid() {
		return ChainResourceScope{}, false
	}
	return v.ChainResourceScope, true
}

// Gap is the total loss, while each scope and Unscoped partition that count.
// Neither a later snapshot nor an unrelated rule can acquire a stored scope.
type ChainResourceGaps struct {
	eventlog.Gap
	Scopes   map[ChainResourceScope]eventlog.Gap
	Unscoped eventlog.Gap
	bytes    int
}

func (g *ChainResourceGaps) Add(scope ChainResourceScope, loss eventlog.Gap) {
	if loss.Count == 0 {
		return
	}
	mergeResourceGap(&g.Gap, loss)
	previous, exists := g.Scopes[scope]
	// Reserve the maximum encoded count/time fields so an existing scope can
	// always advance its loss boundary even at capacity.
	size := 0
	if !exists {
		size = len(scope.Message()) + 256
	}
	if !scope.Valid() || (!exists && len(g.Scopes) >= ChainResourceScopeCount) || g.bytes+size > ChainResourceScopeBytes {
		mergeResourceGap(&g.Unscoped, loss)
		return
	}
	mergeResourceGap(&previous, loss)
	if g.Scopes == nil {
		g.Scopes = map[ChainResourceScope]eventlog.Gap{}
	}
	g.Scopes[scope] = previous
	g.bytes += size
}
func (g *ChainResourceGaps) Merge(other ChainResourceGaps) {
	for scope, loss := range other.Scopes {
		g.Add(scope, loss)
	}
	g.Add(ChainResourceScope{}, other.Unscoped)
}

// ChainDeliveryKey keeps identities that only Panel can compare distinct. It
// does not change the rule/edge recovery identity used by Panel.
func ChainDeliveryKey(o ChainFailureObservation) string {
	key := ChainObservationKey(o)
	if o.ResourceOrder != nil {
		key += "\x00" + o.ResourceOrder.Instance
	}
	return key
}
func resourceObservationBytes(o ChainFailureObservation) int {
	raw, _ := json.Marshal(o)
	return len(raw)
}
func mergeResourceGap(a *eventlog.Gap, b eventlog.Gap) {
	if b.Count == 0 {
		return
	}
	if a.Count == 0 {
		*a = b
		return
	}
	a.Count += b.Count
	if b.From.Before(a.From) {
		a.From = b.From
	}
	if b.To.After(a.To) {
		a.To = b.To
	}
}
func (e *Engine) recordResourceCandidate(o ChainFailureObservation) {
	if e.chainResources == nil {
		e.chainResources = map[string]ChainFailureObservation{}
	}
	key := ChainDeliveryKey(o)
	previous, exists := e.chainResources[key]
	if exists && previous.ResourceOrder.Sequence >= o.ResourceOrder.Sequence {
		return
	}
	size := resourceObservationBytes(o)
	previousSize := 0
	if exists {
		previousSize = resourceObservationBytes(previous)
	}
	if size > 2048 || (!exists && len(e.chainResources) >= chainResourceCandidateCount) || e.chainResourceBytes-previousSize+size > chainResourceCandidateBytes {
		e.chainResourceGap.Add(ResourceScope(o), eventlog.Gap{Count: 1, From: o.ObservedAt, To: o.ObservedAt, Reason: "overflow"})
		return
	}
	e.chainResources[key] = o
	e.chainResourceBytes += size - previousSize
}

// FlushChainResourceEvidence hands a consistent bounded snapshot to durable
// delivery. Only a successful callback acknowledges candidates. Newer evidence
// arriving during delivery survives; a failed write restores the exact gap.
func (e *Engine) FlushChainResourceEvidence(deliver func([]ChainFailureObservation, *ChainResourceGaps) error) error {
	e.chainResourceFlushMu.Lock()
	defer e.chainResourceFlushMu.Unlock()
	e.chainFailuresMu.Lock()
	candidates := make([]ChainFailureObservation, 0, len(e.chainResources))
	for _, o := range e.chainResources {
		candidates = append(candidates, o)
	}
	outcomes := append([]ChainFailureObservation{}, candidates...)
	for _, o := range e.chainFailures {
		outcomes = append(outcomes, o)
	}
	gap := e.chainResourceGap
	e.chainResourceGap = ChainResourceGaps{}
	e.chainFailuresMu.Unlock()
	var lost *ChainResourceGaps
	if gap.Count > 0 {
		lost = &gap
	}
	err := deliver(outcomes, lost)
	e.chainFailuresMu.Lock()
	defer e.chainFailuresMu.Unlock()
	if err != nil {
		e.chainResourceGap.Merge(gap)
		return err
	}
	for _, o := range candidates {
		key := ChainDeliveryKey(o)
		if current, ok := e.chainResources[key]; ok && current.Sequence == o.Sequence {
			delete(e.chainResources, key)
			e.chainResourceBytes -= resourceObservationBytes(current)
		}
	}
	return nil
}
