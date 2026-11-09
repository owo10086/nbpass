package panel

import (
	"context"
	"fmt"
	"testing"
)

func TestEntryBlockedRotationMigrationPreservesVersionThirtyThree(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for v, m := range migrations[:33] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','原节点',0);
 INSERT INTO entry_groups(id,name,blocked_policy,auto_failback_paused) VALUES('g','既有组','switch_entry',1);
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0);
 INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,written_at)
 VALUES('a','g',7,'manual_switch','n','9.9.9.9','8.8.8.8',60,'服务商已写入',1,2);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id,written_at) VALUES('g',7,'n','8.8.8.8','a',2);
 INSERT INTO entry_blocked_decisions(id,group_id,node_id,incident_id,desired_version,outcome,created_at) VALUES('d','g','n','i',6,'submitted',1);
 INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('r','n','8.8.8.8','unconfirmed',3,4,5,'secret');
 INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES('r','n',3);
 INSERT INTO rotation_policy(node_id,no_improvement,paused) VALUES('n',2,1);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var version, desired, written, paused, attempts, noImprovement int
	var active, policy, decisionPolicy, rotationState string
	err = p.db.QueryRow(`SELECT s.desired_version,s.written_at,s.active_node_id,g.blocked_policy,g.auto_failback_paused,d.policy,a.state,
 (SELECT COUNT(*) FROM rotation_quota),r.no_improvement FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id
 JOIN entry_blocked_decisions d ON d.group_id=g.id JOIN rotation_actions a ON a.node_id=s.active_node_id JOIN rotation_policy r ON r.node_id=a.node_id`).Scan(&desired, &written, &active, &policy, &paused, &decisionPolicy, &rotationState, &attempts, &noImprovement)
	if err != nil || desired != 7 || written != 2 || active != "n" || policy != "switch_entry" || paused != 1 || decisionPolicy != "switch_entry" || rotationState != "unconfirmed" || attempts != 1 || noImprovement != 2 {
		t.Fatal("迁移改变已有发布、事故、暂停、冻结或额度", desired, written, active, policy, paused, decisionPolicy, rotationState, attempts, noImprovement, err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal(version, err)
	}
}
