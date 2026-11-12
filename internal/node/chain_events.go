package node

import (
	"context"
	"encoding/json"
	"nbpass/internal/eventlog"
	"nbpass/internal/forwarding"
	"sort"
)

// One coalesced latest transition per authorized edge per heartbeat, not a disk write on
// every connection. Unchanged observations are not queued again; events remain
// subject to the existing bounded persistent queue and backfill policy.
func recordChainEvents(ctx context.Context, deps Dependencies, e *forwarding.Engine, instance string, last map[string]string) error {
	if deps.Events == nil {
		return nil
	}
	return e.FlushChainResourceEvidence(func(failures []forwarding.ChainFailureObservation, gap *forwarding.ChainResourceGaps) error {
		return queueChainOutcomesWithGap(ctx, deps.Events, e.State(), instance, append(failures, e.ChainEstablishments()...), last, gap)
	})
}

func queueChainOutcomes(ctx context.Context, queue *EventQueue, state forwarding.State, instance string, outcomes []forwarding.ChainFailureObservation, last map[string]string) error {
	return queueChainOutcomesWithGap(ctx, queue, state, instance, outcomes, last, nil)
}
func queueChainOutcomesWithGap(ctx context.Context, queue *EventQueue, state forwarding.State, instance string, outcomes []forwarding.ChainFailureObservation, last map[string]string, gap *forwarding.ChainResourceGaps) error {
	// Keep the engine's delivery snapshot immutable: failed persistence retries
	// these oversized outcomes, rather than acknowledging/counting them twice.
	if gap != nil {
		copy := &forwarding.ChainResourceGaps{}
		copy.Merge(*gap)
		gap = copy
	}
	newest := latestChainOutcomes(outcomes)
	pending := map[string]string{}
	events := []eventlog.Event{}
	ids := []string{}
	for id := range newest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		o := newest[id]
		raw, err := json.Marshal(forwarding.ChainEvent{ProcessInstance: instance, SnapshotDigest: state.HighestDigest, Observation: o})
		if err != nil {
			return err
		}
		// A newly observed outcome (even the same class) or current snapshot
		// can recover an upstream report that arrived between our heartbeats.
		// Keep the real observation time; a heartbeat never creates success.
		key := string(raw)
		if last[id] == key {
			continue
		}
		if len(raw) > 2048 {
			if o.ResourceOrder != nil {
				if gap == nil {
					gap = &forwarding.ChainResourceGaps{}
				}
				gap.Add(forwarding.ResourceScope(o), eventlog.Gap{Count: 1, From: o.ObservedAt, To: o.ObservedAt, Reason: "overflow"})
			}
			continue
		}
		events = append(events, eventlog.Event{Type: "chain_flow", Message: string(raw), ConfigRevision: state.HighestRevision, OccurredAt: queue.clock.Now()})
		pending[id] = key
	}
	if err := queue.recordChainBatch(ctx, events, gap); err != nil {
		return err
	}
	for id, key := range pending {
		last[id] = key
	}
	for id := range last {
		if _, ok := newest[id]; !ok {
			delete(last, id)
		}
	}
	return nil
}

func latestChainOutcomes(outcomes []forwarding.ChainFailureObservation) map[string]forwarding.ChainFailureObservation {
	sort.SliceStable(outcomes, func(i, j int) bool {
		if outcomes[i].ObservedAt.Equal(outcomes[j].ObservedAt) {
			return outcomes[i].Sequence < outcomes[j].Sequence
		}
		return outcomes[i].ObservedAt.Before(outcomes[j].ObservedAt)
	})
	newest := map[string]forwarding.ChainFailureObservation{}
	for _, o := range outcomes {
		key := forwarding.ChainDeliveryKey(o)
		if previous, ok := newest[key]; ok && o.ResourceOrder != nil && previous.ResourceOrder != nil && previous.ResourceOrder.Sequence >= o.ResourceOrder.Sequence {
			continue
		}
		newest[key] = o
	}
	return newest
}
