package panel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
	"nbpass/internal/forwarding"
	"time"
)

func decodeChainEvent(message string) (forwarding.ChainEvent, error) {
	var v forwarding.ChainEvent
	d := json.NewDecoder(bytes.NewBufferString(message))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return v, err
	}
	if d.Decode(new(any)) != io.EOF {
		return v, errors.New("invalid chain observation")
	}
	return v, nil
}

// Old configuration/process events are acknowledged but never become current
// evidence. The authenticated node may report only its own trusted suffix.
func (p *Panel) currentChainEvent(ctx context.Context, q queryer, node, instance string, e eventlog.Event) (bool, error) {
	v, err := decodeChainEvent(e.Message)
	if err != nil {
		return false, err
	}
	s, err := p.exitHealthSnapshot(ctx, q, node)
	if err != nil {
		return false, err
	}
	current, err := validChainEventSnapshot(v, s, node, instance, e)
	if err != nil || !current || v.Observation.ResourceOrder == nil {
		return current, err
	}
	var ownerInstance string
	err = operationRows(ctx, q, "SELECT process_instance FROM nodes WHERE id=?", func(rows *sql.Rows) error { return rows.Scan(&ownerInstance) }, v.Observation.Edge.FromNodeID)
	return err == nil && ownerInstance == v.Observation.ResourceOrder.Instance, err
}
func validChainEventSnapshot(v forwarding.ChainEvent, s *controlpb.ConfigSnapshot, node, instance string, e eventlog.Event) (bool, error) {
	if v.ProcessInstance != instance || s.GetRevision() != e.ConfigRevision || s.GetDigest() != v.SnapshotDigest {
		return false, nil
	}
	o := v.Observation
	if !forwarding.ValidChainResourceOrder(o.ResourceOrder, o.Class, &o.Edge) {
		return false, errors.New("invalid resource observation order")
	}
	if o.ResourceOrder != nil && o.Edge.FromNodeID == node && (o.ResourceOrder.Instance != instance || o.ResourceOrder.Sequence != o.Sequence) {
		return false, nil
	}
	if o.ObservedAt.IsZero() || o.ObservedAt.After(e.OccurredAt) {
		return false, errors.New("invalid chain observation time")
	}
	rules := append([]*controlpb.ForwardRule{}, s.Rules...)
	for _, g := range s.GetTunnelServer().GetGrants() {
		rules = append(rules, g.Rule)
	}
	for _, r := range rules {
		if r.Id != o.RuleID || r.ChainId != o.ChainID {
			continue
		}
		if o.Edge.ToNodeID != "" && o.Edge.HopIndex <= r.HopIndex {
			continue
		}
		class := o.Class
		if class == "entry_exit" && (o.Edge.FromNodeID != node || o.Edge.HopIndex != r.HopIndex+1) {
			continue
		}
		if class == "local_established" {
			if o.Edge.FromNodeID != node || o.Edge.ToNodeID != "" || o.Edge.HopIndex != r.HopIndex {
				return false, nil
			}
			class = "local_resource"
		}
		if class == "established" {
			class = "entry_exit"
			if o.Edge.ToNodeID == "" {
				class = "exit_target"
			}
			if o.Edge.FromNodeID != node || (o.Edge.ToNodeID != "" && o.Edge.HopIndex != r.HopIndex+1) || (o.Edge.ToNodeID == "" && o.Edge.HopIndex != r.HopIndex) {
				return false, nil
			}
		}
		if forwarding.ValidateChainFailure(r, class, &o.Edge) {
			return true, nil
		}
	}
	return false, nil
}

// Resource origin order is authoritative within the current owner process.
// Legacy evidence keeps the actual-owner priority at equal timestamps.
func newerChainOutcome(node string, next forwarding.ChainEvent, previousNode string, previous forwarding.ChainEvent) bool {
	a, b := next.Observation, previous.Observation
	if a.ResourceOrder != nil && b.ResourceOrder != nil {
		if a.ResourceOrder.Instance == b.ResourceOrder.Instance {
			return a.ResourceOrder.Sequence > b.ResourceOrder.Sequence
		}
		// The next event already passed the current owner-instance check. Sequence
		// values are unrelated across process restarts.
		return true
	}
	// A retained owner event from before resource-order metadata may belong
	// to a previous process. A verified current origin must not be blocked by
	// that old process's timestamp or owner priority.
	if a.ResourceOrder != nil && b.ResourceOrder == nil && previousNode == a.Edge.FromNodeID {
		if a.ResourceOrder.Instance != previous.ProcessInstance {
			return true
		}
		if b.Sequence > 0 {
			return a.ResourceOrder.Sequence > b.Sequence
		}
	}
	if !a.ObservedAt.Equal(b.ObservedAt) {
		return a.ObservedAt.After(b.ObservedAt)
	}
	local, previousLocal := node == a.Edge.FromNodeID, previousNode == a.Edge.FromNodeID
	if local != previousLocal {
		return local
	}
	if node == previousNode && next.ProcessInstance == previous.ProcessInstance {
		return a.Sequence > b.Sequence
	}
	return true
}

// Keep exactly one receipt baseline per actual authorized edge in the existing
// event store. Normal history remains bounded by retention; the current baseline
// survives history expiry until this authorization is replaced or removed.
func retainChainOutcome(ctx context.Context, tx *sql.Tx, node string, next *forwarding.ChainEvent) error {
	next.Retained = false
	if next.Historical || (forwarding.IsChainEstablishment(next.Observation.Class) && !next.AcceptedSuccess) {
		return nil
	}
	var oldSequence int64
	var oldNode string
	var old forwarding.ChainEvent
	err := operationRows(ctx, tx, `SELECT sequence,node_id,message FROM events WHERE type='chain_flow' AND source='node' AND json_extract(message,'$.retained')=1 AND json_extract(message,'$.observation.RuleID')=?`, func(rows *sql.Rows) error {
		var sequence int64
		var source, message string
		if err := rows.Scan(&sequence, &source, &message); err != nil {
			return err
		}
		v, err := decodeChainEvent(message)
		if err != nil {
			return err
		}
		if forwarding.ChainObservationKey(v.Observation) == forwarding.ChainObservationKey(next.Observation) {
			oldSequence, oldNode, old = sequence, source, v
		}
		return nil
	}, next.Observation.RuleID)
	if err != nil {
		return err
	}
	if oldSequence != 0 && !newerChainOutcome(node, *next, oldNode, old) {
		return nil
	}
	if oldSequence != 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE events SET message=json_remove(message,'$.retained') WHERE sequence=?", oldSequence); err != nil {
			return err
		}
	}
	next.Retained = true
	return nil
}

// The latest real establishment outcome does not replace edge readiness. A
// stale failure stays historical until a fresh real success; expiry is not a
// recovery signal and control loss never makes an incident disappear.
func (p *Panel) attachChainFlowEvidence(ctx context.Context, q queryer, r *operationRule, runtime chainRuntime, now time.Time) error {
	type observed struct {
		node    string
		event   forwarding.ChainEvent
		current bool
		receipt int64
	}
	latest := map[string]observed{}

	err := operationRows(ctx, q, `SELECT e.sequence,e.node_id,e.message,e.config_revision,e.occurred_at FROM events e JOIN rule_authorizations a ON a.rule_id=json_extract(e.message,'$.observation.RuleID') AND a.generation=json_extract(e.message,'$.observation.Edge.rule_generation') AND a.fingerprint=json_extract(e.message,'$.observation.Edge.chain_fingerprint') WHERE e.type='chain_flow' AND e.source='node' AND json_extract(e.message,'$.retained')=1 AND a.rule_id=? ORDER BY e.sequence DESC`, func(rows *sql.Rows) error {
		var node, message string
		var revision uint64
		var occurred, receipt int64
		if e := rows.Scan(&receipt, &node, &message, &revision, &occurred); e != nil {
			return e
		}
		v, e := decodeChainEvent(message)
		if e != nil {
			return e
		}
		o := v.Observation
		if v.Historical || o.RuleID != r.ID || o.Edge.ChainFingerprint != r.ChainFingerprint {
			return nil
		}
		current, checkErr := validChainEventSnapshot(v, runtime.inputs[node].snapshot, node, runtime.instances[node], eventlog.Event{Message: message, ConfigRevision: revision, OccurredAt: time.Unix(0, occurred)})
		if checkErr != nil {
			return checkErr
		}
		if o.ResourceOrder != nil && runtime.instances[o.Edge.FromNodeID] != o.ResourceOrder.Instance {
			current = false
		}
		// Only a receipt decision made after current-session/snapshot and freshness
		// verification can resolve an edge. That durable decision survives expiry.
		if forwarding.IsChainEstablishment(o.Class) && !v.AcceptedSuccess {
			return nil
		}
		key := forwarding.ChainObservationKey(o)
		previous, ok := latest[key]
		newer := !ok || newerChainOutcome(node, v, previous.node, previous.event)
		if newer {
			latest[key] = observed{node: node, event: v, current: current, receipt: receipt}
		}
		return nil
	}, r.ID)
	if err != nil {
		return err
	}
	// A bounded buffer overflow is incomplete evidence, never recovery. Keep
	// the newest gap per reporter, queue reason and lost authorization. The
	// current snapshot only validates that captured scope; it never creates it.
	// Recovery still needs real successes from the complete authorized suffix.
	seenGaps := map[string]bool{}
	if err := operationRows(ctx, q, `SELECT e.sequence,e.node_id,e.gap,e.message FROM events e JOIN rule_authorizations a
 ON a.rule_id=json_extract(CASE WHEN json_valid(e.message) THEN e.message ELSE '{}' END,'$.rule_id')
 AND a.generation=json_extract(CASE WHEN json_valid(e.message) THEN e.message ELSE '{}' END,'$.generation')
 AND a.fingerprint=json_extract(CASE WHEN json_valid(e.message) THEN e.message ELSE '{}' END,'$.fingerprint')
 WHERE e.type='event_gap' AND e.source='node' AND a.rule_id=? AND a.fingerprint=? ORDER BY e.sequence DESC`, func(rows *sql.Rows) error {
		var sequence int64
		var reporter, raw, message string
		if err := rows.Scan(&sequence, &reporter, &raw, &message); err != nil {
			return err
		}
		var gap eventlog.Gap
		if err := json.Unmarshal([]byte(raw), &gap); err != nil {
			return err
		}
		scope, ok := forwarding.DecodeChainResourceScope(message)
		if !ok {
			return nil
		}
		key := reporter + ":" + gap.Reason + ":" + scope.Message()
		if seenGaps[key] {
			return nil
		}
		seenGaps[key] = true
		snapshot := runtime.inputs[reporter].snapshot
		rules := append([]*controlpb.ForwardRule{}, snapshot.GetRules()...)
		for _, grant := range snapshot.GetTunnelServer().GetGrants() {
			rules = append(rules, grant.Rule)
		}
		owners := map[string]bool{}
		for _, rule := range rules {
			if rule.Id == scope.RuleID && rule.AuthorizationGeneration == scope.Generation && rule.GetChainAuthorization().GetFingerprint() == scope.Fingerprint {
				if rule.HopIndex > 0 {
					owners[reporter] = false
				}
				for _, edge := range rule.GetChainAuthorization().GetEdges() {
					owners[edge.ToNodeId] = false
				}
			}
		}
		if len(owners) == 0 {
			return nil
		}
		for _, value := range latest {
			o := value.event.Observation
			if o.Class == "local_established" && value.event.AcceptedSuccess && value.receipt > sequence && o.ObservedAt.After(gap.To) {
				if _, ok := owners[o.Edge.FromNodeID]; ok {
					owners[o.Edge.FromNodeID] = true
				}
			}
		}
		for _, recovered := range owners {
			if !recovered {
				at := gap.To
				r.Reasons = append(r.Reasons, operationReason{Code: "chain_resource_gap", Message: "链式资源证据存在缺口，尚未收到完整后续成功证据", Source: "链式建流", NodeID: reporter, At: &at})
				if r.Enabled && r.Status != "abnormal" {
					r.Status = "unknown"
				}
				break
			}
		}
		return nil
	}, r.ID, r.ChainFingerprint); err != nil {
		return err
	}
	for _, value := range latest {
		o := value.event.Observation
		if forwarding.IsChainEstablishment(o.Class) {
			continue
		}
		node := value.node
		at := o.ObservedAt
		fresh := value.current && runtime.inputs[node].sessionLive && !now.Before(at) && now.Sub(at) < 15*time.Second
		code, message := "chain_flow_history", "最近真实建流失败为历史证据，尚无当前成功建流证据"
		if fresh {
			code = "chain_flow_failed"
			message = "真实建流失败：" + o.Class
		}
		r.Reasons = append(r.Reasons, operationReason{Code: code, Message: message, Source: "链式建流", NodeID: node, At: &at, Failure: &o})
		if r.Enabled {
			if r.Status != "abnormal" {
				r.Status = "unknown"
			}
			if fresh {
				r.Status = "abnormal"
			}
		}
	}
	return nil
}
