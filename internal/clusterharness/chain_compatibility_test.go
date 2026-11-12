package clusterharness_test

import (
	"context"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"sync"
	"testing"
	"time"
)

func withoutChainCapability(report *controlpb.NodeReport) {
	if report.Compatibility == nil {
		return
	}
	caps := report.Compatibility.Capabilities[:0]
	for _, c := range report.Compatibility.Capabilities {
		if c != compatibility.ForwardChains {
			caps = append(caps, c)
		}
	}
	report.Compatibility.Capabilities = caps
}
func TestChainAuthorizationEveryOldPositionFiltered(t *testing.T) {
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup")
	for i := range r.nodes {
		id := r.ids[i]
		r.h.TransformControlReports(func(v *controlpb.NodeReport) {
			if v.NodeId == id {
				withoutChainCapability(v)
			}
		})
		r.nodes[i].Disconnect()
		awaitNode(t, r.c, id, func(v map[string]any) bool { return v["connected"] == false })
		r.nodes[i].Restart()
		r.restore(t, i)
		blocked := awaitRule(t, r.c, r.rule, "整条阻塞（需升级服务器）")
		address := ""
		if i >= r.entries {
			address = r.addresses[i]
		}
		assertPortCapabilityBlocker(t, blocked, id, address)
		r.advance(t, 3)
		for _, rule := range r.snapshot(i).Rules {
			if rule.ChainId != "" {
				t.Fatal("old entry received chain rule", i)
			}
		}
		for _, g := range r.snapshot(i).GetTunnelServer().GetGrants() {
			if g.Rule.ChainId != "" {
				t.Fatal("old executor received chain grant", i)
			}
		}
		r.h.TransformControlReports(nil)
		r.nodes[i].Disconnect()
		awaitNode(t, r.c, id, func(v map[string]any) bool { return v["connected"] == false })
		r.nodes[i].Restart()
		r.restore(t, i)
		awaitRule(t, r.c, r.rule, "正常")
		r.exchange(t, 0)
	}
}
func TestChainAuthorizationBackupUpgradeWaitsForFreshDurableConfirmation(t *testing.T) {
	for _, backup := range []int{2, 4} {
		t.Run([]string{"relay", "terminal"}[backup/2-1], func(t *testing.T) {
			r := newChainRig(t, 1, []int{2, 2}, "primary_backup")
			id := r.ids[backup]
			r.h.TransformControlReports(func(v *controlpb.NodeReport) {
				if v.NodeId == id {
					withoutChainCapability(v)
				}
			})
			r.nodes[backup].Disconnect()
			awaitNode(t, r.c, id, func(v map[string]any) bool { return v["connected"] == false })
			r.nodes[backup].Restart()
			r.restore(t, backup)
			view := awaitRule(t, r.c, r.rule, "可运行但备用成员能力不足")
			assertPortCapabilityBlocker(t, view, id, r.addresses[backup])
			if len(view["configuration_blockers"].([]any)) == 0 {
				t.Fatal("missing backup blocker")
			}
			r.exchange(t, 0)
			gate := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(gate) })
			held := make(chan struct{})
			var holdOnce sync.Once
			r.h.BeforeControlSend(func(ctx context.Context, node string, m *controlpb.PanelMessage) error {
				if m.Snapshot != nil {
					r.mu.Lock()
					r.latest[node] = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
					r.mu.Unlock()
				}
				if node == id && len(m.GetSnapshot().GetTunnelServer().GetGrants()) > 0 {
					holdOnce.Do(func() { close(held) })
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-gate:
					}
				}
				return nil
			})
			r.h.TransformControlReports(nil)
			r.nodes[backup].Disconnect()
			awaitNode(t, r.c, id, func(v map[string]any) bool { return v["connected"] == false })
			// Start the new process with the control link already restored.
			// Await the actual send gate without fast-forwarding its 15-second
			// control deadline while real compilation/crypto is still running.
			r.nodes[backup].Restore()
			r.nodes[backup].Restart()
			select {
			case <-held:
			case <-time.After(5 * time.Second):
				t.Fatal("upgraded grant delivery not exercised")
			}
			// Rules GET must not present the held upgraded grant as confirmed.
			pending := ruleRow(t, r.c, r.rule)["path"].(map[string]any)
			foundPending := false
			for _, raw := range pending["edges"].([]any) {
				edge := raw.(map[string]any)
				if edge["to_node_id"] == id {
					foundPending = true
					if edge["state"] == "ready" || edge["suffix_ready"] == true || edge["grant_generation"] == edge["rule_generation"] && edge["applied_revision"].(float64) > 0 {
						t.Fatal("port GET reused pre-upgrade confirmation", edge)
					}
				}
			}
			if !foundPending {
				t.Fatal("port omitted upgraded candidate pending confirmation", pending)
			}
			if backup == 2 {
				for _, rule := range r.snapshot(0).Rules {
					for _, candidate := range rule.AuthorizedExitIds {
						if candidate == id {
							t.Fatal("upgraded backup reused old confirmation")
						}
					}
				}
			} else {
				for _, relay := range []int{1, 2} {
					for _, g := range r.snapshot(relay).GetTunnelServer().GetGrants() {
						for _, candidate := range g.AuthorizedNextNodeIds {
							if candidate == id {
								t.Fatal("terminal backup reused old confirmation")
							}
						}
					}
				}
			}
			once.Do(func() { close(gate) })
			r.advance(t, 3)
			awaitRule(t, r.c, r.rule, "正常")
			r.ready(t, true)
			r.awaitOperation(t, chainEdgesReady)
			r.assertPortPath(t, ruleRow(t, r.c, r.rule))
			// Break each primary and prove the re-confirmed backup is usable in order.
			if backup == 2 {
				for _, terminal := range r.layers[1] {
					r.links[1][terminal].block(true)
				}
			} else {
				for _, relay := range r.layers[0] {
					r.links[relay][3].block(true)
				}
			}
			r.exchange(t, 0)
		})
	}
}
