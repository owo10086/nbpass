package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func chainFlowFailures(view map[string]any) []map[string]any {
	var failures []map[string]any
	for _, raw := range view["reasons"].([]any) {
		reason := raw.(map[string]any)
		if v, ok := reason["failure"].(map[string]any); ok {
			failures = append(failures, v)
		}
	}
	return failures
}
func awaitChainIncidentResolved(t *testing.T, r *chainRig, id string) {
	t.Helper()
	for i := 0; i < 35; i++ {
		v := r.c.Do("GET", "/api/incidents/"+id, nil, 200)
		if v["ended_at"] != nil && v["closure"] == "异常解除" {
			return
		}
		r.advance(t, 1)
	}
	t.Fatal("real recovery did not resolve chain incident", r.c.Do("GET", "/api/incidents/"+id, nil, 200), r.operation(t))
}

func chainUseSingleEntry(t *testing.T, r *chainRig) {
	t.Helper()
	delete(r.body, "entry_group_id")
	r.body["entry_node_id"] = r.ids[0]
	r.c.Do("PUT", "/api/rules/"+r.rule, r.body, 200)
	awaitRule(t, r.c, r.rule, "正常")
}
func chainEdgesReady(v map[string]any) bool {
	for _, raw := range v["chain_edges"].([]any) {
		e := raw.(map[string]any)
		if e["state"] != "ready" || e["fresh"] != true {
			return false
		}
	}
	return true
}

func TestChainFailoverNewRealSuccessRetriesRejectedRecovery(t *testing.T) {
	for _, mode := range []string{"expired", "old_snapshot"} {
		t.Run(mode, func(t *testing.T) {
			r := newChainRig(t, 1, []int{1, 1}, "primary_backup")
			chainUseSingleEntry(t, r)
			r.links[1][2].block(true)
			assertTCPFailure(t, int(r.entryPorts[0].Load()))
			r.awaitOperation(t, func(v map[string]any) bool { return len(chainFlowFailures(v)) > 0 })
			incident := incidentForChainRule(t, r, r.rule, true)["id"].(string)
			r.links[1][2].recover()
			r.advance(t, 75)
			r.awaitOperation(t, chainEdgesReady)
			var corrupt atomic.Bool
			var corrupted atomic.Int32
			corrupt.Store(true)
			r.h.TransformControlReports(func(report *controlpb.NodeReport) {
				if report.NodeId != r.ids[1] || !corrupt.Load() {
					return
				}
				for _, event := range report.Events {
					if event.Type != "chain_flow" {
						continue
					}
					var v forwarding.ChainEvent
					if json.Unmarshal([]byte(event.Message), &v) != nil || v.Observation.Class != "established" {
						continue
					}
					if mode == "expired" {
						v.Observation.ObservedAt = v.Observation.ObservedAt.Add(-30 * time.Second)
					} else {
						v.SnapshotDigest = "previous-snapshot"
					}
					raw, _ := json.Marshal(v)
					event.Message = string(raw)
					corrupted.Add(1)
				}
			})
			r.exchange(t, 0).Close()
			r.awaitOperation(t, func(map[string]any) bool { return corrupted.Load() > 0 })
			awaitControlIdle(t, r.h)
			if len(chainFlowFailures(r.operation(t))) == 0 {
				t.Fatal("rejected recovery erased failure")
			}
			corrupt.Store(false)
			r.exchange(t, 0).Close()
			r.awaitOperation(t, func(v map[string]any) bool { return len(chainFlowFailures(v)) == 0 })
			awaitChainIncidentResolved(t, r, incident)
		})
	}
}

func TestChainFailoverRecoveryBetweenRelayHeartbeats(t *testing.T) {
	var failing atomic.Bool
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup", func(i int, d *node.Dependencies) {
		if i != 2 {
			return
		}
		dial := d.Forwarding.DialTarget
		d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
			if failing.Load() {
				return nil, errors.New("target temporarily unavailable")
			}
			return dial(ctx, address)
		}
	})
	r.exchange(t, 0).Close()
	readCounts := func() (int, int) {
		failures, successes := 0, 0
		for _, raw := range eventRows(r.c, "?type=chain_flow&limit=100") {
			row := raw.(map[string]any)
			var v forwarding.ChainEvent
			if err := json.Unmarshal([]byte(row["message"].(string)), &v); err != nil {
				t.Fatal(err)
			}
			if row["node_id"] == r.ids[0] && v.Observation.Class == "exit_target" {
				failures++
			}
			if row["node_id"] == r.ids[2] && v.Observation.Class == "established" {
				successes++
			}
			if row["node_id"] == r.ids[2] && v.Observation.Class != "established" && v.Observation.Class != "local_established" {
				t.Fatal("fixture let owner report intermediate failure")
			}
		}
		return failures, successes
	}
	r.awaitOperation(t, func(map[string]any) bool { _, n := readCounts(); return n == 1 })
	// Real failure and success happen with the clock frozen between heartbeats.
	failing.Store(true)
	assertTCPFailure(t, int(r.entryPorts[0].Load()))
	failing.Store(false)
	r.exchange(t, 0).Close()
	r.awaitOperation(t, func(map[string]any) bool { failures, successes := readCounts(); return failures > 0 && successes >= 2 })
	r.awaitOperation(t, func(v map[string]any) bool { return len(chainFlowFailures(v)) == 0 })
}

func TestChainFailoverRelayQuotaRecoveryResolvesOnlyLocalIncident(t *testing.T) {
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup", func(i int, d *node.Dependencies) {
		if i == 1 {
			d.Forwarding.Limits.MaxConnectionsPerRule = 1
		}
	})
	chainUseSingleEntry(t, r)
	existing := r.exchange(t, 0)
	assertTCPFailure(t, int(r.entryPorts[0].Load()))
	r.awaitOperation(t, func(v map[string]any) bool {
		for _, f := range chainFlowFailures(v) {
			edge := f["Edge"].(map[string]any)
			if f["Class"] == "local_resource" && edge["from_node_id"] == r.ids[1] && edge["to_node_id"] == nil {
				return true
			}
		}
		return false
	})
	incident := incidentForChainRule(t, r, r.rule, true)["id"].(string)
	existing.Close()
	// Wait on successful actual business I/O after the old stream releases quota.
	connectRecovered := func() net.Conn {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.entryPorts[0].Load()), time.Second)
			if err == nil {
				c.SetDeadline(time.Now().Add(300 * time.Millisecond))
				_, err = c.Write([]byte("ping"))
				if err == nil {
					b := make([]byte, 10)
					_, err = io.ReadFull(c, b)
					if err == nil && string(b) != "chain:ping" {
						t.Fatal("wrong recovery response")
					}
				}
				if err == nil {
					c.SetDeadline(time.Time{})
					return c
				}
				c.Close()
			}
			if time.Now().After(deadline) {
				t.Fatal("quota release never restored actual forwarding", err)
			}
			time.Sleep(time.Millisecond)
		}
	}
	recovered := connectRecovered()
	defer func() { recovered.Close() }()
	r.awaitOperation(t, func(v map[string]any) bool { return len(chainFlowFailures(v)) == 0 && chainEdgesReady(v) })
	awaitChainIncidentResolved(t, r, incident)
	// A new real success and another quota refusal share the same clock tick.
	// The owner's new failure sequence must outrank its previous local success.
	recovered.Close()
	recovered = connectRecovered()
	assertTCPFailure(t, int(r.entryPorts[0].Load()))
	r.awaitOperation(t, func(v map[string]any) bool {
		for _, f := range chainFlowFailures(v) {
			if f["Class"] == "local_resource" {
				return true
			}
		}
		return false
	})
	second := incidentForChainRule(t, r, r.rule, true)["id"].(string)
	if second == incident {
		t.Fatal("new resource refusal did not create a new incident")
	}
	recovered.Close()
	recovered = connectRecovered()
	r.awaitOperation(t, func(v map[string]any) bool { return len(chainFlowFailures(v)) == 0 && chainEdgesReady(v) })
	awaitChainIncidentResolved(t, r, second)
}
