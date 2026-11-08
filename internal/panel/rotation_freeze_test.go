package panel

import (
	"context"
	"fmt"
	"testing"

	"nbpass/internal/controlpb"
)

// 旧代次进度在重连首报告后可能先于管理命令到达，必须忽略而非断开控制流。
func TestRotationOldProgressCannotInterruptPendingControl(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);
INSERT INTO rotation_actions(id,node_id,old_ip,state,reason,created_at,expires_at,updated_at,secret_name,attempts,sequence,observation_complete,control_sequence,pending_operation)
VALUES('a','n','8.8.8.8','unconfirmed','仍未确认',0,1,0,'secret',5,7,1,1,'release')`)
	if err != nil {
		t.Fatal(err)
	}
	old := &controlpb.RotationProgress{ActionId: "a", OldIp: "8.8.8.8", State: "unconfirmed", Reason: "unconfirmed", Sequence: 8, Attempts: 5, ObservationComplete: true}
	if err := p.acceptRotation(ctx, "n", old); err != nil {
		t.Fatalf("旧进度中断管理命令: %v", err)
	}
	a, err := p.latestRotation(ctx, "n")
	if err != nil || a.PendingOperation != "release" || a.Sequence != 7 {
		t.Fatalf("旧进度消费了管理命令: %v %v", a, err)
	}
	command, err := p.rotationCommand(ctx, "n")
	if err != nil || command.Operation != "release" || command.ControlSequence != 1 || len(command.ConfigJson) != 0 {
		t.Fatalf("管理命令丢失/携带商家凭据: %v %v", command, err)
	}
	// 未授权的新状态也不能释放冻结。
	old.ControlSequence = 1
	old.State = "observing"
	old.Reason = "observing"
	if err := p.acceptRotation(ctx, "n", old); err == nil {
		t.Fatal("非解除结果消费了解除授权")
	}
}

func TestRotationFreezeMigrationPreservesVersionThirteenActions(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, migration := range migrations[:13] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','旧节点',0);
INSERT INTO rotation_settings(node_id,secret_name) VALUES('n','secret');
INSERT INTO rotation_actions(id,node_id,old_ip,state,reason,sequence,attempts,created_at,expires_at,updated_at,secret_name)
 VALUES('a','n','8.8.8.8','unconfirmed','结果未确认',7,5,0,1,0,'secret')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	a, err := p.latestRotation(context.Background(), "n")
	if err != nil || a == nil || a.ID != "a" || a.State != "unconfirmed" || a.Sequence != 7 || a.Attempts != 5 || !a.ObservationComplete || a.ControlSequence != 0 || a.PendingOperation != "" {
		t.Fatalf("升级丢失原动作/冻结: %v %v", a, err)
	}
	// 解除后的原动作仍保留；只释放节点在途唯一约束。
	if _, err := p.db.Exec(`UPDATE rotation_actions SET state='released' WHERE id='a';
INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name)
 VALUES('b','n','8.8.8.8','authorized',2,3,2,'secret')`); err != nil {
		t.Fatalf("解除后仍不能创建新动作: %v", err)
	}
	if _, err := p.db.Exec(`INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name)
 VALUES('c','n','8.8.8.8','authorized',4,5,4,'secret')`); err == nil {
		t.Fatal("迁移失去节点级互斥")
	}
}
