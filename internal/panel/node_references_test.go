package panel

import (
	"context"
	"testing"
)

func TestRuntimeHistoryDoesNotPreventUnreferencedNodeDeletion(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','运行过的节点',0);
 INSERT INTO entry_groups(id,name) VALUES('g','入口组');
 INSERT INTO offline_incidents(id,node_id,offline_at) VALUES(1,'n',1);
 INSERT INTO entry_offline_checks(id,group_id,node_id,incident_id,desired_version,target_ip,target_port,config_revision,issued_at,deadline)
 VALUES('c','g','n',1,1,'8.8.8.8',443,1,1,2);
 INSERT INTO entry_offline_tasks(id,check_id,checker_id,task) VALUES('t','c','x',X'');
 INSERT INTO node_upgrades(id,node_id,manifest,progress,initial_instance,created_at) VALUES('u','n',X'',X'','i',1);
 INSERT INTO node_rule_reports(node_id,revision,digest,revocations_applied) VALUES('n',1,'d',0);
 INSERT INTO node_rule_results(node_id,rule_id,entry_group_id,listen_port,applied_revision,apply_error) VALUES('n','r','g',8443,1,'');
 INSERT INTO rotation_settings(node_id,secret_name) VALUES('n','s');
 INSERT INTO rotation_policy(node_id) VALUES('n');
 INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('a','n','1.1.1.1','changed',1,2,1,'s');
 INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES('a','n',1);`)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := p.removeNode(context.Background(), "n")
	if err != nil || len(refs) != 0 {
		t.Fatal("无业务引用的节点被运行历史阻止删除", refs, err)
	}
	var left int
	if err = p.db.QueryRow("SELECT count(*) FROM nodes WHERE id='n'").Scan(&left); err != nil || left != 0 {
		t.Fatal("节点未删除", left, err)
	}
}
