package panel

import (
	"context"
	"encoding/json"
	"nbpass/internal/controlpb"
	"nbpass/internal/selfcheck"
	"testing"
	"time"
)

// 物理锁边界：入口 API 持 entryMu 等 controlMu 时，控制进度不能反向等待 entryMu。
func TestRotationProgressDoesNotWaitForEntryMutationLock(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now()
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',?);INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('a','n','8.8.8.8','authorized',?,?,?,'s')`, now.UnixNano(), now.UnixNano(), now.Add(time.Minute).UnixNano(), now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	p.entryMu.Lock()
	done := make(chan error, 1)
	go func() {
		p.controlMu.Lock()
		defer p.controlMu.Unlock()
		done <- p.acceptRotation(ctx, "n", &controlpb.RotationProgress{ActionId: "a", OldIp: "8.8.8.8", Sequence: 1, State: "observing", Reason: "observing"})
	}()
	select {
	case err := <-done:
		p.entryMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		p.entryMu.Unlock()
		<-done
		t.Fatal("控制进度反向等待入口锁，会与入口配置 API 死锁")
	}
}

// 物理时序边界：跨控制流补报顺序不能把更早的正常证据当作暂停之后的恢复。
func TestRotationPauseIgnoresOlderNormalEvidence(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now()
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);INSERT INTO node_self_check(node_id,enabled) VALUES('n',1);INSERT INTO rotation_policy(node_id,no_improvement,paused) VALUES('n',2,1)`)
	if err != nil {
		t.Fatal(err)
	}
	// 完成时间取已持久化复检证据，不能用收到时间衡量离线历史。
	r := selfcheck.Round{FinishedAt: now, StartedAt: now, Result: "abnormal"}
	data, _ := json.Marshal([]selfcheck.Round{r, r, r})
	_, err = p.db.Exec(`INSERT INTO rotation_actions(id,node_id,old_ip,new_ip,state,created_at,expires_at,updated_at,secret_name,recheck_status,recheck_rounds) VALUES('a','n','8.8.8.8','1.1.1.1','changed',?,?,?,'s','no_improvement',?)`, now.Add(-time.Hour).UnixNano(), now.UnixNano(), now.UnixNano(), data)
	if err != nil {
		t.Fatal(err)
	}
	wire := selfCheckWire(t, 1, now.Add(-time.Minute))
	var round selfcheck.Round
	_ = json.Unmarshal(wire, &round)
	round.Result = "normal"
	for i := range round.Sites {
		round.Sites[i].Result = "normal"
		round.Sites[i].Connections[0].Connected = true
		round.Sites[i].Connections[0].Error = ""
	}
	wire, _ = json.Marshal(round)
	if _, err = p.acceptSelfChecks(ctx, "n", [][]byte{wire}); err != nil {
		t.Fatal(err)
	}
	var paused bool
	var count int
	if err = p.db.QueryRow("SELECT paused,no_improvement FROM rotation_policy WHERE node_id='n'").Scan(&paused, &count); err != nil {
		t.Fatal(err)
	}
	if !paused || count != 2 {
		t.Fatal("较早正常轮错误清掉了较晚的暂停")
	}
}

func TestRotationRecheckCapabilityCannotDisappearDuringDelivery(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now()
	config, _ := json.Marshal(selfcheck.Config{Enabled: true, Settings: selfcheck.Defaults()})
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);INSERT INTO rotation_settings(node_id,secret_name,automatic_enabled) VALUES('n','template',1);INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name,origin,recheck_config,recheck_revision) VALUES('a','n','8.8.8.8','authorized',?,?,?,'template','automatic',?,1);INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES('a','n',?)`, now.UnixNano(), now.Add(time.Minute).UnixNano(), now.UnixNano(), config, now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.secrets.Put(ctx, "template", []byte(`{"url":"https://vendor.example/change","method":"POST"}`)); err != nil {
		t.Fatal(err)
	}
	p.controls["n"] = &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1)}
	command, err := p.rotationCommand(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	if command != nil {
		t.Fatal("能力消失后仍向旧节点投递自动许可")
	}
	_, err = p.db.Exec("UPDATE rotation_actions SET state='authorized' WHERE id='a';INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES('a','n',?)", now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	p.controls["n"].rotationRecheckCapable = true
	if command, err = p.rotationCommand(ctx, "n"); err != nil || command == nil {
		t.Fatal("新版节点缺少许可", err)
	}
	p.controls["n"].rotationRecheckCapable = false
	if command, err = p.rotationCommand(ctx, "n"); err != nil || command != nil {
		t.Fatal("回滚节点仍收到许可", err)
	}
	a, err := p.latestRotation(ctx, "n")
	if err != nil || a.State != "authorized" {
		t.Fatal("曾经投递的许可不能假定未调用，须保留原动作等待升级核对", a, err)
	}
	q, err := p.rotationQuota(ctx, "n")
	if err != nil || q.UsedDay != 1 {
		t.Fatal("可能已执行的动作额度必须保留", q, err)
	}

}

func TestRotationManualResumeIgnoresEarlierFailure(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now()
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);INSERT INTO rotation_policy(node_id,no_improvement,paused) VALUES('n',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	response := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := response.Result().Cookies()[0]
	diskRequest(t, p, "POST", "/api/nodes/n/ip-rotation/resume", map[string]bool{"confirm": true}, cookie, 200)
	data, _ := json.Marshal([]selfcheck.Round{{Result: "abnormal", FinishedAt: now.Add(-time.Minute)}})
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = p.finishRotationRecheck(ctx, tx, &rotationAction{ID: "earlier", NodeID: "n"}, "no_improvement", data); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = p.db.QueryRow("SELECT no_improvement FROM rotation_policy WHERE node_id='n'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("解除前已完成的迟到失败重新累计")
	}
}
