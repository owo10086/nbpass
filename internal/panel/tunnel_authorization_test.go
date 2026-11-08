package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

func tunnelAuthorizationProtocolFixture(t *testing.T) (*Panel, string, string, []byte, *controlpb.ConfigSnapshot) {
	t.Helper()
	p := openDiskPanel(t, t.TempDir())
	t.Cleanup(func() { p.Close() })
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	request := func(method, path string, body any, code int) map[string]any {
		t.Helper()
		v := map[string]any{}
		if err := json.Unmarshal(diskRequest(t, p, method, path, body, cookie, code).Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	entry := request("POST", "/api/nodes", map[string]string{"name": "入口"}, 201)["id"].(string)
	exit := request("POST", "/api/nodes", map[string]string{"name": "出口"}, 201)["id"].(string)
	_, tokenDigest, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET token_hash=?,tunnel_authorization_version=1 WHERE id=?", tokenDigest, exit); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_authorization_version=1 WHERE id=?", entry); err != nil {
		t.Fatal(err)
	}
	g := request("POST", "/api/entry-groups", map[string]any{"name": "入口组", "members": []string{entry}}, 201)["id"].(string)
	request("PUT", "/api/nodes/"+exit+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 22001, "transport": "wss"}, 200)
	xg := request("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{exit}, "strategy": "primary_backup"}, 201)["id"].(string)
	request("POST", "/api/rules", map[string]any{"name": "规则", "entry_group_id": g, "exit_group_id": xg, "kind": "tunnel", "listen_port": 22002, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)
	m, err := p.controlMessage(context.Background(), exit)
	if err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot
	return p, entry, exit, tokenDigest, s
}

// 协议物理边界：只接受当前出口、当前会话、当前完整快照的落盘成功确认。
func TestTunnelAuthorizationConfirmationReportIsolation(t *testing.T) {
	p, entry, exit, tokenDigest, s := tunnelAuthorizationProtocolFixture(t)
	session := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "current"}
	p.controls[exit] = session
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: s.Revision, AttemptedDigest: s.Digest, AppliedRevision: s.Revision, AppliedDigest: s.Digest, RevocationsApplied: true}
	assertGate := func(want int) {
		t.Helper()
		entryConfig, err := p.controlMessage(context.Background(), entry)
		if err != nil || len(entryConfig.GetSnapshot().GetRules()) != want {
			t.Fatalf("授权门禁规则数量: got=%d want=%d err=%v", len(entryConfig.GetSnapshot().GetRules()), want, err)
		}
	}
	assertGate(0)
	for _, kind := range []string{"old", "future", "attempted_digest", "applied_digest", "probe", "events", "heartbeat", "failure", "not_durable", "session", "process", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			r := proto.Clone(report).(*controlpb.NodeReport)
			active := session
			switch kind {
			case "old":
				r.AttemptedRevision--
				r.AppliedRevision--
			case "future":
				r.AttemptedRevision++
				r.AppliedRevision++
			case "attempted_digest":
				r.AttemptedDigest = "wrong"
			case "applied_digest":
				r.AppliedDigest = "wrong"
			case "probe":
				r.ProbeOnly = true
			case "events":
				r.EventsOnly = true
			case "heartbeat":
				r.AttemptedRevision = 0
				r.AttemptedDigest = ""
			case "failure":
				r.ApplyError = "写盘失败"
			case "not_durable":
				r.RevocationsApplied = false
			case "session":
				active = &controlSession{instance: "current"}
			case "process":
				r.ProcessInstance = "retired"
			case "unsupported":
				if _, err := p.db.Exec("UPDATE nodes SET tunnel_authorization_version=0 WHERE id=?", exit); err != nil {
					t.Fatal(err)
				}
			}
			_, err := p.acceptReport(context.Background(), exit, tokenDigest, active, r)
			if (kind == "session" || kind == "process") != (err != nil) {
				t.Fatalf("报告处理错误: %v", err)
			}
			assertGate(0)
		})
	}
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_authorization_version=1 WHERE id=?", exit); err != nil {
		t.Fatal(err)
	}
	if _, err := p.acceptReport(context.Background(), exit, tokenDigest, session, report); err != nil {
		t.Fatal(err)
	}
	assertGate(1)
	// 重复确认不能制造新 revision，亦不能撤销已确认代次。
	entryConfig, _ := p.controlMessage(context.Background(), entry)
	if _, err := p.acceptReport(context.Background(), exit, tokenDigest, session, report); err != nil {
		t.Fatal(err)
	}
	next, _ := p.controlMessage(context.Background(), entry)
	if next.DesiredRevision != entryConfig.DesiredRevision {
		t.Fatal("重复确认推进了入口 revision")
	}
	// 入口能力降级会撤掉远端 grants；再次升级必须重新等出口确认。
	changeCapability := func(version uint32) {
		t.Helper()
		tx, err := p.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := p.admitTunnelAuthorization(context.Background(), tx, entry, true, version); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	changeCapability(0)
	assertGate(0)
	revoked, err := p.controlMessage(context.Background(), exit)
	if err != nil {
		t.Fatal(err)
	}
	ackRevoked := proto.Clone(report).(*controlpb.NodeReport)
	ackRevoked.AttemptedRevision, ackRevoked.AppliedRevision = revoked.Snapshot.Revision, revoked.Snapshot.Revision
	ackRevoked.AttemptedDigest, ackRevoked.AppliedDigest = revoked.Snapshot.Digest, revoked.Snapshot.Digest
	if _, err := p.acceptReport(context.Background(), exit, tokenDigest, session, ackRevoked); err != nil {
		t.Fatal(err)
	}
	changeCapability(1)
	assertGate(0)
}

// 真实版本 13 升级仅追加授权表/能力列，不修改已有快照、端口及逐规则结果。
func TestTunnelAuthorizationMigrationPreservesVersionThirteenData(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, migration := range migrations[:13] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"INSERT INTO nodes(id,name,created_at,desired_revision,tunnel_listen_port,check_port) VALUES('n','已有节点',0,8,23443,18080)",
		"INSERT INTO entry_groups(id,name) VALUES('g','已有入口组')",
		"INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled) VALUES('r','已有规则','g','direct',23444,'8.8.8.8','ipv4',443,0,1)",
		"INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES('n',X'010203','原摘要')",
		"INSERT INTO node_rule_reports(node_id,revision,digest,revocations_applied) VALUES('n',8,'原摘要',1)",
		"INSERT INTO node_rule_results(node_id,rule_id,entry_group_id,listen_port,applied_revision,apply_error) VALUES('n','r','g',23444,8,'')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	defer p.Close()
	var version, capability, port, applied int
	var data []byte
	if err := p.db.QueryRow(`SELECT tunnel_authorization_version,tunnel_listen_port,snapshot,node_rule_results.applied_revision FROM nodes JOIN node_snapshots ON node_id=nodes.id JOIN node_rule_results USING(node_id) WHERE nodes.id='n'`).Scan(&capability, &port, &data, &applied); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) || capability != 0 || port != 23443 || applied != 8 || string(data) != "\x01\x02\x03" {
		t.Fatal("迁移改写了旧配置或错误宣称旧节点支持新能力")
	}
	for _, query := range []string{
		"INSERT INTO rule_authorizations(rule_id,generation,fingerprint) VALUES('r',1,'fingerprint')",
		"INSERT INTO tunnel_authorization_confirmations(exit_id,entry_id,rule_id,generation) VALUES('n','n','r',1)",
		"DELETE FROM forward_rules WHERE id='r'",
	} {
		if _, err := p.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := p.db.QueryRow("SELECT count(*) FROM tunnel_authorization_confirmations").Scan(&count); err != nil || count != 0 {
		t.Fatal("删除规则未清理历史授权确认")
	}
}

// 已有零代次授权在当前/上一版节点混用时保持互通；只有新/变化授权等待新能力。
func TestLegacyTunnelAuthorizationSurvivesRollingUpgrade(t *testing.T) {
	for _, versions := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		t.Run(fmt.Sprint(versions), func(t *testing.T) {
			p, entry, exit, tokenDigest, server := tunnelAuthorizationProtocolFixture(t)
			server.TunnelServer.Grants[0].Rule.AuthorizationGeneration = 0
			legacyRule := proto.Clone(server.TunnelServer.Grants[0].Rule).(*controlpb.ForwardRule)
			entryMessage, err := p.controlMessage(context.Background(), entry)
			if err != nil {
				t.Fatal(err)
			}
			entryMessage.Snapshot.Rules = []*controlpb.ForwardRule{legacyRule}
			// 物理夹具代表票08/v13已下发的双端快照；不伪造授权确认。
			for id, snapshot := range map[string]*controlpb.ConfigSnapshot{entry: entryMessage.Snapshot, exit: server} {
				snapshot.Digest = forwarding.SnapshotDigest(snapshot)
				data, err := proto.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				data, err = p.secrets.(*sealedSecrets).seal("node-snapshot:"+id, data)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := p.db.Exec("UPDATE node_snapshots SET snapshot=?,digest=?,encrypted=1 WHERE node_id=?", data, snapshot.Digest, id); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.db.Exec("UPDATE nodes SET tunnel_authorization_version=? WHERE id=?", versions[0], entry); err != nil {
				t.Fatal(err)
			}
			if _, err := p.db.Exec("UPDATE nodes SET tunnel_authorization_version=? WHERE id=?", versions[1], exit); err != nil {
				t.Fatal(err)
			}
			if err := p.commitForwarding(context.Background(), func(*sql.Tx) error { return nil }); err != nil {
				t.Fatal(err)
			}
			compiledEntry, _ := p.controlMessage(context.Background(), entry)
			compiledExit, _ := p.controlMessage(context.Background(), exit)
			if versions == [2]int{1, 1} {
				if len(compiledEntry.Snapshot.Rules) != 0 || len(compiledExit.Snapshot.TunnelServer.Grants) != 1 || compiledExit.Snapshot.TunnelServer.Grants[0].Rule.AuthorizationGeneration == 0 {
					t.Fatal("双端升级后未先准备新授权再门禁入口")
				}
				session := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "upgraded"}
				p.controls[exit] = session
				s := compiledExit.Snapshot
				if _, err := p.acceptReport(context.Background(), exit, tokenDigest, session, &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "upgraded", AttemptedRevision: s.Revision, AppliedRevision: s.Revision, AttemptedDigest: s.Digest, AppliedDigest: s.Digest, RevocationsApplied: true}); err != nil {
					t.Fatal(err)
				}
				compiledEntry, _ = p.controlMessage(context.Background(), entry)
				if len(compiledEntry.Snapshot.Rules) != 1 || compiledEntry.Snapshot.Rules[0].AuthorizationGeneration == 0 {
					t.Fatal("新授权确认后未升级入口代次")
				}
				return
			}
			if len(compiledEntry.Snapshot.Rules) != 1 || len(compiledExit.Snapshot.TunnelServer.Grants) != 1 {
				t.Fatal("重编译撤销了既有旧版隧道")
			}
			if compiledEntry.Snapshot.Rules[0].AuthorizationGeneration != 0 || compiledExit.Snapshot.TunnelServer.Grants[0].Rule.AuthorizationGeneration != 0 {
				t.Fatal("向不支持能力的对端提前启用新代次")
			}
			// 修改目标使旧授权失效，随后改回也不能从旧快照复活授权。
			for _, port := range []int{444, 443} {
				if err := p.commitForwarding(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE forward_rules SET target_port=?", port)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				compiledEntry, _ = p.controlMessage(context.Background(), entry)
				compiledExit, _ = p.controlMessage(context.Background(), exit)
				if len(compiledEntry.Snapshot.Rules) != 0 || len(compiledExit.Snapshot.TunnelServer.Grants) != 0 {
					t.Fatal("变化授权或已撤销的零代次授权绕过能力门禁")
				}
			}
		})
	}
}
