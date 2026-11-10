package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// 只用旧 SQL 安排升级输入，迁移后的模式与发布状态经面板 HTTP 读取。
func TestAllPublishMigrationKeepsExistingPrimaryPublication(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous := 0
	for i, m := range migrations {
		if m == managedRecordModelMigration {
			previous = i + 1
			break
		}
	}
	for i, m := range migrations[:previous] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','既有节点',0);
 INSERT INTO entry_groups(id,name) VALUES('g','既有主备组');
 INSERT INTO entry_group_members VALUES('g','n',0);
 INSERT INTO dns_accounts(id,name,provider) VALUES('dns','原账号','dnspod');
 INSERT INTO managed_records(id,group_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings) VALUES('m','g','dns','dnspod','example.com','100','entry','默认','0','9.9.9.9',600,'[]');
 INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,write_started) VALUES('a','g',7,'manual_switch','n','9.9.9.9','8.8.8.8',600,'结果未确认',0,1);
 INSERT INTO entry_action_records(action_id,managed_record_id,binding_generation,provider,domain,record_id,old_value,new_value,old_ttl,old_enabled,new_enabled,stage,write_started) VALUES('a','m',1,'dnspod','example.com','100','9.9.9.9','8.8.8.8',600,1,1,'结果未确认',1);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id) VALUES('g',7,'n','8.8.8.8','a');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	var groups map[string]any
	if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/entry-groups", nil, cookie, 200).Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	group := groups["entry_groups"].([]any)[0].(map[string]any)
	if group["publication_mode"] != "primary_backup" || group["minimum_kept"] != float64(1) {
		t.Fatalf("已有组未迁移成主备：%v", group)
	}
	var pub map[string]any
	if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/entry-groups/g/publication", nil, cookie, 200).Body.Bytes(), &pub); err != nil {
		t.Fatal(err)
	}
	a := pub["action"].(map[string]any)
	if pub["desired_version"] != float64(7) || pub["active_node_id"] != "n" || a["stage"] != EntryUnconfirmed || len(a["records"].([]any)) != 1 {
		t.Fatalf("迁移丢失发布或逐条在途动作：%v", pub)
	}
}
