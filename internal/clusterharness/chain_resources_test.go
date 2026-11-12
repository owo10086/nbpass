package clusterharness_test

import (
	"context"
	"fmt"
	"io"
	"nbpass/internal/node"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChainFailoverNodeResourceOrderAndRecovery(t *testing.T) {
	for _, limit := range []string{"connections", "pending"} {
		t.Run(limit, func(t *testing.T) {
			var hold atomic.Bool
			entered, release := make(chan struct{}), make(chan struct{})
			var enterOnce, releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			r := newChainRig(t, 1, []int{1, 1}, "primary_backup", func(i int, d *node.Dependencies) {
				if i == 1 {
					if limit == "connections" {
						d.Forwarding.Limits.MaxConnections = 1
					} else {
						d.Forwarding.Limits.MaxPendingDials = 1
					}
				}
				if i == 2 {
					dial := d.Forwarding.DialTarget
					d.Forwarding.DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
						if hold.Load() {
							enterOnce.Do(func() { close(entered) })
							select {
							case <-ctx.Done():
								return nil, ctx.Err()
							case <-release:
							}
						}
						return dial(ctx, address)
					}
				}
			})
			chainUseSingleEntry(t, r)
			// Start just before the first heartbeat so pending establishment stays
			// within its original budget while the refusal is reported.
			r.advance(t, 9)
			prior := r.exchange(t, 0)
			defer prior.Close()
			at := r.h.Clock.Now()
			var pending net.Conn
			if limit == "pending" {
				prior.Close()
				hold.Store(true)
				var err error
				pending, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.entryPorts[0].Load()), time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer pending.Close()
				if _, err = pending.Write([]byte("ping")); err != nil {
					t.Fatal(err)
				}
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("actual downstream dial not held")
				}
			}
			assertTCPFailure(t, int(r.entryPorts[0].Load()))
			if !r.h.Clock.Now().Equal(at) {
				t.Fatal("success/refusal did not share an observation timestamp")
			}
			hasRefusal := func(v map[string]any) bool {
				for _, f := range chainFlowFailures(v) {
					edge := f["Edge"].(map[string]any)
					if f["Class"] == "local_resource" && edge["from_node_id"] == r.ids[1] && edge["to_node_id"] == nil {
						return true
					}
				}
				return false
			}
			r.advance(t, 1)
			deadline := time.Now().Add(2 * time.Second)
			for !hasRefusal(r.operation(t)) {
				if time.Now().After(deadline) {
					t.Fatal("same-timestamp node quota refusal was lost", r.operation(t))
				}
				time.Sleep(time.Millisecond)
			}
			incident := incidentForChainRule(t, r, r.rule, true)["id"].(string)
			if limit == "pending" {
				hold.Store(false)
				releaseOnce.Do(func() { close(release) })
				pending.SetReadDeadline(time.Now().Add(time.Second))
				b := make([]byte, 10)
				if _, err := io.ReadFull(pending, b); err != nil || string(b) != "chain:ping" {
					t.Fatal("held business stream did not recover", string(b), err)
				}
				pending.Close()
			} else {
				prior.Close()
				// Retry actual I/O until asynchronous RST cleanup releases the occupied slot.
				deadline := time.Now().Add(3 * time.Second)
				for {
					c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.entryPorts[0].Load()), time.Second)
					if err == nil {
						c.SetDeadline(time.Now().Add(200 * time.Millisecond))
						_, err = c.Write([]byte("ping"))
						if err == nil {
							b := make([]byte, 10)
							_, err = io.ReadFull(c, b)
							if err == nil && string(b) != "chain:ping" {
								t.Fatal("wrong recovery response")
							}
						}
						c.Close()
					}
					if err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("released quota did not recover", err)
					}
					time.Sleep(time.Millisecond)
				}
			}
			r.awaitOperation(t, func(v map[string]any) bool { return len(chainFlowFailures(v)) == 0 && chainEdgesReady(v) })
			awaitChainIncidentResolved(t, r, incident)
		})
	}
}
