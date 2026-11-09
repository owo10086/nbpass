package panel

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/rotation"
)

// 物理边界：完整模板的明文只出现在解密后、且仅属于执行节点的命令中。
func TestRotationSecretCiphertextAndCommandScope(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	var ids []string
	for _, name := range []string{"执行节点", "其他节点"} {
		response := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": name}, cookie, 201)
		var row map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &row)
		ids = append(ids, row["id"].(string))
	}
	path := "/api/nodes/" + ids[0] + "/ip-rotation"
	const secret = "rotation-plaintext-secret-marker-28"
	config := rotation.Config{URL: "https://vendor.example/change?key=" + secret, Method: "POST", Headers: map[string]string{"Authorization": "Bearer " + secret}, Body: `{"key":"` + secret + `"}`}
	diskRequest(t, p, "PUT", path, config, cookie, 200)
	p.controls[ids[0]] = &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1)}
	if _, err := p.db.Exec("UPDATE nodes SET last_seen_at=? WHERE id=?", p.deps.Clock.Now().UnixNano(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := p.recordNodeIP(context.Background(), ids[0], "8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	diskRequest(t, p, "POST", path+"/actions", nil, cookie, 202)
	response := diskRequest(t, p, "GET", path, nil, cookie, 200)
	if strings.Contains(response.Body.String(), secret) {
		t.Fatal("API回显凭据")
	}
	command, err := p.rotationCommand(context.Background(), ids[0])
	if err != nil || command == nil {
		t.Fatal("缺少执行节点命令", err)
	}
	if !strings.Contains(string(command.ConfigJson), secret) || command.NodeId != ids[0] || command.ExpiresAt-command.IssuedAt != int64(2*time.Minute) {
		t.Fatal("命令未绑定节点/期限/秘密")
	}
	if len(command.RecheckConfig) != 0 {
		t.Fatal("旧节点未声明能力，不应收到强制复检命令")
	}
	other, err := p.rotationCommand(context.Background(), ids[1])
	if err != nil || other != nil {
		t.Fatal("其他节点收到凭据")
	}
	var ciphertext []byte
	if err := p.db.QueryRow("SELECT ciphertext FROM secrets WHERE name LIKE 'rotation.%'").Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	forbidden := map[string][]byte{"rotation secret": []byte(secret)}
	wal := assertNoPlaintext(t, filepath.Join(dir, "panel.db-wal"), forbidden)
	assertStoredCredentialsPresent(t, wal, ciphertext)
	assertNoPlaintext(t, filepath.Join(dir, "panel.db"), forbidden)
}

// 混入其他字段的动作进度不能推进活性、配置、探针或事件补报。
func TestRotationProgressIsIndependentOfOtherReports(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now()
	_, digest, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.db.Exec(`INSERT INTO nodes(id,name,token_hash,created_at,last_seen_at,desired_revision,applied_revision,applied_digest,process_instance) VALUES('n','节点',?,?,?,5,4,'previous','instance')`, digest, now.UnixNano(), now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.db.Exec(`INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('a','n','8.8.8.8','authorized',?,?,?,'s')`, now.UnixNano(), now.Add(2*time.Minute).UnixNano(), now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	session := &controlSession{instance: "instance", done: make(chan struct{})}
	p.controls["n"] = session
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "instance", RotationOnly: true, Rotation: &controlpb.RotationProgress{ActionId: "a", OldIp: "8.8.8.8", State: "observing", Reason: "observing", Sequence: 1}, PublicIpv4: "1.1.1.1", AppliedRevision: 5, Probe: &controlpb.ProbeSample{Sequence: 1, Values: map[string]float64{"cpu_percent": 50}}, Events: []*controlpb.Event{{EventId: "malicious"}}}
	if _, err := p.acceptReport(ctx, "n", digest, session, report); err != nil {
		t.Fatal(err)
	}
	var last, applied int64
	var ip string
	if err := p.db.QueryRow("SELECT last_seen_at,applied_revision FROM nodes WHERE id='n'").Scan(&last, &applied); err != nil {
		t.Fatal(err)
	}
	if last != now.UnixNano() || applied != 4 {
		t.Fatal("动作报告推进活性或配置")
	}
	var count int
	_ = p.db.QueryRow("SELECT count(*) FROM node_regions WHERE node_id='n'").Scan(&count)
	if count != 0 {
		t.Fatal("动作报告消费无关IP")
	}
	_ = p.db.QueryRow("SELECT count(*) FROM probe_realtime WHERE node_id='n'").Scan(&count)
	if count != 0 {
		t.Fatal("动作报告消费探针")
	}
	_ = p.db.QueryRow("SELECT count(*) FROM events WHERE event_id='malicious'").Scan(&count)
	if count != 0 {
		t.Fatal("动作报告消费事件")
	}
	report.RotationOnly = false
	report.Events = nil
	report.Probe = nil
	report.AppliedRevision = 0
	report.PublicIpv4 = ""
	report.Rotation.State = "changed"
	report.Rotation.NewIp = "1.1.1.1"
	report.Rotation.Sequence = 2
	report.Rotation.Reason = "changed"
	if _, err := p.acceptReport(ctx, "n", digest, session, report); err != nil {
		t.Fatal(err)
	}
	_ = p.db.QueryRow("SELECT state FROM rotation_actions WHERE id='a'").Scan(&ip)
	if ip != "observing" {
		t.Fatal("活性报告消费动作")
	}
}
