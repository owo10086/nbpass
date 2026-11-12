package forwarding

import "encoding/json"

// ChainEvent is bounded public evidence carried by the existing event channel.
// It is never authorization and never substitutes for each edge's probe.
type ChainEvent struct {
	// Retained, AcceptedSuccess and Historical are panel-owned receipt decisions. Node
	// input is always cleared and recomputed before persistence.
	Retained        bool                    `json:"retained,omitempty"`
	AcceptedSuccess bool                    `json:"accepted_success,omitempty"`
	Historical      bool                    `json:"historical,omitempty"`
	ProcessInstance string                  `json:"process_instance"`
	SnapshotDigest  string                  `json:"snapshot_digest"`
	Observation     ChainFailureObservation `json:"observation"`
}

// ChainObservationKey scopes a transition to the actual edge and authorization.
// Outcome class/time do not identify an edge. Local resource outcomes have
// their own scope, so local recovery cannot erase a target or network failure.
func ChainObservationKey(o ChainFailureObservation) string {
	data, _ := json.Marshal(struct {
		Rule          string
		Edge          ChainFailure
		LocalResource bool
	}{o.RuleID, o.Edge, o.Edge.ToNodeID == "" && (o.Class == "local_resource" || o.Class == "local_established")})
	return string(data)
}

// IsChainEstablishment distinguishes actual business success from failures and
// probe results. Local success recovers only the resource scope of its owner.
func IsChainEstablishment(class string) bool {
	return class == "established" || class == "local_established"
}

// ChainResourceOrder identifies the actual resource owner's process and order.
// It adds no authorization: the receiver must still validate the selected
// downstream suffix and Panel must match the owner's current process instance.
type ChainResourceOrder struct {
	Instance string `json:"instance"`
	Sequence uint64 `json:"sequence"`
}

func ValidChainResourceOrder(order *ChainResourceOrder, class string, edge *ChainFailure) bool {
	if order == nil {
		return true
	}
	return len(order.Instance) > 0 && len(order.Instance) <= 128 && order.Sequence > 0 &&
		(class == "local_resource" || class == "local_established") && edge != nil && edge.FromNodeID != "" && edge.ToNodeID == ""
}

// Called only after carrier authentication when the node-wide gate refuses a
// stream. It does not read a request or allocate a stream worker/quota slot.
func (e *Engine) nextChainResourceOrder() *ChainResourceOrder {
	e.chainFailuresMu.Lock()
	defer e.chainFailuresMu.Unlock()
	e.chainObservationSequence++
	return e.chainResourceOrder(e.chainObservationSequence)
}
func (e *Engine) chainResourceOrder(sequence uint64) *ChainResourceOrder {
	if e.deps.ProcessInstance == "" {
		return nil
	}
	return &ChainResourceOrder{Instance: e.deps.ProcessInstance, Sequence: sequence}
}
