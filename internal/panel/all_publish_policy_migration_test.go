package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// 仅以旧 SQL 安排升级输入；升级后的期望、手动锁与未确认动作通过 HTTP 验证。
func TestAllPublishPolicyMigrationPreservesMemberExpectationAndPendingAction(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous := 0
	for i, m := range migrations {
		if m == allPublishMigration {
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
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','既有成员',0);
INSERT INTO entry_groups(id,name,publication_mode,minimum_kept,first_published_at) VALUES('g','既有全部发布组','all',2,1);
INSERT INTO entry_group_members VALUES('g','n',0);
INSERT INTO dns_accounts(id,name,provider) VALUES('dns','原账号','dnspod');
INSERT INTO managed_records(id,owner_kind,group_id,node_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,current_enabled,warnings) VALUES('m','member','g','n','dns','dnspod','example.com','100','entry','默认','0','8.8.8.8',60,0,'[]');
INSERT INTO entry_member_publications(group_id,node_id,expected_value,expected_enabled,address_pending,manual_locked,reason) VALUES('g','n','8.8.8.8',0,0,1,'手动暂停');
INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,write_started) VALUES('a','g',7,'sync_address','n','','',60,'结果未确认',0,1);
INSERT INTO entry_action_records(action_id,managed_record_id,binding_generation,provider,domain,record_id,old_value,new_value,old_ttl,old_enabled,new_enabled,stage,write_started) VALUES('a','m',1,'dnspod','example.com','100','8.8.8.8','1.1.1.1',60,0,0,'结果未确认',1);
INSERT INTO entry_publications(group_id,desired_version,current_action_id) VALUES('g',7,'a');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	var pub map[string]any
	if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/entry-groups/g/publication", nil, cookie, 200).Body.Bytes(), &pub); err != nil {
		t.Fatal(err)
	}
	m := pub["members"].([]any)[0].(map[string]any)
	if pub["minimum_kept"] != float64(2) || pub["first_published"] != true || pub["desired_version"] != float64(7) || m["expected_value"] != "8.8.8.8" || m["expected_enabled"] != false || m["manual_locked"] != true || m["withdrawn_at"] != nil {
		t.Fatalf("迁移改变既有成员期望：%v", pub)
	}
	a := pub["action"].(map[string]any)
	if a["id"] != "a" || a["stage"] != EntryUnconfirmed || len(a["records"].([]any)) != 1 {
		t.Fatalf("迁移丢失在途动作：%v", a)
	}
}
