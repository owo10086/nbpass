package panel

import (
	"context"
	"fmt"
	"testing"
)

// 从当前main的完整27版迁移链升级，保留已有发布、手动暂停和离线事故。
func TestEntryOfflineMigrationPreservesVersionTwentySeven(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for v, m := range migrations[:27] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','旧节点',0);
 INSERT INTO node_regions(node_id,public_ipv4) VALUES('n','8.8.8.8');
 INSERT INTO entry_groups(id,name,auto_failback_paused) VALUES('g','旧组',1);
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value) VALUES('g',4,'n','8.8.8.8');
 INSERT INTO offline_incidents(node_id,offline_at) VALUES('n',0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var version, pause int
	var active, ip string
	if err = p.db.QueryRow("SELECT desired_version,active_node_id,expected_value FROM entry_publications WHERE group_id='g'").Scan(&version, &active, &ip); err != nil || version != 4 || active != "n" || ip != "8.8.8.8" {
		t.Fatal("丢失已有发布", version, active, ip, err)
	}
	if err = p.db.QueryRow("SELECT auto_failback_paused FROM entry_groups WHERE id='g'").Scan(&pause); err != nil || pause != 1 {
		t.Fatal("丢失手动暂停", pause, err)
	}
	// 新确认表能引用已有事故；配置/地址往返变化不能复活已失效任务。
	_, err = p.db.Exec(`INSERT INTO entry_offline_checks(id,group_id,node_id,incident_id,desired_version,target_ip,target_port,config_revision,issued_at,deadline) VALUES('c','g','n',1,4,'8.8.8.8',18080,0,0,1);
 UPDATE node_regions SET public_ipv4='1.1.1.1' WHERE node_id='n';
 UPDATE node_regions SET public_ipv4='8.8.8.8' WHERE node_id='n'`)
	if err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err = p.db.QueryRow("SELECT outcome FROM entry_offline_checks WHERE id='c'").Scan(&outcome); err != nil || outcome != "invalidated" {
		t.Fatal("旧地址确认复活", outcome, err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal("迁移版本", version, err)
	}
	if _, err = p.db.Exec("DELETE FROM entry_groups WHERE id='g'"); err != nil {
		t.Fatal("无法级联清理组确认", err)
	}
}
