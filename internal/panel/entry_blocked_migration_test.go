package panel

import (
	"context"
	"fmt"
	"testing"
)

func TestEntryBlockedMigrationPreservesVersionThirtyOne(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for v, migration := range migrations[:31] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','既有节点',0);
 INSERT INTO node_regions(node_id,public_ipv4) VALUES('n','8.8.8.8');
 INSERT INTO node_self_check(node_id,enabled,state,last_sequence) VALUES('n',1,'blocked',7);
 INSERT INTO entry_groups(id,name,blocked_policy,auto_failback_paused) VALUES('g','既有组','rotate_ip',1);
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0);
 INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,written_at)
 VALUES('a','g',4,'manual_switch','n','9.9.9.9','8.8.8.8',60,'服务商已写入',1,2);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id,written_at) VALUES('g',4,'n','8.8.8.8','a',2)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var version, pause, written int
	var active, policy, incident, ip string
	err = p.db.QueryRow(`SELECT s.desired_version,s.active_node_id,s.written_at,g.auto_failback_paused,g.blocked_policy,i.incident_id,i.target_ip FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id JOIN entry_blocked_incidents i ON i.node_id=s.active_node_id WHERE g.id='g'`).Scan(&version, &active, &written, &pause, &policy, &incident, &ip)
	if err != nil || version != 4 || active != "n" || written != 2 || pause != 1 || policy != "rotate_ip" || incident == "" || ip != "8.8.8.8" {
		t.Fatal("迁移改变已有发布或未恢复既有被墙事故", version, active, written, pause, policy, incident, ip, err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal("迁移版本错误", version, err)
	}
}

func TestEntryBlockedDecisionDoesNotPreventUnreferencedNodeDeletion(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','历史事故成员',0);
 INSERT INTO entry_groups(id,name) VALUES('g','入口组');
 INSERT INTO entry_blocked_decisions(id,group_id,node_id,incident_id,desired_version,outcome,created_at)
 VALUES('d','g','n','i',1,'no_candidate',1);`)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := p.removeNode(context.Background(), "n")
	if err != nil || len(refs) != 0 {
		t.Fatal("无业务引用的节点被事故待办阻止删除", refs, err)
	}
}
