package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"nbpass/internal/controlpb"
	"testing"
	"time"
)

func TestRecoveryPayloadCannotRideIndependentReports(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	digest := sha256.Sum256([]byte("restore-token"))
	if _, err := p.db.Exec(`INSERT INTO nodes(id,name,token_hash,created_at,process_instance) VALUES('n','节点',?,0,'instance');INSERT INTO recovery_state VALUES(1,1);INSERT INTO recovery_nodes(node_id) VALUES('n');`, digest[:]); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), instance: "instance"}
	p.controls["n"] = session
	for _, kind := range []string{"probe", "events", "self", "checker", "dns", "traffic", "rotation", "config"} {
		report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "instance", Recovery: &controlpb.RecoveryReport{HighWater: 999}, ProbeOnly: kind == "probe", EventsOnly: kind == "events", SelfCheckOnly: kind == "self", CheckerOnly: kind == "checker", DnsWatchOnly: kind == "dns", TrafficOnly: kind == "traffic", RotationOnly: kind == "rotation"}
		if kind == "rotation" {
			report.Rotation = &controlpb.RotationProgress{}
		}
		if kind == "config" {
			report.AttemptedRevision = 1
		}
		if _, err := p.acceptReport(ctx, "n", digest[:], session, report); err != nil {
			t.Fatal(kind, err)
		}
		var high sql.NullInt64
		if err := p.db.QueryRow("SELECT high_water FROM recovery_nodes WHERE node_id='n'").Scan(&high); err != nil || high.Valid {
			t.Fatal("独立上报推进恢复状态", kind, high, err)
		}
	}
}

func TestRecoveryCoverageDoesNotBridgeMissingHistory(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now().UnixNano()
	if _, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);INSERT INTO recovery_state VALUES(1,?);INSERT INTO recovery_nodes(node_id,high_water,verified) VALUES('n',1,1);`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE recovery_nodes SET history_since=?,history_until=? WHERE node_id='n'", now-int64(time.Hour), now-int64(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := p.acceptRecovery(ctx, "n", &controlpb.RecoveryReport{HighWater: 1, HistorySince: now - int64(time.Minute), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	var since int64
	p.db.QueryRow("SELECT history_since FROM recovery_nodes WHERE node_id='n'").Scan(&since)
	if since != now-int64(time.Minute) {
		t.Fatal("跨缺口伪造了完整历史", since)
	}
}

func TestRecoveryClockSkewDoesNotBreakOrdinaryHeartbeat(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	if _, err := p.db.Exec("INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0)"); err != nil {
		t.Fatal(err)
	}
	// 尚未恢复的数据库不应消费恢复载荷；节点系统时间小幅领先也不应断控制流。
	if err := p.acceptRecovery(ctx, "n", &controlpb.RecoveryReport{HistorySince: p.deps.Clock.Now().Add(time.Minute).UnixNano()}); err != nil {
		t.Fatal("普通心跳被恢复信息拒绝", err)
	}
	if _, err := p.db.Exec("INSERT INTO recovery_state VALUES(1,1);INSERT INTO recovery_nodes(node_id) VALUES('n')"); err != nil {
		t.Fatal(err)
	}
	if err := p.acceptRecovery(ctx, "n", &controlpb.RecoveryReport{HistorySince: p.deps.Clock.Now().Add(time.Second).UnixNano(), ObservedAt: p.deps.Clock.Now().Add(2 * time.Second).UnixNano()}); err != nil {
		t.Fatal("小幅时差导致恢复无法连接", err)
	}
}

// 接收延迟不能扩张节点已经证明的历史区间。
func TestRecoveryCoverageEndsAtNodeSampleNotReceipt(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now().UnixNano()
	if _, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);INSERT INTO recovery_state VALUES(1,?);INSERT INTO recovery_nodes(node_id) VALUES('n');`, now); err != nil {
		t.Fatal(err)
	}
	if err := p.acceptRecovery(ctx, "n", &controlpb.RecoveryReport{HistorySince: now - int64(3*time.Minute), ObservedAt: now - int64(2*time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := p.acceptRecovery(ctx, "n", &controlpb.RecoveryReport{HighWater: 1, HistorySince: now - int64(time.Minute), ObservedAt: now}); err != nil {
		t.Fatal(err)
	}
	var since int64
	p.db.QueryRow("SELECT history_since FROM recovery_nodes WHERE node_id='n'").Scan(&since)
	if since != now-int64(time.Minute) {
		t.Fatal("接收延迟跨过了日志缺口", since)
	}
}

// 节点已进入后续动作时，旧快照的待办不能撞唯一约束、阻止高水位和动作核对。
func TestRecoveryCurrentActionSupersedesStalePendingConstraint(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now().UnixNano()
	if _, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);INSERT INTO recovery_state VALUES(1,?);INSERT INTO recovery_nodes(node_id) VALUES('n');`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('old','n','8.8.8.8','unconfirmed',?,0,?,'')`, now-int64(25*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	record := &controlpb.RecoveryRotation{ActionId: "current", AttemptedAt: now - int64(time.Minute), State: "unconfirmed", Progress: &controlpb.RotationProgress{ActionId: "current", OldIp: "8.8.8.8", State: "unconfirmed", Reason: "unconfirmed", Sequence: 7, Attempts: 5, ObservationComplete: true}}
	if err := p.acceptRecovery(ctx, "n", &controlpb.RecoveryReport{ObservedAt: now, Current: record}); err != nil {
		t.Fatal("旧待办阻止当前动作恢复", err)
	}
	var state string
	if err := p.db.QueryRow("SELECT state FROM rotation_actions WHERE id='old'").Scan(&state); err != nil || state != "released" {
		t.Fatal("旧待办未封存", state, err)
	}
	var complete bool
	if err := p.db.QueryRow("SELECT observation_complete FROM rotation_actions WHERE id='current'").Scan(&complete); err != nil || !complete {
		t.Fatal("当前动作不能管理", complete, err)
	}
}

func TestRecoveryNewNodeStillHonorsPausedRelatedGroup(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	if _, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('new','新节点',0);INSERT INTO recovery_state VALUES(1,1);INSERT INTO entry_groups(id,name) VALUES('g','待核对入口组');INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','new',0);INSERT INTO recovery_groups(group_id) VALUES('g');`); err != nil {
		t.Fatal(err)
	}
	if allowed, err := p.recoveryNodeAllowed(ctx, p.db, "new", true); err != nil || allowed {
		t.Fatal("新节点绕过关联入口组暂停", allowed, err)
	}
}
