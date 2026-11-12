package panel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChainFlowSuccessCannotEraseDifferentEdgeOrHistoricalFailure(t *testing.T) {
	for _, scenario := range []string{"other_edge", "same_edge", "old_process_success", "old_snapshot_success", "expired_success", "expired_after_failure", "same_timestamp_new_failure", "same_timestamp_owner_success", "same_timestamp_new_process", "unrelated_event_volume", "retention_failure", "retention_success", "new_generation_same_fingerprint"} {
		t.Run(scenario, func(t *testing.T) {
			p, ids, rid := chainConfirmFixture(t)
			ctx := context.Background()
			for _, i := range []int{2, 1} {
				m, e := p.controlMessage(ctx, ids[i])
				if e != nil {
					t.Fatal(e)
				}
				tx, e := p.db.BeginTx(ctx, nil)
				if e != nil {
					t.Fatal(e)
				}
				_, e = p.confirmTunnelAuthorizations(ctx, tx, ids[i], chainReport(m.Snapshot), m.Snapshot.Revision, m.Snapshot.Digest)
				if e != nil {
					tx.Rollback()
					t.Fatal(e)
				}
				if e = tx.Commit(); e != nil {
					t.Fatal(e)
				}
			}
			connected := map[string]bool{}
			for _, id := range ids {
				connected[id] = true
			}
			now := p.deps.Clock.Now()
			runtime, e := p.loadChainRuntime(ctx, p.db, connected, now)
			if e != nil {
				t.Fatal(e)
			}
			a := runtime.inputs[ids[0]].snapshot.Rules[0]
			edge := a.ChainAuthorization.Edges[len(a.ChainAuthorization.Edges)-1]
			failed := forwarding.ChainFailure{FromNodeID: edge.FromNodeId, ToNodeID: edge.ToNodeId, AddressID: edge.AddressId, EndpointConfigDigest: edge.EndpointConfigDigest, HopIndex: edge.HopIndex, RuleGeneration: a.AuthorizationGeneration, GrantGeneration: a.AuthorizationGeneration, ChainFingerprint: a.ChainAuthorization.Fingerprint}
			sessionInstance := "fixture"
			put := func(node string, observation forwarding.ChainFailureObservation, instance, digest string, rev uint64) {
				t.Helper()
				raw, _ := json.Marshal(forwarding.ChainEvent{Retained: true, AcceptedSuccess: true, Historical: true, ProcessInstance: instance, SnapshotDigest: digest, Observation: observation})
				event := eventlog.Event{ID: fmt.Sprint(node, observation.Class, observation.ObservedAt.UnixNano(), observation.Sequence, instance), Type: "chain_flow", Message: string(raw), ConfigRevision: rev, OccurredAt: observation.ObservedAt}
				if _, e := p.acceptEvents(ctx, node, &controlSession{instance: sessionInstance, eventsLogged: true}, &controlpb.NodeReport{Events: []*controlpb.Event{event.Proto()}}); e != nil {
					t.Fatal(e)
				}
			}
			failedAt := now.Add(-2 * time.Second)
			if scenario == "expired_after_failure" {
				failedAt = now.Add(-30 * time.Second)
			}
			failedNode, failedClass, failedSequence := ids[0], "downstream_next_hop", uint64(0)
			if scenario == "same_timestamp_new_failure" {
				failedNode, failedClass, failedSequence = ids[1], "entry_exit", 2
			}
			if scenario == "same_timestamp_owner_success" {
				failedSequence = 999
			}
			put(failedNode, forwarding.ChainFailureObservation{Sequence: failedSequence, RuleID: rid, ChainID: a.ChainId, Class: failedClass, Edge: failed, ObservedAt: failedAt}, "fixture", runtime.inputs[failedNode].snapshot.Digest, runtime.inputs[failedNode].snapshot.Revision)
			success := failed
			node := ids[1]
			if scenario == "other_edge" {
				node = ids[0]
				e := a.ChainAuthorization.Edges[0]
				success.FromNodeID, success.ToNodeID, success.AddressID, success.EndpointConfigDigest, success.HopIndex = e.FromNodeId, e.ToNodeId, e.AddressId, e.EndpointConfigDigest, e.HopIndex
			}
			s := runtime.inputs[node].snapshot
			instance, digest := "fixture", s.Digest
			at := now.Add(-time.Second)
			if scenario == "old_process_success" {
				instance = "old"
			}
			if scenario == "old_snapshot_success" {
				digest = "old"
			}
			if scenario == "expired_success" || scenario == "expired_after_failure" || scenario == "retention_failure" || scenario == "unrelated_event_volume" || scenario == "new_generation_same_fingerprint" {
				at = now.Add(-20 * time.Second)
			}
			successSequence := uint64(100)
			if scenario == "same_timestamp_new_failure" || scenario == "same_timestamp_owner_success" {
				at, successSequence = failedAt, 1
			}
			put(node, forwarding.ChainFailureObservation{Sequence: successSequence, RuleID: rid, ChainID: a.ChainId, Class: "established", Edge: success, ObservedAt: at}, instance, digest, s.Revision)
			if scenario == "same_timestamp_owner_success" {
				// A delayed upstream account with a larger unrelated sequence
				// cannot undo the actual edge owner's recovery at the same time.
				put(ids[0], forwarding.ChainFailureObservation{Sequence: 1000, RuleID: rid, ChainID: a.ChainId, Class: "downstream_next_hop", Edge: failed, ObservedAt: at}, "fixture", runtime.inputs[ids[0]].snapshot.Digest, runtime.inputs[ids[0]].snapshot.Revision)
			}
			if scenario == "same_timestamp_new_process" {
				// Sequence restarts in a new process. The later received current
				// failure must win over a prior process's larger success sequence.
				if _, e = p.db.Exec("UPDATE nodes SET process_instance='restart' WHERE id=?", node); e != nil {
					t.Fatal(e)
				}
				sessionInstance = "restart"
				put(node, forwarding.ChainFailureObservation{Sequence: 1, RuleID: rid, ChainID: a.ChainId, Class: "entry_exit", Edge: failed, ObservedAt: at}, "restart", digest, s.Revision)
				runtime, e = p.loadChainRuntime(ctx, p.db, connected, now)
				if e != nil {
					t.Fatal(e)
				}
			}
			if scenario == "unrelated_event_volume" {
				tx, err := p.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 1001; i++ {
					raw, _ := json.Marshal(forwarding.ChainEvent{Historical: true, Observation: forwarding.ChainFailureObservation{RuleID: "unrelated"}})
					if err = insertEvent(ctx, tx, ids[0], "node", eventlog.Event{ID: fmt.Sprint("unrelated-", i), Type: "chain_flow", Message: string(raw), OccurredAt: now}, now); err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "retention_failure" || scenario == "retention_success" {
				now = now.Add(91 * 24 * time.Hour)
				cleaner := &Panel{db: p.db, deps: p.deps}
				cleaner.deps.Clock = retentionClock{now: now}
				for {
					more, err := cleaner.cleanHistoryBatch(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if !more {
						break
					}
				}
			}
			if scenario == "new_generation_same_fingerprint" {
				if _, e = p.db.Exec("UPDATE rule_authorizations SET generation=generation+1 WHERE rule_id=?", rid); e != nil {
					t.Fatal(e)
				}
				for _, id := range ids {
					input := runtime.inputs[id]
					input.snapshot = proto.Clone(input.snapshot).(*controlpb.ConfigSnapshot)
					for _, rule := range input.snapshot.Rules {
						rule.AuthorizationGeneration++
					}
					for _, grant := range input.snapshot.GetTunnelServer().GetGrants() {
						grant.Rule.AuthorizationGeneration++
					}
					runtime.inputs[id] = input
				}
			}
			item := operationRule{ID: rid, ChainID: a.ChainId, ChainFingerprint: a.ChainAuthorization.Fingerprint, Enabled: true, Status: "no_known_issue"}
			if scenario == "new_generation_same_fingerprint" {
				item.Status = "unknown"
			}
			if e = p.attachChainFlowEvidence(ctx, p.db, &item, runtime, now); e != nil {
				t.Fatal(e)
			}
			wantFailure := scenario != "same_edge" && scenario != "same_timestamp_owner_success" && scenario != "retention_success" && scenario != "new_generation_same_fingerprint"
			if (len(item.Reasons) > 0) != wantFailure {
				t.Fatalf("failure erased or wrong edge cleared: %s %+v", scenario, item)
			}
			if scenario == "new_generation_same_fingerprint" && item.Status != "unknown" {
				t.Fatal("old generation promoted absent current evidence", item)
			}
			if scenario == "retention_failure" || scenario == "retention_success" {
				var count int
				if e = p.db.QueryRow("SELECT count(*) FROM events WHERE type='chain_flow'").Scan(&count); e != nil || count != 1 {
					t.Fatal("retention lost current baseline or kept every transition", count, e)
				}
				if _, e = p.db.Exec("UPDATE rule_authorizations SET fingerprint='new-configuration' WHERE rule_id=?", rid); e != nil {
					t.Fatal(e)
				}
				cleaner := &Panel{db: p.db, deps: p.deps}
				cleaner.deps.Clock = retentionClock{now: now}
				if _, e = cleaner.cleanHistoryBatch(ctx); e != nil {
					t.Fatal(e)
				}
				if e = p.db.QueryRow("SELECT count(*) FROM events WHERE type='chain_flow'").Scan(&count); e != nil || count != 0 {
					t.Fatal("obsolete configuration baseline survived history retention", count, e)
				}
			}
			if scenario == "same_edge" {
				checkResolved := func(at time.Time) {
					t.Helper()
					item.Reasons = nil
					item.Status = "no_known_issue"
					if e := p.attachChainFlowEvidence(ctx, p.db, &item, runtime, at); e != nil {
						t.Fatal(e)
					}
					if len(item.Reasons) != 0 {
						t.Fatal("accepted recovery resurrected old failure", item.Reasons)
					}
				}
				checkResolved(now.Add(time.Minute))
				// An unrelated snapshot and process change cannot revoke a
				// recovery receipt already accepted for this authorization.
				changed := proto.Clone(s).(*controlpb.ConfigSnapshot)
				changed.Revision++
				changed.CheckPort++
				changed.Digest = forwarding.SnapshotDigest(changed)
				raw, e := proto.Marshal(changed)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = p.db.Exec("UPDATE node_snapshots SET snapshot=?,digest=?,encrypted=0 WHERE node_id=?", raw, changed.Digest, node); e != nil {
					t.Fatal(e)
				}
				if _, e = p.db.Exec("UPDATE nodes SET desired_revision=?,process_instance='restart' WHERE id=?", changed.Revision, node); e != nil {
					t.Fatal(e)
				}
				runtime, e = p.loadChainRuntime(ctx, p.db, connected, now.Add(time.Minute))
				if e != nil {
					t.Fatal(e)
				}
				checkResolved(now.Add(time.Minute))
				deps := p.deps
				if e = p.Close(); e != nil {
					t.Fatal(e)
				}
				p, e = Open(ctx, Config{PublicOrigin: "https://panel.test"}, deps)
				if e != nil {
					t.Fatal(e)
				}
				defer p.Close()
				runtime, e = p.loadChainRuntime(ctx, p.db, connected, now.Add(time.Minute))
				if e != nil {
					t.Fatal(e)
				}
				checkResolved(now.Add(time.Minute))
				// A late failure older than that success stays superseded;
				// a genuinely later current failure becomes abnormal again.
				put(ids[0], forwarding.ChainFailureObservation{RuleID: rid, ChainID: a.ChainId, Class: "downstream_next_hop", Edge: failed, ObservedAt: now.Add(-1500 * time.Millisecond)}, "fixture", runtime.inputs[ids[0]].snapshot.Digest, runtime.inputs[ids[0]].snapshot.Revision)
				checkResolved(now.Add(time.Minute))
				put(ids[0], forwarding.ChainFailureObservation{RuleID: rid, ChainID: a.ChainId, Class: "downstream_next_hop", Edge: failed, ObservedAt: now}, "fixture", runtime.inputs[ids[0]].snapshot.Digest, runtime.inputs[ids[0]].snapshot.Revision)
				item.Reasons = nil
				if e = p.attachChainFlowEvidence(ctx, p.db, &item, runtime, now); e != nil {
					t.Fatal(e)
				}
				if item.Status != "abnormal" || len(item.Reasons) != 1 {
					t.Fatal("new current failure did not reappear", item)
				}
			}

		})
	}
}

func TestChainGrantHeartbeatDoesNotConfirmAndPartialStillConfirmsAfterSummary(t *testing.T) {
	p, ids, _ := chainConfirmFixture(t)
	ctx := context.Background()
	m, e := p.controlMessage(ctx, ids[2])
	if e != nil {
		t.Fatal(e)
	}
	s := m.Snapshot
	report := chainReport(s)
	report.AttemptedRevision = 0
	report.AttemptedDigest = ""
	report.TunnelGrantResults = nil
	report.RevocationsApplied = false
	confirm := func() {
		t.Helper()
		e = p.commitForwarding(ctx, func(tx *sql.Tx) error {
			_, e := p.confirmTunnelAuthorizations(ctx, tx, ids[2], report, s.Revision, s.Digest)
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	confirm()
	var n int
	if e = p.db.QueryRow("SELECT count(*) FROM node_tunnel_grant_results WHERE node_id=?", ids[2]).Scan(&n); e != nil || n != 0 {
		t.Fatal("heartbeat inferred grant ACK", n, e)
	}
	if _, e = p.db.Exec("UPDATE nodes SET applied_revision=desired_revision WHERE id=?", ids[2]); e != nil {
		t.Fatal(e)
	}
	report = chainReport(s)
	report.AppliedRevision = 0
	report.AppliedDigest = ""
	report.ApplyError = "unrelated listener failed"
	confirm()
	if e = p.db.QueryRow("SELECT count(*) FROM node_tunnel_grant_results WHERE node_id=? AND apply_error=''", ids[2]).Scan(&n); e != nil || n != 1 {
		t.Fatal("summary swallowed complete partial grant report", n, e)
	}
}

func TestChainLocalEstablishmentRequiresActualOwnerAndAuthorization(t *testing.T) {
	now := time.Now()
	r := &controlpb.ForwardRule{Id: "r", ChainId: "chain", HopIndex: 1, AuthorizationGeneration: 7, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: "fp", Edges: []*controlpb.ChainEdgeAuthorization{{FromNodeId: "a", ToNodeId: "b", HopIndex: 1}, {FromNodeId: "b", ToNodeId: "c", HopIndex: 2}}}}
	s := &controlpb.ConfigSnapshot{Revision: 1, Digest: "digest", TunnelServer: &controlpb.TunnelServer{Grants: []*controlpb.TunnelGrant{{Rule: r}}}}
	for _, scenario := range []string{"owner", "forwarded", "wrong_generation", "wrong_hop", "network_edge", "wrong_rule", "wrong_fingerprint"} {
		t.Run(scenario, func(t *testing.T) {
			v := forwarding.ChainEvent{ProcessInstance: "process", SnapshotDigest: "digest", Observation: forwarding.ChainFailureObservation{RuleID: "r", ChainID: "chain", Class: "local_established", ObservedAt: now, Edge: forwarding.ChainFailure{FromNodeID: "b", HopIndex: 1, RuleGeneration: 7, GrantGeneration: 7, ChainFingerprint: "fp"}}}
			node := "b"
			switch scenario {
			case "forwarded":
				node = "a"
			case "wrong_generation":
				v.Observation.Edge.GrantGeneration++
			case "wrong_hop":
				v.Observation.Edge.HopIndex++
			case "network_edge":
				v.Observation.Edge.ToNodeID = "c"
			case "wrong_rule":
				v.Observation.RuleID = "other"
			case "wrong_fingerprint":
				v.Observation.Edge.ChainFingerprint = "old"
			}
			ok, err := validChainEventSnapshot(v, s, node, "process", eventlog.Event{ConfigRevision: 1, OccurredAt: now})
			if err != nil || ok != (scenario == "owner") {
				t.Fatal("local recovery authority incorrect", ok, err)
			}
		})
	}
}

func TestChainLocalRecoveryDoesNotEraseTargetFailureWithSameNodeIdentity(t *testing.T) {
	p, ids, rid := chainConfirmFixture(t)
	ctx := context.Background()
	m, err := p.controlMessage(ctx, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot
	r := s.GetTunnelServer().GetGrants()[0].Rule
	now := p.deps.Clock.Now()
	edge := forwarding.ChainFailure{FromNodeID: ids[2], HopIndex: 2, RuleGeneration: r.AuthorizationGeneration, GrantGeneration: r.AuthorizationGeneration, ChainFingerprint: r.ChainAuthorization.Fingerprint}
	for i, class := range []string{"local_resource", "exit_target", "local_established"} {
		raw, _ := json.Marshal(forwarding.ChainEvent{ProcessInstance: "fixture", SnapshotDigest: s.Digest, Observation: forwarding.ChainFailureObservation{Sequence: uint64(i + 1), RuleID: rid, ChainID: r.ChainId, Class: class, Edge: edge, ObservedAt: now}})
		event := eventlog.Event{ID: fmt.Sprint(i), Type: "chain_flow", Message: string(raw), ConfigRevision: s.Revision, OccurredAt: now}
		if _, err = p.acceptEvents(ctx, ids[2], &controlSession{instance: "fixture", eventsLogged: true}, &controlpb.NodeReport{Events: []*controlpb.Event{event.Proto()}}); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := p.loadChainRuntime(ctx, p.db, map[string]bool{ids[2]: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	item := operationRule{ID: rid, ChainID: r.ChainId, ChainFingerprint: r.ChainAuthorization.Fingerprint, Enabled: true, Status: "no_known_issue"}
	if err = p.attachChainFlowEvidence(ctx, p.db, &item, runtime, now); err != nil {
		t.Fatal(err)
	}
	if len(item.Reasons) != 1 || item.Reasons[0].Failure.Class != "exit_target" || item.Status != "abnormal" {
		t.Fatal("local success erased unrelated target failure or failed to clear quota", item)
	}
}

func TestChainResourceOriginOrderSurvivesRelayDelayAndRestart(t *testing.T) {
	p, ids, rid := chainConfirmFixture(t)
	ctx := context.Background()
	for _, i := range []int{2, 1} {
		m, err := p.controlMessage(ctx, ids[i])
		if err != nil {
			t.Fatal(err)
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.confirmTunnelAuthorizations(ctx, tx, ids[i], chainReport(m.Snapshot), m.Snapshot.Revision, m.Snapshot.Digest); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	now := p.deps.Clock.Now()
	snapshots := map[string]*controlpb.ConfigSnapshot{}
	for _, id := range ids {
		m, err := p.controlMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[id] = m.Snapshot
	}
	r := snapshots[ids[0]].Rules[0]
	edge := forwarding.ChainFailure{FromNodeID: ids[1], HopIndex: 1, RuleGeneration: r.AuthorizationGeneration, GrantGeneration: r.AuthorizationGeneration, ChainFingerprint: r.ChainAuthorization.Fingerprint}
	serial := 0
	put := func(reporter, instance, origin, class string, sequence, originSequence uint64, at time.Time) forwarding.ChainEvent {
		t.Helper()
		serial++
		s := snapshots[reporter]
		v := forwarding.ChainEvent{ProcessInstance: instance, SnapshotDigest: s.Digest, Observation: forwarding.ChainFailureObservation{Sequence: sequence, ResourceOrder: &forwarding.ChainResourceOrder{Instance: origin, Sequence: originSequence}, RuleID: rid, ChainID: r.ChainId, Class: class, Edge: edge, ObservedAt: at}}
		raw, _ := json.Marshal(v)
		event := eventlog.Event{ID: fmt.Sprint("resource-", serial), Type: "chain_flow", Message: string(raw), ConfigRevision: s.Revision, OccurredAt: at}
		if _, err := p.acceptEvents(ctx, reporter, &controlSession{instance: instance, eventsLogged: true}, &controlpb.NodeReport{Events: []*controlpb.Event{event.Proto()}}); err != nil {
			t.Fatal(err)
		}
		var message string
		if err := p.db.QueryRow("SELECT message FROM events WHERE event_id=?", event.ID).Scan(&message); err != nil {
			t.Fatal(err)
		}
		got, err := decodeChainEvent(message)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	retainedClass := func(want string) {
		t.Helper()
		var message string
		if err := p.db.QueryRow("SELECT message FROM events WHERE type='chain_flow' AND json_extract(message,'$.retained')=1").Scan(&message); err != nil {
			t.Fatal(err)
		}
		got, err := decodeChainEvent(message)
		if err != nil || got.Observation.Class != want {
			t.Fatal("wrong retained outcome", got, err)
		}
	}
	put(ids[1], "fixture", "fixture", "local_established", 1, 1, now)
	put(ids[0], "fixture", "fixture", "local_resource", 100, 2, now)
	retainedClass("local_resource")
	put(ids[1], "fixture", "fixture", "local_established", 3, 3, now)
	// The reporter's own sequence and timestamp cannot replace the actual owner's order.
	put(ids[0], "fixture", "fixture", "local_resource", 1000, 2, now.Add(time.Second))
	retainedClass("local_established")
	if _, err := p.db.Exec("UPDATE nodes SET process_instance='restart' WHERE id=?", ids[1]); err != nil {
		t.Fatal(err)
	}
	// A new process starts at one; timestamps and sequence numbers from the old
	// process must not block this currently authenticated resource refusal.
	put(ids[0], "fixture", "restart", "local_resource", 1001, 1, now.Add(-time.Second))
	retainedClass("local_resource")
	old := put(ids[0], "fixture", "fixture", "local_established", 1002, 999, now)
	if !old.Historical || old.Retained {
		t.Fatal("upstream fabricated owner recovery", old)
	}
	old = put(ids[0], "fixture", "fixture", "local_resource", 1003, 999, now)
	if !old.Historical || old.Retained {
		t.Fatal("old owner process revived", old)
	}
	put(ids[1], "restart", "restart", "local_established", 2, 2, now)
	retainedClass("local_established")
}

func TestChainResourceOriginValidation(t *testing.T) {
	now := time.Now()
	r := &controlpb.ForwardRule{Id: "r", ChainId: "chain", AuthorizationGeneration: 7, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: "fp", Edges: []*controlpb.ChainEdgeAuthorization{{FromNodeId: "a", ToNodeId: "b", HopIndex: 1}}}}
	s := &controlpb.ConfigSnapshot{Revision: 1, Digest: "digest", Rules: []*controlpb.ForwardRule{r}}
	for _, scenario := range []string{"valid", "empty_instance", "long_instance", "zero_sequence", "network", "target", "unauthorized_owner", "owner_instance", "owner_sequence", "generation"} {
		t.Run(scenario, func(t *testing.T) {
			o := forwarding.ChainFailureObservation{Sequence: 3, ResourceOrder: &forwarding.ChainResourceOrder{Instance: "b-process", Sequence: 2}, RuleID: "r", ChainID: "chain", Class: "local_resource", ObservedAt: now, Edge: forwarding.ChainFailure{FromNodeID: "b", HopIndex: 1, RuleGeneration: 7, GrantGeneration: 7, ChainFingerprint: "fp"}}
			node := "a"
			switch scenario {
			case "empty_instance":
				o.ResourceOrder.Instance = ""
			case "long_instance":
				o.ResourceOrder.Instance = strings.Repeat("x", 129)
			case "zero_sequence":
				o.ResourceOrder.Sequence = 0
			case "network":
				o.Edge.ToNodeID = "c"
			case "target":
				o.Class = "exit_target"
			case "unauthorized_owner":
				o.Edge.FromNodeID = "other"
			case "owner_instance":
				node = "b"
				o.Sequence = 2
			case "owner_sequence":
				node = "b"
				o.ResourceOrder.Instance = "process"
			case "generation":
				o.Edge.RuleGeneration++
			}
			v := forwarding.ChainEvent{ProcessInstance: "process", SnapshotDigest: "digest", Observation: o}
			ok, err := validChainEventSnapshot(v, s, node, "process", eventlog.Event{ConfigRevision: 1, OccurredAt: now})
			if scenario == "valid" {
				if !ok || err != nil {
					t.Fatal(ok, err)
				}
			} else if ok {
				t.Fatal("invalid resource order accepted")
			}
		})
	}
}

func TestChainResourceOrderReplacesLegacyOwnerBaseline(t *testing.T) {
	now := time.Now()
	edge := forwarding.ChainFailure{FromNodeID: "owner"}
	previous := forwarding.ChainEvent{ProcessInstance: "old", Observation: forwarding.ChainFailureObservation{Sequence: 999, Class: "local_established", Edge: edge, ObservedAt: now}}
	next := forwarding.ChainEvent{ProcessInstance: "relay", Observation: forwarding.ChainFailureObservation{Sequence: 20, ResourceOrder: &forwarding.ChainResourceOrder{Instance: "new", Sequence: 1}, Class: "local_resource", Edge: edge, ObservedAt: now.Add(-time.Second)}}
	if !newerChainOutcome("relay", next, "owner", previous) {
		t.Fatal("legacy old process blocked current owner failure")
	}
	previous.ProcessInstance = "new"
	previous.Observation.Sequence = 2
	if newerChainOutcome("relay", next, "owner", previous) {
		t.Fatal("older origin replaced actual owner recovery")
	}
}

func TestChainResourcePersistentBatchCurrentThenOldAndOverflow(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprint("overflow=", overflow), func(t *testing.T) {
			p, ids, rid := chainConfirmFixture(t)
			ctx := context.Background()
			for _, i := range []int{2, 1} {
				m, err := p.controlMessage(ctx, ids[i])
				if err != nil {
					t.Fatal(err)
				}
				tx, err := p.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = p.confirmTunnelAuthorizations(ctx, tx, ids[i], chainReport(m.Snapshot), m.Snapshot.Revision, m.Snapshot.Digest); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			snapshots := map[string]*controlpb.ConfigSnapshot{}
			for _, id := range ids {
				m, err := p.controlMessage(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				snapshots[id] = m.Snapshot
			}
			rule := snapshots[ids[0]].Rules[0]
			now := p.deps.Clock.Now()
			at := now.Add(-time.Second)
			makeEvent := func(reporter, instance, origin, class string, owner int, seq uint64, when time.Time) forwarding.ChainEvent {
				return forwarding.ChainEvent{ProcessInstance: instance, SnapshotDigest: snapshots[reporter].Digest, Observation: forwarding.ChainFailureObservation{Sequence: seq, RuleID: rid, ChainID: rule.ChainId, Class: class, ObservedAt: when, ResourceOrder: &forwarding.ChainResourceOrder{Instance: origin, Sequence: seq}, Edge: forwarding.ChainFailure{FromNodeID: ids[owner], HopIndex: uint32(owner), RuleGeneration: rule.AuthorizationGeneration, GrantGeneration: rule.AuthorizationGeneration, ChainFingerprint: rule.ChainAuthorization.Fingerprint}}}
			}
			accept := func(reporter, instance string, events []eventlog.Event) {
				t.Helper()
				report := &controlpb.NodeReport{}
				for _, e := range events {
					report.Events = append(report.Events, e.Proto())
				}
				if _, err := p.acceptEvents(ctx, reporter, &controlSession{instance: instance, eventsLogged: true}, report); err != nil {
					t.Fatal(err)
				}
			}
			putOwner := func(owner int, instance string, seq uint64, when time.Time) {
				v := makeEvent(ids[owner], instance, instance, "local_established", owner, seq, when)
				raw, _ := json.Marshal(v)
				accept(ids[owner], instance, []eventlog.Event{{ID: fmt.Sprint(owner, "-", instance, "-", seq), Type: "chain_flow", Message: string(raw), ConfigRevision: snapshots[ids[owner]].Revision, OccurredAt: when}})
			}
			putOwner(1, "fixture", 99, now.Add(-2*time.Second))
			if _, err := p.db.Exec("UPDATE nodes SET process_instance='current' WHERE id=?", ids[1]); err != nil {
				t.Fatal(err)
			}
			limits := node.EventLimits{}
			if overflow {
				limits = node.EventLimits{Count: 1, Bytes: 4096}
			}
			disk := node.FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
			clock := retentionClock{now: at}
			queue, err := node.OpenEventQueue(ctx, disk, clock, rand.Reader, limits)
			if err != nil {
				t.Fatal(err)
			}
			// These are the distinct candidates produced and coalesced by the Engine
			// and node tests. Exercise the actual encrypted queue, reopen, wire batch
			// and Panel receipt, with current first and three old instances last.
			for i, origin := range []string{"current", "old-1", "old-2", "old-3"} {
				v := makeEvent(ids[0], "fixture", origin, "local_resource", 1, uint64(i+1), at)
				raw, _ := json.Marshal(v)
				if err := queue.Record(ctx, "chain_flow", string(raw), snapshots[ids[0]].Revision); err != nil {
					t.Fatal(err)
				}
			}
			queue, err = node.OpenEventQueue(ctx, disk, clock, rand.Reader, limits)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := queue.Batch(ctx)
			if err != nil {
				t.Fatal(err)
			}
			accept(ids[0], "fixture", batch)
			if overflow {
				// An older expired gap can be sent after a newer overflow gap. The
				// queue's two independent reasons must not replace each other's baseline.
				accept(ids[0], "fixture", []eventlog.Event{{ID: "late-expired-gap", Type: "event_gap", Message: (forwarding.ChainResourceScope{RuleID: rid, Generation: rule.AuthorizationGeneration, Fingerprint: rule.ChainAuthorization.Fingerprint}).Message(), OccurredAt: now, Gap: &eventlog.Gap{Count: 1, From: now.Add(-4 * time.Second), To: now.Add(-3 * time.Second), Reason: "expired"}}})
				putOwner(1, "current", 2, now.Add(-2*time.Second))
				putOwner(2, "fixture", 2, now.Add(-2*time.Second))
			}
			runtime, err := p.loadChainRuntime(ctx, p.db, map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}, now)
			if err != nil {
				t.Fatal(err)
			}
			project := func(want string) {
				t.Helper()
				item := operationRule{ID: rid, ChainID: rule.ChainId, ChainFingerprint: rule.ChainAuthorization.Fingerprint, Enabled: true, Status: "no_known_issue"}
				if err := p.attachChainFlowEvidence(ctx, p.db, &item, runtime, now); err != nil {
					t.Fatal(err)
				}
				if item.Status != want {
					t.Fatal("current refusal/gap hidden by old success", item)
				}
				incidents := collectRuleIncidents(operationsView{Rules: []operationRule{item}})
				if want != "no_known_issue" && (len(incidents) != 1 || incidents[0].Closure != "" || incidents[0].ExistingOnly) {
					t.Fatal("unknown gap closed/failed to preserve incident evidence", incidents)
				}
			}
			if !overflow {
				project("abnormal")
			} else {
				project("unknown")
			}
			var historical int
			if err := p.db.QueryRow("SELECT count(*) FROM events WHERE type='chain_flow' AND json_extract(message,'$.historical')=1").Scan(&historical); err != nil {
				t.Fatal(err)
			}
			wantOld := 3
			if overflow {
				wantOld = 1
			}
			if historical != wantOld {
				t.Fatal("old instances became current or vanished", historical)
			}
			if overflow {
				cleaner := &Panel{db: p.db, deps: p.deps}
				cleaner.deps.Clock = retentionClock{now: now.Add(91 * 24 * time.Hour)}
				for {
					more, err := cleaner.cleanHistoryBatch(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if !more {
						break
					}
				}
				project("unknown")
				deps := p.deps
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
				p, err = Open(ctx, Config{PublicOrigin: "https://panel.test"}, deps)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				project("unknown")
				// Recovery on only B does not establish that C's possibly lost failure
				// recovered. All owners in this reporter's authorized suffix are needed.
				putOwner(1, "current", 3, now)
				project("unknown")
				putOwner(2, "fixture", 3, now)
				project("no_known_issue")
			}
		})
	}
}

// A gap belongs to the authorization whose actual resource evidence was lost.
// Moving it to the reporter's latest snapshot would reopen unrelated incidents.
func TestChainResourceGapConfigurationBoundary(t *testing.T) {
	for _, scenario := range []string{"address", "target", "chain", "new_rule", "late_old", "cleared_then_change", "generation", "global", "unscoped_json"} {
		t.Run(scenario, func(t *testing.T) {
			p, ids, rid := chainConfirmFixture(t)
			// This test drives only gap evidence; periodic whole-system projection
			// would intentionally reconcile our isolated source against other evidence.
			p.stopBackground()
			p.background.Wait()
			ctx := context.Background()
			confirm := func() map[string]*controlpb.ConfigSnapshot {
				t.Helper()
				for _, i := range []int{2, 1, 2, 1} {
					m, err := p.controlMessage(ctx, ids[i])
					if err != nil {
						t.Fatal(err)
					}
					tx, err := p.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = p.confirmTunnelAuthorizations(ctx, tx, ids[i], chainReport(m.Snapshot), m.Snapshot.Revision, m.Snapshot.Digest); err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				snapshots := map[string]*controlpb.ConfigSnapshot{}
				for _, id := range ids {
					m, err := p.controlMessage(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					snapshots[id] = m.Snapshot
				}
				return snapshots
			}
			snapshots := confirm()
			old := snapshots[ids[0]].Rules[0]
			now := p.deps.Clock.Now()
			at := now.Add(-time.Second)
			disk := node.FileStore{Dir: filepath.Join(t.TempDir(), "queue"), Random: rand.Reader}
			q, err := node.OpenEventQueue(ctx, disk, retentionClock{now: at}, rand.Reader, node.EventLimits{Count: 1, Bytes: 4096})
			if err != nil {
				t.Fatal(err)
			}
			lost := forwarding.ChainEvent{ProcessInstance: "fixture", SnapshotDigest: snapshots[ids[0]].Digest, Observation: forwarding.ChainFailureObservation{Sequence: 1, RuleID: rid, ChainID: old.ChainId, Class: "local_resource", ObservedAt: at, ResourceOrder: &forwarding.ChainResourceOrder{Instance: "fixture", Sequence: 1}, Edge: forwarding.ChainFailure{FromNodeID: ids[1], HopIndex: 1, RuleGeneration: old.AuthorizationGeneration, GrantGeneration: old.AuthorizationGeneration, ChainFingerprint: old.ChainAuthorization.Fingerprint}}}
			raw, _ := json.Marshal(lost)
			if err = q.Record(ctx, "chain_flow", string(raw), snapshots[ids[0]].Revision); err != nil {
				t.Fatal(err)
			}
			if err = q.Record(ctx, "sync", "later event", snapshots[ids[0]].Revision); err != nil {
				t.Fatal(err)
			}
			q, err = node.OpenEventQueue(ctx, disk, retentionClock{now: at}, rand.Reader, node.EventLimits{Count: 1, Bytes: 4096})
			if err != nil {
				t.Fatal(err)
			}
			batch, err := q.Batch(ctx)
			if err != nil || len(batch) != 2 || batch[0].Gap == nil {
				t.Fatal(batch, err)
			}
			accept := func(id string, events []eventlog.Event) {
				t.Helper()
				report := &controlpb.NodeReport{}
				for _, e := range events {
					report.Events = append(report.Events, e.Proto())
				}
				if _, err := p.acceptEvents(ctx, id, &controlSession{instance: "fixture", eventsLogged: true}, report); err != nil {
					t.Fatal(err)
				}
			}
			project := func(ruleID string) operationRule {
				t.Helper()
				runtime, err := p.loadChainRuntime(ctx, p.db, map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}, now)
				if err != nil {
					t.Fatal(err)
				}
				var fp string
				if err = p.db.QueryRow("SELECT fingerprint FROM rule_authorizations WHERE rule_id=?", ruleID).Scan(&fp); err != nil {
					t.Fatal(err)
				}
				item := operationRule{ID: ruleID, Name: "gap", ChainID: old.ChainId, ChainFingerprint: fp, Configuration: fp, Enabled: true, Status: "no_known_issue"}
				if err = p.attachChainFlowEvidence(ctx, p.db, &item, runtime, now); err != nil {
					t.Fatal(err)
				}
				return item
			}
			persist := func(items ...operationRule) {
				t.Helper()
				view := operationsView{Rules: items}
				tx, err := p.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				observations, err := reconcileIncidentSources(ctx, tx, collectRuleIncidents(view), view)
				if err != nil {
					t.Fatal(err)
				}
				for _, o := range observations {
					if err = saveIncidentObservation(ctx, tx, o, now); err != nil {
						t.Fatal(err)
					}
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "global" || scenario == "unscoped_json" {
				batch[0].Message = forwarding.ChainResourceGapMessage
				if scenario == "unscoped_json" {
					raw, _ := json.Marshal(forwarding.ResourceScope(lost.Observation))
					batch[0].Message = string(raw)
				}
				accept(ids[0], batch[:1])
			}
			if scenario != "late_old" && scenario != "global" && scenario != "unscoped_json" {
				accept(ids[0], batch[:1])
				if got := project(rid); got.Status != "unknown" {
					t.Fatal("current gap not visible", got)
				}
			}
			if scenario != "late_old" && scenario != "global" && scenario != "unscoped_json" && scenario != "generation" {
				persist(project(rid))
			}
			if scenario == "cleared_then_change" {
				for i := 1; i <= 2; i++ {
					v := lost
					v.ProcessInstance = "fixture"
					v.SnapshotDigest = snapshots[ids[i]].Digest
					v.Observation.Class = "local_established"
					v.Observation.Sequence = 2
					v.Observation.ResourceOrder = &forwarding.ChainResourceOrder{Instance: "fixture", Sequence: 2}
					v.Observation.ObservedAt = now
					v.Observation.Edge.FromNodeID = ids[i]
					v.Observation.Edge.HopIndex = uint32(i)
					raw, _ := json.Marshal(v)
					accept(ids[i], []eventlog.Event{{ID: fmt.Sprint("success", i), Type: "chain_flow", Message: string(raw), ConfigRevision: snapshots[ids[i]].Revision, OccurredAt: now}})
				}
				if got := project(rid); got.Status != "no_known_issue" {
					t.Fatal("real suffix success did not recover", got)
				}
			}
			if scenario == "cleared_then_change" {
				persist(project(rid))
			}
			checked := rid
			if scenario == "generation" {
				if _, err = p.db.Exec("UPDATE rule_authorizations SET generation=generation+1 WHERE rule_id=?", rid); err != nil {
					t.Fatal(err)
				}
			} else if err = p.commitForwarding(ctx, func(tx *sql.Tx) error {
				switch scenario {
				case "address", "late_old", "cleared_then_change":
					_, err := tx.Exec("UPDATE tunnel_exits SET public_port=25001 WHERE node_id=?", ids[2])
					return err
				case "target":
					_, err := tx.Exec("UPDATE forward_rules SET target_port=444 WHERE id=?", rid)
					return err
				case "chain":
					_, err := tx.Exec("UPDATE forward_chain_hops SET position=position+10 WHERE chain_id=?", old.ChainId)
					if err != nil {
						return err
					}
					_, err = tx.Exec("UPDATE forward_chain_hops SET position=13-position WHERE chain_id=?", old.ChainId)
					return err
				case "new_rule":
					rules, err := loadRules(ctx, tx)
					if err != nil {
						return err
					}
					r := rules[0]
					r.ID = "unrelated-new-chain-rule"
					r.ListenPort++
					checked = r.ID
					return storeRule(ctx, tx, r, true)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if scenario != "generation" {
				snapshots = confirm()
			}
			if scenario == "late_old" {
				accept(ids[0], batch[:1])
			}
			if got := project(checked); got.Status != "no_known_issue" {
				t.Fatal("old gap polluted new authorization", got)
			}
			if scenario == "new_rule" {
				if got := project(rid); got.Status != "unknown" {
					t.Fatal("unrelated rule creation erased existing gap", got)
				}
			}
			if scenario != "generation" {
				items := []operationRule{project(checked)}
				if scenario == "new_rule" {
					items = append(items, project(rid))
				}
				persist(items...)
				var count int
				if err = p.db.QueryRow("SELECT count(*) FROM operation_incidents WHERE kind='rule' AND title LIKE 'gap：%'").Scan(&count); err != nil {
					t.Fatal(err)
				}
				want := 1
				if scenario == "late_old" || (scenario == "global" || scenario == "unscoped_json") {
					want = 0
				}
				if count != want {
					t.Fatal("gap created unrelated/new incident", count, want)
				}
				if want == 1 {
					var closure string
					if err = p.db.QueryRow("SELECT closure FROM operation_incidents WHERE kind='rule' AND title LIKE 'gap：%'").Scan(&closure); err != nil {
						t.Fatal(err)
					}
					expected := "新版本替代"
					if scenario == "new_rule" {
						expected = ""
					}
					if scenario == "cleared_then_change" {
						expected = "异常解除"
					}
					if closure != expected {
						t.Fatal("wrong gap history ending", closure, expected)
					}
				}
				deps := p.deps
				if err = p.Close(); err != nil {
					t.Fatal(err)
				}
				p, err = Open(ctx, Config{PublicOrigin: "https://panel.test"}, deps)
				if err != nil {
					t.Fatal(err)
				}
				defer p.Close()
				p.stopBackground()
				p.background.Wait()
				if got := project(checked); got.Status != "no_known_issue" {
					t.Fatal("restart revived old gap", got)
				}
				persist(items...)
				var after int
				if err = p.db.QueryRow("SELECT count(*) FROM operation_incidents WHERE kind='rule' AND title LIKE 'gap：%'").Scan(&after); err != nil || after != count {
					t.Fatal("restart duplicated incident", after, count, err)
				}
			}
			cleaner := &Panel{db: p.db, deps: p.deps}
			cleaner.deps.Clock = retentionClock{now: now.Add(91 * 24 * time.Hour)}
			for {
				more, err := cleaner.cleanHistoryBatch(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !more {
					break
				}
			}
			var count int
			if err = p.db.QueryRow("SELECT count(*) FROM events WHERE type='event_gap'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "new_rule" {
				want = 1
			}
			if count != want {
				t.Fatal("retention kept obsolete gap or removed current gap", count, want)
			}
		})
	}
}
