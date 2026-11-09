package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

func exitAddressProtocolFixture(t *testing.T) (*Panel, string, string, []byte, *controlSession, *controlpb.ConfigSnapshot) {
	t.Helper()
	p, entry, exit, exitToken, initial := tunnelAuthorizationProtocolFixture(t)
	ctx := context.Background()
	_, token, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec("UPDATE nodes SET token_hash=? WHERE id=?", token, entry); err != nil {
		t.Fatal(err)
	}
	xs := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "current"}
	es := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "current"}
	p.controls[exit], p.controls[entry] = xs, es
	if _, err = p.acceptReport(ctx, exit, exitToken, xs, rotationReport(initial)); err != nil {
		t.Fatal(err)
	}
	now := p.deps.Clock.Now().UnixNano()
	if _, err = p.db.Exec(`INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('address-a',?,'8.8.8.8','authorized',?,?,?,'s')`, exit, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err = p.acceptRotation(ctx, exit, &controlpb.RotationProgress{ActionId: "address-a", OldIp: "8.8.8.8", NewIp: "1.1.1.1", State: "changed", Reason: "changed", Sequence: 1}); err != nil {
		t.Fatal(err)
	}
	m, err := p.controlMessage(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	return p, entry, exit, token, es, m.Snapshot
}

// 协议物理属性：完整应用确认与握手证据独立，旧会话/旧版本/旧进度均不能冒领。
func TestExitAddressConfirmationReportIsolation(t *testing.T) {
	p, entry, exit, token, session, s := exitAddressProtocolFixture(t)
	ctx := context.Background()
	reportFor := func() *controlpb.NodeReport {
		r := rotationReport(s)
		for _, rule := range s.Rules {
			r.RuleResults = append(r.RuleResults, &controlpb.RuleApplyResult{RuleId: rule.Id, AppliedRevision: s.Revision})
		}
		return r
	}
	assertProgress := func(applied, handshake bool) {
		t.Helper()
		var rev, at int64
		if err := p.db.QueryRow("SELECT applied_revision,handshaken_at FROM exit_address_progress WHERE action_id='address-a' AND entry_id=?", entry).Scan(&rev, &at); err != nil {
			t.Fatal(err)
		}
		if (rev > 0) != applied || (at > 0) != handshake {
			t.Fatalf("两类确认串扰: revision=%d handshake=%d", rev, at)
		}
	}
	assertProgress(false, false)
	for _, kind := range []string{"old", "future", "digest", "not_durable", "failure", "probe", "events", "checker", "dns", "self_check", "traffic", "rotation", "session", "process"} {
		t.Run(kind, func(t *testing.T) {
			report := reportFor()
			active := session
			switch kind {
			case "old":
				report.AttemptedRevision--
				report.AppliedRevision--
			case "future":
				report.AttemptedRevision++
				report.AppliedRevision++
			case "digest":
				report.AppliedDigest = "wrong"
			case "not_durable":
				report.RevocationsApplied = false
			case "failure":
				report.ApplyError = "磁盘失败"
			case "probe":
				report.ProbeOnly = true
			case "events":
				report.EventsOnly = true
			case "checker":
				report.CheckerOnly = true
			case "dns":
				report.DnsWatchOnly = true
			case "self_check":
				report.SelfCheckOnly = true
			case "traffic":
				report.TrafficOnly = true
			case "rotation":
				report.RotationOnly = true
				report.Rotation = &controlpb.RotationProgress{}
			case "session":
				active = &controlSession{instance: "current", done: make(chan struct{})}
			case "process":
				report.ProcessInstance = "old"
			}
			_, err := p.acceptReport(ctx, entry, token, active, report)
			if err != nil && kind != "session" && kind != "process" && kind != "digest" {
				t.Fatal(err)
			}
			assertProgress(false, false)
		})
	}
	// 配置报告混入成功探针也不能确认握手。
	report := reportFor()
	health := &controlpb.ExitHealth{ExitId: exit, AddressVersion: "address-a", State: "healthy", LastProbe: "success", CheckedAtUnix: p.deps.Clock.Now().Unix()}
	report.Probe = &controlpb.ProbeSample{Sequence: 1, ExitHealth: []*controlpb.ExitHealth{health}}
	if _, err := p.acceptReport(ctx, entry, token, session, report); err != nil {
		t.Fatal(err)
	}
	assertProgress(true, false)
	// 失败、旧地址、旧协议无版本、未来与过期探针都不能确认。
	for i, kind := range []string{"failure", "old_address", "no_version", "future", "stale", "valid"} {
		h := proto.Clone(health).(*controlpb.ExitHealth)
		switch kind {
		case "failure":
			h.LastProbe = "failure"
		case "old_address":
			h.AddressVersion = "previous"
		case "no_version":
			h.AddressVersion = ""
		case "future":
			h.CheckedAtUnix += 60
		case "stale":
			h.CheckedAtUnix -= 60
		}
		probeReport := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current", ProbeOnly: true, Probe: &controlpb.ProbeSample{Sequence: uint64(i + 1), ExitHealth: []*controlpb.ExitHealth{h}}}
		if _, err := p.acceptReport(ctx, entry, token, session, probeReport); err != nil {
			t.Fatal(err)
		}
		assertProgress(true, kind == "valid")
	}
}

func TestExitAddressManualEndpointEditInvalidatesProgress(t *testing.T) {
	p, entry, exit, _, _, s := exitAddressProtocolFixture(t)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	path := "/api/nodes/" + exit
	view := func() map[string]any {
		var v map[string]any
		if err := json.Unmarshal(diskRequest(t, p, "GET", path+"/exit-blocked", nil, cookie, 200).Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if len(view()["entries"].([]any)) != 1 {
		t.Fatal("缺少关联入口")
	}
	ep := s.ExitGroups[0].Exits[0]
	diskRequest(t, p, "PUT", path+"/tunnel", map[string]any{"address": "9.9.9.9", "listen_port": ep.Port, "transport": "wss"}, cookie, 200)
	v := view()
	if v["action_id"] != "" || len(v["entries"].([]any)) != 0 {
		t.Fatalf("手工端点变化复用了旧进度: %v (入口 %s)", v, entry)
	}
}

// 解除最后一条关联后不再等待该入口，重新关联也不能复用撤销前的确认。
func TestExitAddressProgressFollowsCurrentParticipants(t *testing.T) {
	p, entry, exit, _, _, s := exitAddressProtocolFixture(t)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	path := "/api/nodes/" + exit + "/exit-blocked"
	view := func() []any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal(diskRequest(t, p, "GET", path, nil, cookie, 200).Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v["entries"].([]any)
	}
	if _, err := p.db.Exec("UPDATE exit_address_progress SET applied_revision=7,handshaken_at=1 WHERE entry_id=?", entry); err != nil {
		t.Fatal(err)
	}
	rule := s.Rules[0]
	diskRequest(t, p, "DELETE", "/api/rules/"+rule.Id, nil, cookie, 204)
	if entries := view(); len(entries) != 0 {
		t.Fatalf("已解除关联的入口仍参与等待: %v", entries)
	}
	body := map[string]any{"name": "重新关联", "entry_group_id": rule.EntryGroupId, "exit_group_id": rule.ExitGroupId, "kind": "tunnel", "listen_port": rule.ListenPort, "target_address": rule.TargetAddress, "target_family": "ipv4", "target_port": rule.TargetPort, "enabled": true}
	diskRequest(t, p, "POST", "/api/rules", body, cookie, 201)
	entries := view()
	if len(entries) != 1 {
		t.Fatalf("重新关联入口未登记: %v", entries)
	}
	e := entries[0].(map[string]any)
	if e["applied"] != false || e["handshaken"] != false {
		t.Fatalf("重新关联复用旧确认: %v", e)
	}
}

func TestExitBlockedMigrationPreservesVersion24(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:24] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('exit','旧出口',0);INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext) VALUES('exit','8.8.8.8',8443,'wss','stable.identity','certificate',X'1234');INSERT INTO rotation_settings(node_id,secret_name,automatic_enabled) VALUES('exit','secret',1)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	defer p.Close()
	var address, name, version, cert string
	var enabled, auto bool
	if err = p.db.QueryRow("SELECT address,server_name,address_version,certificate_pem,blocked_enabled FROM tunnel_exits WHERE node_id='exit'").Scan(&address, &name, &version, &cert, &enabled); err != nil {
		t.Fatal(err)
	}
	if address != "8.8.8.8" || name != "stable.identity" || cert != "certificate" || version != "" || enabled {
		t.Fatalf("升级改变旧出口或默认开启策略: %s %s %s %s %v", address, name, version, cert, enabled)
	}
	if err = p.db.QueryRow("SELECT automatic_enabled FROM rotation_settings WHERE node_id='exit'").Scan(&auto); err != nil || !auto {
		t.Fatal("升级改变节点总授权", auto, err)
	}
	var v int
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatal(v, err)
	}
}
