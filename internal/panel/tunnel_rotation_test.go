package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

func rotationReport(s *controlpb.ConfigSnapshot) *controlpb.NodeReport {
	return &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: s.Revision, AppliedRevision: s.Revision, AttemptedDigest: s.Digest, AppliedDigest: s.Digest, RevocationsApplied: true}
}

// 报告隔离属于协议物理属性；管理操作仍走 HTTP，不从数据库制造轮换阶段。
func TestTunnelRotationConfirmationIsolation(t *testing.T) {
	p, entry, exit, exitToken, initial := tunnelAuthorizationProtocolFixture(t)
	ctx := context.Background()
	_, entryToken, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET token_hash=? WHERE id=?", entryToken, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_rotation_version=1"); err != nil {
		t.Fatal(err)
	}
	exitSession := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "current"}
	entrySession := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "current"}
	p.controls[exit], p.controls[entry] = exitSession, entrySession
	if _, err := p.acceptReport(ctx, exit, exitToken, exitSession, rotationReport(initial)); err != nil {
		t.Fatal(err)
	}
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	path := "/api/nodes/" + exit + "/tunnel/rotation"
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both"}, cookie, 202)
	assertPhase := func(want string) {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal(diskRequest(t, p, "GET", path, nil, cookie, 200).Body.Bytes(), &v); err != nil || v["phase"] != want {
			t.Fatalf("轮换阶段错误: %v %v", v, err)
		}
	}
	for _, step := range []struct {
		id          string
		token       []byte
		session     *controlSession
		phase, next string
	}{{exit, exitToken, exitSession, "preparing", "distributing"}, {entry, entryToken, entrySession, "distributing", "activating"}, {exit, exitToken, exitSession, "activating", "cleaning"}, {entry, entryToken, entrySession, "cleaning", "complete"}} {
		m, err := p.controlMessage(ctx, step.id)
		if err != nil {
			t.Fatal(err)
		}
		report := rotationReport(m.Snapshot)
		assertPhase(step.phase)

		if step.phase == "activating" {
			// 撤旧快照已经发出，不能再加入尚未确认的入口。
			request := func(method, path string, body any, code int) map[string]any {
				t.Helper()
				v := map[string]any{}
				if err := json.Unmarshal(diskRequest(t, p, method, path, body, cookie, code).Body.Bytes(), &v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			e := request("POST", "/api/nodes", map[string]string{"name": "新增入口"}, 201)["id"].(string)
			if _, err := p.db.Exec("UPDATE nodes SET tunnel_rotation_version=1,tunnel_authorization_version=1 WHERE id=?", e); err != nil {
				t.Fatal(err)
			}
			g := request("POST", "/api/entry-groups", map[string]any{"name": "新组", "members": []string{e}}, 201)["id"].(string)
			var exitGroup string
			if err := p.db.QueryRow("SELECT id FROM exit_groups LIMIT 1").Scan(&exitGroup); err != nil {
				t.Fatal(err)
			}
			request("POST", "/api/rules", map[string]any{"name": "新增关联", "entry_group_id": g, "exit_group_id": exitGroup, "kind": "tunnel", "listen_port": 23003, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 409)
		}
		for _, kind := range []string{"old", "future", "attempted_digest", "applied_digest", "heartbeat", "probe", "events", "self_check", "dns", "traffic", "ip_rotation", "checker", "failed", "not_durable", "session", "process", "unsupported"} {
			t.Run(step.phase+"/"+kind, func(t *testing.T) {
				r := proto.Clone(report).(*controlpb.NodeReport)
				active := step.session
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
				case "heartbeat":
					r.AttemptedRevision = 0
					r.AttemptedDigest = ""
				case "probe":
					r.ProbeOnly = true
				case "events":
					r.EventsOnly = true
				case "self_check":
					r.SelfCheckOnly = true
				case "dns":
					r.DnsWatchOnly = true
				case "traffic":
					r.TrafficOnly = true
				case "ip_rotation":
					r.RotationOnly = true
				case "checker":
					r.CheckerOnly = true
				case "failed":
					r.ApplyError = "写盘失败"
				case "not_durable":
					r.RevocationsApplied = false
				case "session":
					active = &controlSession{instance: "current"}
				case "process":
					r.ProcessInstance = "retired"
				case "unsupported":
					if _, err := p.db.Exec("UPDATE nodes SET tunnel_rotation_version=0 WHERE id=?", step.id); err != nil {
						t.Fatal(err)
					}
				}
				_, err := p.acceptReport(ctx, step.id, step.token, active, r)
				if (kind == "session" || kind == "process" || kind == "ip_rotation") != (err != nil) {
					t.Fatalf("报告处理: %v", err)
				}
				assertPhase(step.phase)
			})
		}
		if _, err := p.db.Exec("UPDATE nodes SET tunnel_rotation_version=1 WHERE id=?", step.id); err != nil {
			t.Fatal(err)
		}
		if _, err := p.acceptReport(ctx, step.id, step.token, step.session, report); err != nil {
			t.Fatal(err)
		}
		assertPhase(step.next)
	}
	// 重复的最后确认不能制造版本。
	m, _ := p.controlMessage(ctx, exit)
	if _, err := p.acceptReport(ctx, exit, exitToken, exitSession, rotationReport(m.Snapshot)); err != nil {
		t.Fatal(err)
	}
	n, _ := p.controlMessage(ctx, exit)
	if n.DesiredRevision != m.DesiredRevision {
		t.Fatal("重复确认制造新版本")
	}
}

func TestTunnelRotationAPIValidationAndEncryptedMaterials(t *testing.T) {
	p, entry, exit, exitToken, _ := tunnelAuthorizationProtocolFixture(t)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	path := "/api/nodes/" + exit + "/tunnel/rotation"
	diskRequest(t, p, "GET", path, nil, nil, 401)
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both"}, nil, 401)
	diskRequest(t, p, "POST", path, map[string]string{"kind": "wrong"}, cookie, 400)
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both", "extra": "no"}, cookie, 400)
	diskRequest(t, p, "POST", "/api/nodes/missing/tunnel/rotation", map[string]string{"kind": "both"}, cookie, 404)
	diskRequest(t, p, "GET", "/api/nodes/missing/tunnel/rotation", nil, cookie, 404)
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both"}, cookie, 409) // 旧节点未声明能力。
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_rotation_version=1 WHERE id=?", exit); err != nil {
		t.Fatal(err)
	}
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both"}, cookie, 409) // 入口也必须升级。
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_rotation_version=1 WHERE id=?", entry); err != nil {
		t.Fatal(err)
	}
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both"}, cookie, 202)
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both"}, cookie, 409)
	var id string
	var sealedKey, sealedToken []byte
	if err := p.db.QueryRow("SELECT id,private_key_ciphertext FROM tunnel_rotations WHERE exit_id=?", exit).Scan(&id, &sealedKey); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT credential_ciphertext FROM tunnel_rotation_pairs WHERE exit_id=? AND entry_id=?", exit, entry).Scan(&sealedToken); err != nil {
		t.Fatal(err)
	}
	secrets := p.secrets.(*sealedSecrets)
	key, err := secrets.open("tunnel-rotation-key:"+exit+":"+id, sealedKey)
	if err != nil {
		t.Fatal(err)
	}
	token, err := secrets.open("tunnel-rotation-pair:"+id+":"+entry, sealedToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.open("tunnel-rotation-key:"+entry+":"+id, sealedKey); err == nil {
		t.Fatal("轮换私钥未绑定出口")
	}
	if _, err := secrets.open("tunnel-rotation-pair:"+id+":"+exit, sealedToken); err == nil {
		t.Fatal("轮换凭据未绑定节点对")
	}
	raw := diskRequest(t, p, "GET", path, nil, cookie, 200).Body.Bytes()
	if bytes.Contains(raw, key) || bytes.Contains(raw, token) {
		t.Fatal("轮换状态泄露秘密")
	}
	block, _ := pem.Decode(key)
	if block == nil {
		t.Fatal("轮换私钥无效")
	}
	forbidden := map[string][]byte{"key": key, "key_der": block.Bytes, "token": token}
	// 从 SQLite 返回真实路径，检查数据库及 WAL 的物理字节。
	rows, err := p.db.Query("PRAGMA database_list")
	if err != nil {
		t.Fatal(err)
	}
	var dbPath string
	for rows.Next() {
		var seq int
		var name, path string
		if err := rows.Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		if name == "main" {
			dbPath = path
		}
	}
	rows.Close()
	assertNoPlaintext(t, dbPath, forbidden)
	assertNoPlaintext(t, dbPath+"-wal", forbidden)
	if _, err := p.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, dbPath, forbidden)
	// 进行中的新能力不能下发给声明不支持的替代进程。
	incompatible := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1)}
	err = p.admitControl(context.Background(), exit, exitToken, incompatible, &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "unsupported", TunnelAuthorizationVersion: 1})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("轮换中接受不支持轮换的节点进程: %v", err)
	}
}

func TestTunnelRotationMigrationPreservesPreviousMain(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, migration := range migrations {
		if migration == tunnelRotationMigration {
			break
		}
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','出口',0);
 INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext) VALUES('n','127.0.0.1',22443,'wss','exit.test',x'0102',x'0304');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	var version, capability, port int
	var cert, key []byte
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT n.tunnel_rotation_version,e.listen_port,e.certificate_pem,e.private_key_ciphertext FROM tunnel_exits e JOIN nodes n ON n.id=e.node_id").Scan(&capability, &port, &cert, &key); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) || capability != 0 || port != 22443 || !bytes.Equal(cert, []byte{1, 2}) || !bytes.Equal(key, []byte{3, 4}) {
		t.Fatal("现有 main 升级破坏既有出口配置")
	}
}

func TestTunnelRotationRemovedEntryDoesNotBlockTrustCleanup(t *testing.T) {
	p, entry, exit, exitToken, initial := tunnelAuthorizationProtocolFixture(t)
	ctx := context.Background()
	_, entryToken, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET token_hash=? WHERE id=?", entryToken, entry); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_rotation_version=1"); err != nil {
		t.Fatal(err)
	}
	xs := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "current"}
	es := &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), instance: "current"}
	p.controls[exit], p.controls[entry] = xs, es
	if _, err := p.acceptReport(ctx, exit, exitToken, xs, rotationReport(initial)); err != nil {
		t.Fatal(err)
	}
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	path := "/api/nodes/" + exit + "/tunnel/rotation"
	diskRequest(t, p, "POST", path, map[string]string{"kind": "both"}, cookie, 202)
	ack := func(id string, token []byte, session *controlSession) {
		t.Helper()
		m, err := p.controlMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.acceptReport(ctx, id, token, session, rotationReport(m.Snapshot)); err != nil {
			t.Fatal(err)
		}
	}
	ack(exit, exitToken, xs)
	ack(entry, entryToken, es)
	ack(exit, exitToken, xs)
	var v map[string]any
	if err := json.Unmarshal(diskRequest(t, p, "GET", path, nil, cookie, 200).Body.Bytes(), &v); err != nil || v["phase"] != "cleaning" {
		t.Fatalf("未进入旧信任清理: %v %v", v, err)
	}
	m, err := p.controlMessage(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	diskRequest(t, p, "DELETE", "/api/rules/"+m.Snapshot.Rules[0].Id, nil, cookie, 204)
	ack(exit, exitToken, xs)
	if err := json.Unmarshal(diskRequest(t, p, "GET", path, nil, cookie, 200).Body.Bytes(), &v); err != nil || v["phase"] != "complete" || v["pending"] != float64(0) {
		t.Fatalf("已解除关联入口永久阻塞清理: %v %v", v, err)
	}
}
