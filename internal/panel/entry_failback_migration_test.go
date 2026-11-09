package panel

import (
	"context"
	"fmt"
	"testing"
)

func TestEntryFailbackMigrationPreservesVersionTwentyEight(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for v, migration := range migrations[:28] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','既有节点',0);
 INSERT INTO entry_groups(id,name,auto_failback_paused) VALUES('g','既有组',1);
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
	var version, pause, notified, written int
	var active string
	err = p.db.QueryRow(`SELECT s.desired_version,s.active_node_id,s.written_at,g.auto_failback_paused,a.failback_notified FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id JOIN entry_actions a ON a.id=s.current_action_id WHERE s.group_id='g'`).Scan(&version, &active, &written, &pause, &notified)
	if err != nil || version != 4 || active != "n" || written != 2 || pause != 1 || notified != 0 {
		t.Fatal("升级丢失发布/驻留起点/暂停，或通知标记默认值错误", version, active, written, pause, notified, err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal("迁移未升级到当前版本", version, err)
	}
}
