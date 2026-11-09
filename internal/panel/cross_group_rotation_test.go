package panel

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCrossGroupRotationMigrationPreservesVersionThirtyFour(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for v, m := range migrations[:34] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);
 INSERT INTO entry_groups(id,name,blocked_policy,auto_failback_paused) VALUES('g','原组','switch_then_rotate',1);
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0);
 INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,written_at) VALUES('a','g',7,'switch','n','9.9.9.9','8.8.8.8',60,'服务商已写入',1,2);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id,written_at) VALUES('g',7,'n','8.8.8.8','a',2);
 INSERT INTO entry_blocked_decisions(id,group_id,node_id,incident_id,desired_version,outcome,created_at,policy) VALUES('d','g','n','i',6,'submitted',1,'switch_then_rotate');
 INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('r','n','8.8.8.8','unconfirmed',3,4,5,'secret');
 INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES('r','n',3);
 INSERT INTO rotation_policy(node_id,no_improvement,paused) VALUES('n',2,1);
 INSERT INTO entry_blocked_rotations(decision_id,expected_version,rotation_id) VALUES('d',7,'r');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var version, desired, written, paused, attempts, noImprovement, linkedVersion, cross, notified int
	var linked, state string
	err = p.db.QueryRow(`SELECT s.desired_version,s.written_at,g.auto_failback_paused,
 (SELECT COUNT(*) FROM rotation_quota),r.no_improvement,f.expected_version,f.rotation_id,a.state,d.cross_rotation,d.cross_notified
 FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id JOIN entry_blocked_decisions d ON d.group_id=g.id
 JOIN entry_blocked_rotations f ON f.decision_id=d.id JOIN rotation_actions a ON a.id=f.rotation_id JOIN rotation_policy r ON r.node_id=a.node_id`).Scan(&desired, &written, &paused, &attempts, &noImprovement, &linkedVersion, &linked, &state, &cross, &notified)
	if err != nil || desired != 7 || written != 2 || paused != 1 || attempts != 1 || noImprovement != 2 || linkedVersion != 7 || linked != "r" || state != "unconfirmed" || cross != 0 || notified != 0 {
		t.Fatal("迁移改变了原工作流、发布、额度或冻结", err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal(version, err)
	}
}

// 物理投递边界：授权落盘后、首次投递前的手动操作不能被旧许可越过。
func TestCrossGroupRotationUnsentPermitRejectsNewEntryVersion(t *testing.T) {
	for _, change := range []string{"entry_version", "automatic_disabled"} {
		t.Run(change, func(t *testing.T) { crossGroupUnsentPermit(t, change) })
	}
}

func crossGroupUnsentPermit(t *testing.T, change string) {
	t.Helper()
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now()
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at,last_seen_at) VALUES('n','节点',0,?),('other','备用',0,?);
 INSERT INTO node_regions(node_id,public_ipv4) VALUES('n','8.8.8.8');
 INSERT INTO node_self_check(node_id,enabled,state) VALUES('n',1,'blocked');
 INSERT INTO rotation_settings(node_id,secret_name,automatic_enabled) VALUES('n','template',1);
 INSERT INTO entry_groups(id,name,blocked_policy) VALUES('a','组 A','rotate_ip'),('b','组 B','switch_entry');
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('a','n',0),('a','other',1),('b','n',0),('b','other',1);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id,written_at)
 VALUES('a',1,'other','1.1.1.1','aa',1),('b',1,'other','1.1.1.1','ba',1);`, now.UnixNano(), now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.secrets.Put(ctx, "template", []byte(`{"url":"https://vendor.example/change","method":"POST"}`)); err != nil {
		t.Fatal(err)
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	p.controls["n"] = &controlSession{done: make(chan struct{}), changed: make(chan struct{}, 1), rotationRecheckCapable: true}
	a, err := p.authorizeRotation(ctx, "n", false)
	if err != nil {
		t.Fatal(err)
	}
	// 新期望即使已写入，也不是原许可核对过的版本；单独验证版本条件。
	mutation := "UPDATE entry_publications SET desired_version=2,active_node_id='n',expected_value='8.8.8.8',written_at=2 WHERE group_id='b'"
	if change == "automatic_disabled" {
		mutation = "UPDATE rotation_settings SET automatic_enabled=0 WHERE node_id='n'"
	}
	if _, err = p.db.Exec(mutation); err != nil {
		t.Fatal(err)
	}
	command, err := p.rotationCommand(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	if command != nil {
		t.Fatal("入口版本变化后仍投递旧的自动换 IP 许可")
	}
	latest, err := p.latestRotation(ctx, "n")
	if err != nil || latest.ID != a.ID || latest.State != "cancelled" {
		t.Fatalf("未投递许可未安全取消: %v %v", latest, err)
	}
	q, err := p.rotationQuota(ctx, "n")
	if err != nil || q.UsedDay != 0 {
		t.Fatal("未投递许可仍占额度", q, err)
	}
	// 不伪造外部执行；原许可仍处于期限内，拒绝原因必须是屏障变化。
	if !a.ExpiresAt.After(now.Add(time.Second)) {
		t.Fatal("测试未覆盖有效许可")
	}
	// 若之前可能已投递，以上变化都不能被误当成商家尚未执行，须保留原动作及额度。
	if _, err = p.db.Exec(`UPDATE rotation_actions SET state='authorized',permit_sent=1 WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec(`INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES(?,'n',?)`, a.ID, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	command, err = p.rotationCommand(ctx, "n")
	if err != nil || command == nil || command.ActionId != a.ID {
		t.Fatal("已可能投递的动作未保留续查", command, err)
	}
	q, err = p.rotationQuota(ctx, "n")
	if err != nil || q.UsedDay != 1 {
		t.Fatal("已可能执行的额度被释放", q, err)
	}
}

// 物理事务边界：入口手动操作可以不经过 controlMu，旧屏障不能授权新期望。
func TestCrossGroupRotationBarrierRejectsChangedVersion(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);
 INSERT INTO entry_groups(id,name) VALUES('g','组');
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id,written_at) VALUES('g',1,'n','8.8.8.8','a',1);`)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := p.rotationEntries(ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []bool{false, true} {
		if changed {
			if _, err = p.db.Exec("UPDATE entry_publications SET desired_version=2 WHERE group_id='g'"); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		current, err := rotationEntriesCurrent(ctx, tx, "n", entries)
		tx.Rollback()
		if err != nil || current == changed {
			t.Fatalf("屏障版本复核: current=%v changed=%v err=%v", current, changed, err)
		}
	}
}
