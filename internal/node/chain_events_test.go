package node

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"nbpass/internal/eventlog"
	"nbpass/internal/forwarding"
	"testing"
	"time"
)

func TestChainEventsSameTimestampKeepsNewestRealOutcomePerEdge(t *testing.T) {
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, nextClass := range []string{"entry_exit", "established"} {
		t.Run(nextClass, func(t *testing.T) {
			first := forwarding.ChainFailureObservation{Sequence: 1, RuleID: "r", ChainID: "chain", Class: "established", ObservedAt: at, Edge: forwarding.ChainFailure{FromNodeID: "a", ToNodeID: "b", AddressID: "ix", RuleGeneration: 1, GrantGeneration: 1}}
			next := first
			next.Sequence, next.Class = 2, nextClass
			if nextClass == "established" {
				first.Class = "entry_exit"
			}
			other := first
			other.Edge.ToNodeID = "c"
			other.Class = "entry_exit"
			// Engine returns failures then successes, independently of occurrence order.
			values := []forwarding.ChainFailureObservation{next, first, other}
			if nextClass == "established" {
				values = []forwarding.ChainFailureObservation{first, other, next}
			}
			latest := latestChainOutcomes(values)
			if len(latest) != 2 {
				t.Fatal("one edge erased another", latest)
			}
			for _, got := range latest {
				if got.Edge.ToNodeID == "b" && (got.Sequence != 2 || got.Class != nextClass) {
					t.Fatal("same timestamp reordered the actual transition", got)
				}
				if got.Edge.ToNodeID == "c" && got.Class != "entry_exit" {
					t.Fatal("unrelated edge recovered", got)
				}
			}
		})
	}
}

func TestChainEventsRefreshNewOutcomesAndSnapshotWithoutPerConnectionWrites(t *testing.T) {
	ctx := context.Background()
	q, _ := queueOnDisk(t, EventLimits{Count: 2, Bytes: 4096})
	state := forwarding.State{HighestRevision: 1, HighestDigest: "first"}
	last := map[string]string{}
	base := forwarding.ChainFailureObservation{Sequence: 1, RuleID: "r", Class: "established", ObservedAt: time.Now(), Edge: forwarding.ChainFailure{FromNodeID: "b", ToNodeID: "c", AddressID: "ix", RuleGeneration: 1, GrantGeneration: 1}}
	send := func(values []forwarding.ChainFailureObservation) {
		t.Helper()
		if err := queueChainOutcomes(ctx, q, state, "process", values, last); err != nil {
			t.Fatal(err)
		}
	}
	send([]forwarding.ChainFailureObservation{base})
	for i := 0; i < 10; i++ {
		send([]forwarding.ChainFailureObservation{base})
	}
	if len(q.state.Events) != 1 {
		t.Fatal("unchanged heartbeat wrote duplicate events", len(q.state.Events))
	}
	values := []forwarding.ChainFailureObservation{}
	for i := 2; i <= 100; i++ {
		v := base
		v.Sequence = uint64(i)
		values = append(values, v)
	}
	send(values)
	if len(q.state.Events) != 2 {
		t.Fatal("latest real success was suppressed or each connection was queued", len(q.state.Events))
	}
	var got forwarding.ChainEvent
	if err := json.Unmarshal([]byte(q.state.Events[1].Message), &got); err != nil || got.Observation.Sequence != 100 {
		t.Fatal("did not persist latest coalesced result", got, err)
	}
	state.HighestRevision, state.HighestDigest = 2, "current"
	send(values)
	if len(q.state.Events) != 2 || q.state.Events[1].ConfigRevision != 2 {
		t.Fatal("current snapshot did not refresh unaccepted observation", q.state.Events)
	}
	if len(q.state.Pending) != 1 {
		t.Fatal("persistent queue limit was bypassed")
	}
}

func TestChainEventsDifferentResourceOriginsReachPersistentQueue(t *testing.T) {
	ctx := context.Background()
	q, _ := queueOnDisk(t, EventLimits{})
	state := forwarding.State{HighestRevision: 1, HighestDigest: "digest"}
	at := time.Now()
	values := []forwarding.ChainFailureObservation{}
	for i, instance := range []string{"current", "old-1", "old-2", "old-3"} {
		values = append(values, forwarding.ChainFailureObservation{Sequence: uint64(i + 1), ResourceOrder: &forwarding.ChainResourceOrder{Instance: instance, Sequence: 1}, RuleID: "r", ChainID: "chain", Class: "local_resource", ObservedAt: at, Edge: forwarding.ChainFailure{FromNodeID: "owner", RuleGeneration: 7, GrantGeneration: 7, ChainFingerprint: "fp"}})
	}
	if err := queueChainOutcomes(ctx, q, state, "relay", values, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	batch, err := q.Batch(ctx)
	if err != nil || len(batch) != 4 {
		t.Fatal("node coalescing discarded a different origin before Panel verification", len(batch), err)
	}
}

func TestChainEventsResourceBatchAtomicGapRetryAndReopen(t *testing.T) {
	ctx := context.Background()
	q, disk := queueOnDisk(t, EventLimits{Count: 1, Bytes: 4096})
	failing := &failedEventDisk{FileStore: disk, fail: true}
	q.store = failing
	at := q.clock.Now()
	values := []forwarding.ChainFailureObservation{}
	for i, instance := range []string{"current", "old"} {
		values = append(values, forwarding.ChainFailureObservation{Sequence: uint64(i + 1), ResourceOrder: &forwarding.ChainResourceOrder{Instance: instance, Sequence: 1}, RuleID: "r", Class: "local_resource", ObservedAt: at, Edge: forwarding.ChainFailure{FromNodeID: "owner", RuleGeneration: 7, ChainFingerprint: "fp"}})
	}
	state := forwarding.State{HighestRevision: 1, HighestDigest: "digest"}
	last := map[string]string{}
	gap := &forwarding.ChainResourceGaps{}
	gap.Add(forwarding.ChainResourceScope{RuleID: "r", Generation: 7, Fingerprint: "fp"}, eventlog.Gap{Count: 2, From: at.Add(-2 * time.Second), To: at.Add(-time.Second), Reason: "overflow"})
	if err := queueChainOutcomesWithGap(ctx, q, state, "relay", values, last, gap); err == nil {
		t.Fatal("failed disk write reported delivery")
	}
	if len(last) != 0 || len(q.state.Events) != 0 || len(q.state.Pending) != 0 {
		t.Fatal("failed batch changed acknowledgement or queue")
	}
	failing.fail = false
	if err := queueChainOutcomesWithGap(ctx, q, state, "relay", values, last, gap); err != nil {
		t.Fatal(err)
	}
	q2, err := OpenEventQueue(ctx, disk, q.clock, rand.Reader, EventLimits{Count: 1, Bytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := q2.Batch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || batch[0].Message != (forwarding.ChainResourceScope{RuleID: "r", Generation: 7, Fingerprint: "fp"}).Message() || batch[0].Gap == nil || batch[0].Gap.Count != 3 || !batch[0].Gap.From.Equal(gap.From) || batch[0].Gap.To.Before(at) {
		t.Fatal("bounded queue lost exact durable overflow", batch)
	}
	// An unacknowledged batch must survive restart with the same IDs and gap.
	q3, err := OpenEventQueue(ctx, disk, q.clock, rand.Reader, EventLimits{Count: 1, Bytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	again, err := q3.Batch(ctx)
	if err != nil || len(again) != 2 || again[0].ID != batch[0].ID || again[0].Gap.Count != 3 {
		t.Fatal("restart rewrote/lost pending gap", again, err)
	}
	if err := q3.Ack(ctx, []string{again[0].ID, again[1].ID}); err != nil {
		t.Fatal(err)
	}
	if err := queueChainOutcomes(ctx, q3, state, "relay", values, last); err != nil {
		t.Fatal(err)
	}
	empty, err := q3.Batch(ctx)
	if err != nil || len(empty) != 0 {
		t.Fatal("same delivered observations accumulated", empty, err)
	}
}

func TestChainEventsGapScopesUseBoundedMultipleMessagesAndAck(t *testing.T) {
	ctx := context.Background()
	q, disk := queueOnDisk(t, EventLimits{})
	gap := &forwarding.ChainResourceGaps{}
	at := q.clock.Now()
	for i := 0; i < 300; i++ {
		gap.Add(forwarding.ChainResourceScope{RuleID: fmt.Sprint("rule-", i), Generation: 1, Fingerprint: "fp"}, eventlog.Gap{Count: 1, From: at, To: at, Reason: "overflow"})
	}
	if err := q.recordChainBatch(ctx, nil, gap); err != nil {
		t.Fatal(err)
	}
	q, err := OpenEventQueue(ctx, disk, q.clock, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for {
		batch, err := q.Batch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		if len(batch) > eventlog.MaxBatch {
			t.Fatal("batch unbounded")
		}
		ids := []string{}
		for _, e := range batch {
			scope, ok := forwarding.DecodeChainResourceScope(e.Message)
			if !ok || e.Gap.Count != 1 || seen[scope.RuleID] || len(e.Message) > 2048 {
				t.Fatal("lost/duplicated scope", e)
			}
			seen[scope.RuleID] = true
			ids = append(ids, e.ID)
		}
		// Unacknowledged multi-gap batches survive encrypted-store reopen unchanged.
		before, _ := json.Marshal(batch)
		q, err = OpenEventQueue(ctx, disk, q.clock, rand.Reader, EventLimits{})
		if err != nil {
			t.Fatal(err)
		}
		again, err := q.Batch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(again)
		if string(before) != string(after) {
			t.Fatal("reopen changed frozen batch")
		}
		if err = q.Ack(ctx, ids); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 300 || q.candidate().gapBytes != 0 {
		t.Fatal("scopes were squeezed into one message or not reclaimed", len(seen))
	}
}

func TestChainEventsGapScopeBudgetFallsBackGloballyAndReclaimsAfterAck(t *testing.T) {
	ctx := context.Background()
	q, disk := queueOnDisk(t, EventLimits{})
	s := q.candidate()
	at := q.clock.Now()
	for i := 0; i < forwarding.ChainResourceScopeCount+2; i++ {
		scope := forwarding.ChainResourceScope{RuleID: fmt.Sprint("rule-", i), Generation: 1, Fingerprint: "fp"}
		if err := q.mergeGap(&s, eventlog.Gap{Count: 1, From: at, To: at, Reason: "overflow"}, scope.Message()); err != nil {
			t.Fatal(err)
		}
	}
	if s.gapCount < 100 || s.gapCount > forwarding.ChainResourceScopeCount || s.gapBytes > forwarding.ChainResourceScopeBytes {
		t.Fatal("scope budget", s.gapCount, s.gapBytes)
	}
	var total uint64
	for _, e := range s.Pending {
		total += e.Gap.Count
	}
	if total != forwarding.ChainResourceScopeCount+2 || s.Pending["overflow"].Gap == nil {
		t.Fatal("budget overflow was silent", total)
	}
	// Existing precise scope must still advance while new scopes fall back.
	scope := forwarding.ChainResourceScope{RuleID: "rule-0", Generation: 1, Fingerprint: "fp"}
	if err := q.mergeGap(&s, eventlog.Gap{Count: 1, From: at, To: at.Add(time.Second), Reason: "overflow"}, scope.Message()); err != nil {
		t.Fatal(err)
	}
	if s.Pending[gapKey("overflow", scope.Message())].Gap.Count != 2 {
		t.Fatal("full budget blocked existing scope")
	}
	if err := q.commit(ctx, s); err != nil {
		t.Fatal(err)
	}
	q, err := OpenEventQueue(ctx, disk, q.clock, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := q.Batch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, e := range batch {
		ids = append(ids, e.ID)
	}
	if err = q.Ack(ctx, ids); err != nil {
		t.Fatal(err)
	}
	s = q.candidate()
	scope.RuleID = "later-current-rule"
	if err = q.mergeGap(&s, eventlog.Gap{Count: 1, From: at, To: at, Reason: "overflow"}, scope.Message()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Pending[gapKey("overflow", scope.Message())]; !ok {
		t.Fatal("ack did not free space for later current scope")
	}
}
