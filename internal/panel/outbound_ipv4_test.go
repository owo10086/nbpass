package panel

import (
	"context"
	"database/sql"
	"fmt"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"testing"
)

func TestOutboundIPv4APIAndCapabilityFiltering(t *testing.T) {
	p, request := ixAddressAPI(t)
	id := request("POST", "/api/nodes", map[string]string{"name": "A"}, 201)["id"].(string)
	path := "/api/nodes/" + id + "/outbound"
	request("GET", "/api/nodes/missing/outbound", nil, 404)
	request("PUT", "/api/nodes/missing/outbound", map[string]string{"outbound_ipv4": "127.0.0.1"}, 404)
	for _, bad := range []string{"::1", "::ffff:127.0.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "host.test", "127.1", "127.0.0.1:80"} {
		request("PUT", path, map[string]string{"outbound_ipv4": bad}, 400)
	}
	request("POST", "/api/rules", map[string]any{"name": "direct", "entry_node_id": id, "kind": "direct", "listen_port": 23001, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)
	saved := request("PUT", path, map[string]string{"outbound_ipv4": " 127.0.0.1 "}, 200)
	if saved["outbound_ipv4"] != "127.0.0.1" || request("GET", path, nil, 200)["outbound_ipv4"] != "127.0.0.1" {
		t.Fatal(saved)
	}
	m, err := p.controlMessage(context.Background(), id)
	if err != nil || len(m.Snapshot.Rules) != 0 {
		t.Fatal("old node received source rule", m, err)
	}
	views := request("GET", "/api/rules", nil, 200)["rules"].([]any)[0].(map[string]any)
	blockers := views["configuration_blockers"].([]any)
	if len(blockers) != 1 || blockers[0].(map[string]any)["capability"] != compatibility.OutboundIPv4 {
		t.Fatal(views)
	}
	c := nodeCompatibility{ProcessInstance: "current", ControlProtocol: 2, Node: compatibility.Current(), Capabilities: []string{compatibility.OutboundIPv4}}
	if err = p.commitForwarding(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE nodes SET process_instance='current' WHERE id=?", id); err != nil {
			return err
		}
		return p.saveNodeCompatibility(context.Background(), tx, id, c)
	}); err != nil {
		t.Fatal(err)
	}
	m, err = p.controlMessage(context.Background(), id)
	if err != nil || len(m.Snapshot.Rules) != 1 {
		t.Fatal(m, err)
	}
	before := m.Snapshot.Revision
	diskRequest(t, p, "PUT", "/api/nodes/"+id+"/name", map[string]string{"name": "renamed"}, diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0], 204)
	m, _ = p.controlMessage(context.Background(), id)
	if m.Snapshot.Revision != before {
		t.Fatal("rename advanced revision")
	}
	nodes := request("GET", "/api/nodes", nil, 200)["nodes"].([]any)[0].(map[string]any)
	if nodes["outbound_ipv4"] != "127.0.0.1" || nodes["name"] != "renamed" {
		t.Fatal(nodes)
	}
	request("PUT", path, map[string]string{"outbound_ipv4": ""}, 200)
	m, _ = p.controlMessage(context.Background(), id)
	if len(m.Snapshot.Rules) != 1 {
		t.Fatal("clear lost rule")
	}
}

func TestOutboundIPv4Migration49Preserves48NodesAndRules(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:48] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO nodes(id,name,created_at,desired_revision) VALUES('old','旧服务器',1,17)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	var source, name string
	var revision, version int
	if err = p.db.QueryRow("SELECT name,desired_revision,outbound_ipv4 FROM nodes WHERE id='old'").Scan(&name, &revision, &source); err != nil || source != "" || name != "旧服务器" || revision != 17 {
		t.Fatal(source, name, revision, err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 49 {
		t.Fatal(version, err)
	}
}

func TestOutboundIPv4RevalidationReportRejectsStaleBoundaries(t *testing.T) {
	p, request := ixAddressAPI(t)
	id := request("POST", "/api/nodes", map[string]string{"name": "A"}, 201)["id"].(string)
	_, token, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	c := nodeCompatibility{ProcessInstance: "current", ControlProtocol: 2, Node: compatibility.Current(), Capabilities: []string{compatibility.OutboundIPv4}}
	if err = p.commitForwarding(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE nodes SET token_hash=?,process_instance='current',last_seen_at=? WHERE id=?", token, p.deps.Clock.Now().UnixNano(), id); err != nil {
			return err
		}
		return p.saveNodeCompatibility(context.Background(), tx, id, c)
	}); err != nil {
		t.Fatal(err)
	}
	ruleID := request("POST", "/api/rules", map[string]any{"name": "direct", "entry_node_id": id, "kind": "direct", "listen_port": 23001, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)["id"].(string)
	request("PUT", "/api/nodes/"+id+"/outbound", map[string]string{"outbound_ipv4": "127.0.0.1"}, 200)
	message, err := p.controlMessage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	s := message.Snapshot
	session := &controlSession{done: make(chan struct{}), instance: "current", compatibility: &c}
	p.controls[id] = session
	success := &controlpb.NodeReport{ProtocolVersion: 2, ProcessInstance: "current", AttemptedRevision: s.Revision, AttemptedDigest: s.Digest, AppliedRevision: s.Revision, AppliedDigest: s.Digest, RevocationsApplied: true}
	send := func(report *controlpb.NodeReport) {
		t.Helper()
		if _, err := p.acceptReport(context.Background(), id, token, session, report); err != nil {
			t.Fatal(err)
		}
	}
	send(success)
	failure := &controlpb.NodeReport{ProtocolVersion: 2, ProcessInstance: "current", AttemptedRevision: s.Revision, AttemptedDigest: s.Digest, RevocationsApplied: true, ApplyError: "出站 IP 127.0.0.1 不是本机接口 IPv4 地址", RuleResults: []*controlpb.RuleApplyResult{{RuleId: ruleID, ApplyError: "出站 IP 127.0.0.1 不是本机接口 IPv4 地址"}}}
	assertConfirmed := func() {
		t.Helper()
		var applied uint64
		var digest, applyError string
		if err := p.db.QueryRow("SELECT applied_revision,applied_digest,apply_error FROM nodes WHERE id=?", id).Scan(&applied, &digest, &applyError); err != nil || applied != s.Revision || digest != s.Digest || applyError != "" {
			t.Fatal("stale report replaced confirmation", applied, digest, applyError, err)
		}
	}
	for _, kind := range []string{"old_revision", "old_digest", "old_source", "no_checkpoint", "no_capability", "old_session", "old_process"} {
		t.Run(kind, func(t *testing.T) {
			report := proto.Clone(failure).(*controlpb.NodeReport)
			switch kind {
			case "old_revision":
				report.AttemptedRevision--
			case "old_digest":
				report.AttemptedDigest = "old"
			case "old_source":
				report.ApplyError = "出站 IP 127.0.0.2 不是本机接口 IPv4 地址"
			case "no_checkpoint":
				report.RevocationsApplied = false
			case "no_capability":
				session.compatibility = &nodeCompatibility{ControlProtocol: 2, Node: compatibility.Current()}
				defer func() { session.compatibility = &c }()
			case "old_process":
				report.ProcessInstance = "retired"
			case "old_session":
				if _, err := p.acceptReport(context.Background(), id, token, &controlSession{done: make(chan struct{}), instance: "current", compatibility: &c}, report); err == nil {
					t.Fatal("old session accepted")
				}
				assertConfirmed()
				return
			}
			if kind == "old_process" {
				if _, err := p.acceptReport(context.Background(), id, token, session, report); err == nil {
					t.Fatal("old process accepted")
				}
			} else {
				send(report)
			}
			assertConfirmed()
		})
	}
	send(failure)
	var applied uint64
	var applyError string
	if err := p.db.QueryRow("SELECT applied_revision,apply_error FROM nodes WHERE id=?", id).Scan(&applied, &applyError); err != nil || applied != 0 || applyError == "" {
		t.Fatal("fresh revalidation failure hidden", applied, applyError, err)
	}
	views, err := p.ruleSyncViews(context.Background())
	if err != nil || views[0].SyncStatus != "同步失败" {
		t.Fatal(views, err)
	}
	send(success)
	assertConfirmed()
}
