package panel

import (
	"context"
	"database/sql"
	"fmt"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"testing"
)

func chainConfirmFixture(t *testing.T) (*Panel, []string, string) {
	t.Helper()
	p, request := forwardChainsAPI(t)
	ids := []string{}
	for i := 0; i < 3; i++ {
		id := request("POST", "/api/nodes", map[string]any{"name": fmt.Sprint(i)}, 201)["id"].(string)
		ids = append(ids, id)
		if i > 0 {
			request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 24000 + i, "transport": "wss"}, 200)
		}
	}
	groups := []string{}
	for i := 1; i < 3; i++ {
		groups = append(groups, request("POST", "/api/exit-groups", map[string]any{"name": fmt.Sprint(i), "members": ids[i : i+1], "strategy": "primary_backup"}, 201)["id"].(string))
	}
	chain := request("POST", "/api/forward-chains", map[string]any{"name": "链", "exit_group_ids": groups}, 201)["id"].(string)
	rule := request("POST", "/api/rules", map[string]any{"name": "链端口", "entry_node_id": ids[0], "kind": "tunnel", "chain_id": chain, "listen_port": 27000, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)["id"].(string)
	err := p.commitForwarding(context.Background(), func(tx *sql.Tx) error {
		if _, e := tx.Exec("UPDATE nodes SET process_instance='fixture',tunnel_authorization_version=1,exit_failover_version=1"); e != nil {
			return e
		}
		for _, id := range ids {
			c := nodeCompatibility{ProcessInstance: "fixture", ControlProtocol: 2, Node: compatibility.Current()}
			c.Capabilities = c.Node.Capabilities
			if e := p.saveNodeCompatibility(context.Background(), tx, id, c); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p, ids, rule
}
func chainReport(s *controlpb.ConfigSnapshot) *controlpb.NodeReport {
	r := &controlpb.NodeReport{AttemptedRevision: s.Revision, AttemptedDigest: s.Digest, AppliedRevision: s.Revision, AppliedDigest: s.Digest, RevocationsApplied: true}
	for _, g := range s.GetTunnelServer().GetGrants() {
		r.TunnelGrantResults = append(r.TunnelGrantResults, &controlpb.TunnelGrantApplyResult{EntryId: g.EntryId, RuleId: g.Rule.Id, Generation: g.Rule.AuthorizationGeneration, AppliedRevision: s.Revision})
	}
	return r
}
func TestChainGrantConfirmationBeforeEntry(t *testing.T) {
	p, ids, _ := chainConfirmFixture(t)
	ctx := context.Background()
	snapshot := func(id string) *controlpb.ConfigSnapshot {
		t.Helper()
		m, e := p.controlMessage(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		return m.Snapshot
	}
	if len(snapshot(ids[0]).Rules) != 0 {
		t.Fatal("entry enabled without durable grants")
	}
	if len(snapshot(ids[1]).GetTunnelServer().GetGrants()) != 0 {
		t.Fatal("relay authorized before terminal confirmation")
	}
	terminal := snapshot(ids[2])
	if len(terminal.GetTunnelServer().GetGrants()) != 1 {
		t.Fatal("terminal grant absent")
	}
	confirm := func(id string) {
		t.Helper()
		s := snapshot(id)
		tx, e := p.db.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		_, e = p.confirmTunnelAuthorizations(ctx, tx, id, chainReport(s), s.Revision, s.Digest)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	confirm(ids[2])
	if len(snapshot(ids[0]).Rules) != 0 {
		t.Fatal("terminal ACK enabled entry before relay ACK")
	}
	if len(snapshot(ids[1]).GetTunnelServer().GetGrants()) != 1 {
		t.Fatal("relay grant missing after terminal ACK")
	}
	confirm(ids[1])
	if len(snapshot(ids[0]).Rules) != 1 {
		t.Fatal("complete confirmed path not enabled")
	}
	before := snapshot(ids[0]).Revision
	for i := 0; i < 3; i++ {
		confirm(ids[2])
		confirm(ids[1])
	}
	if snapshot(ids[0]).Revision != before {
		t.Fatal("same content confirmation loops revisions")
	}
}

func TestChainGrantReportsRequireExactCompleteCurrentEvidence(t *testing.T) {
	for _, name := range []string{"complete", "missing", "duplicate", "forged", "generation", "future", "disk_failure", "checkpoint", "old_revision", "old_digest", "partial_other_listener"} {
		t.Run(name, func(t *testing.T) {
			p, ids, _ := chainConfirmFixture(t)
			ctx := context.Background()
			m, e := p.controlMessage(ctx, ids[2])
			if e != nil {
				t.Fatal(e)
			}
			s := m.Snapshot
			r := chainReport(s)
			switch name {
			case "missing":
				r.TunnelGrantResults = nil
			case "duplicate":
				r.TunnelGrantResults = append(r.TunnelGrantResults, r.TunnelGrantResults[0])
			case "forged":
				r.TunnelGrantResults[0].EntryId = ids[0]
			case "generation":
				r.TunnelGrantResults[0].Generation++
			case "future":
				r.TunnelGrantResults[0].AppliedRevision++
			case "disk_failure":
				r.ApplyError = "disk failed"
				r.AppliedRevision = 0
				r.AppliedDigest = ""
				r.TunnelGrantResults[0].AppliedRevision = 0
				r.TunnelGrantResults[0].ApplyError = "disk failed"
			case "checkpoint":
				r.RevocationsApplied = false
			case "old_revision":
				r.AttemptedRevision--
				r.AppliedRevision--
			case "old_digest":
				r.AttemptedDigest = "old"
				r.AppliedDigest = "old"
			case "partial_other_listener":
				r.AppliedRevision = 0
				r.AppliedDigest = ""
				r.ApplyError = "unrelated bind error"
			}
			tx, e := p.db.BeginTx(ctx, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			_, e = p.confirmTunnelAuthorizations(ctx, tx, ids[2], r, s.Revision, s.Digest)
			invalid := name == "missing" || name == "duplicate" || name == "forged" || name == "generation" || name == "future" || name == "checkpoint"
			if (e != nil) != invalid {
				t.Fatalf("unexpected validation: %v", e)
			}
			if invalid {
				return
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			next, e := p.controlMessage(ctx, ids[1])
			if e != nil {
				t.Fatal(e)
			}
			want := name == "complete" || name == "partial_other_listener"
			if (len(next.Snapshot.GetTunnelServer().GetGrants()) > 0) != want {
				t.Fatal("grant readiness accepted wrong evidence", name, next)
			}
		})
	}
}

func TestChainPublicAddressRotationIncludesOfflineDirectPredecessorsOnly(t *testing.T) {
	p, ids, _ := chainConfirmFixture(t)
	ctx := context.Background()
	for _, hop := range []int{2, 1} {
		action := fmt.Sprint("public-rotation-", hop)
		if e := p.commitForwarding(ctx, func(tx *sql.Tx) error {
			if _, e := tx.Exec("INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES(?,?,'8.8.8.8','changed',0,0,0,'fixture')", action, ids[hop]); e != nil {
				return e
			}
			_, e := p.rotateExitAddress(ctx, tx, &rotationAction{ID: action, NodeID: ids[hop]}, "8.8.4.4")
			return e
		}); e != nil {
			t.Fatal(e)
		}
		var entry string
		var count int
		if e := p.db.QueryRow("SELECT count(*),entry_id FROM exit_address_progress WHERE action_id=?", action).Scan(&count, &entry); e != nil || count != 1 || entry != ids[hop-1] {
			t.Fatal("rotation used original entry instead of all actual direct predecessors", hop, count, entry, e)
		}
		var applied uint64
		if e := p.db.QueryRow("SELECT applied_revision FROM exit_address_progress WHERE action_id=?", action).Scan(&applied); e != nil || applied != 0 {
			t.Fatal("offline predecessor counted as applied", applied, e)
		}
	}
	if e := p.commitForwarding(ctx, func(tx *sql.Tx) error {
		if _, e := tx.Exec("INSERT INTO tunnel_addresses VALUES(?,'ix','private','10.0.0.9',NULL,0)", ids[2]); e != nil {
			return e
		}
		_, e := tx.Exec("UPDATE exit_group_members SET address_id='ix' WHERE node_id=?", ids[2])
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := p.commitForwarding(ctx, func(tx *sql.Tx) error {
		if _, e := tx.Exec("INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('private-not-consumer',?,'8.8.8.8','changed',0,0,0,'fixture')", ids[2]); e != nil {
			return e
		}
		_, e := p.rotateExitAddress(ctx, tx, &rotationAction{ID: "private-not-consumer", NodeID: ids[2]}, "1.1.1.1")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	var count int
	p.db.QueryRow("SELECT count(*) FROM exit_address_progress WHERE action_id='private-not-consumer'").Scan(&count)
	if count != 0 {
		t.Fatal("dedicated route became public rotation consumer")
	}
	var address string
	if e := p.db.QueryRow("SELECT address FROM tunnel_addresses WHERE node_id=? AND id='ix'", ids[2]).Scan(&address); e != nil || address != "10.0.0.9" {
		t.Fatal("public rotation rewrote dedicated address", address, e)
	}
}
