package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// 用第 40 版 SQL 安排升级输入，历史检测、任务与发布状态经 HTTP 核对。
func TestAllPublishOfflineMigrationPreservesPrimaryEvidenceAndInvalidatesUnboundMemberEvidence(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, m := range migrations[:40] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('a','全部发布成员',0),('b','主备成员',0);
INSERT INTO entry_groups(id,name,publication_mode,first_published_at) VALUES('all','全部发布','all',1),('primary','主备','primary_backup',1);
INSERT INTO entry_group_members VALUES('all','a',0),('primary','b',0);
INSERT INTO entry_publications(group_id,desired_version) VALUES('all',7),('primary',8);
INSERT INTO offline_incidents(id,node_id,offline_at) VALUES(1,'a',0),(2,'b',0);
INSERT INTO entry_offline_checks(id,group_id,node_id,incident_id,desired_version,target_ip,target_port,config_revision,issued_at,deadline,outcome)
VALUES('member','all','a',1,7,'8.8.8.8',18080,0,0,1,'unreachable'),('primary','primary','b',2,8,'1.1.1.1',18080,0,0,1,'pending');
INSERT INTO entry_offline_tasks(id,check_id,checker_id,task) VALUES('member-task','member','checker','{}'),('primary-task','primary','checker','{}');`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	for _, tc := range []struct {
		group, outcome, task string
		version              float64
	}{
		{"all", "invalidated", "member-task", 7}, {"primary", "pending", "primary-task", 8},
	} {
		var view map[string]any
		if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/entry-groups/"+tc.group+"/offline-checks", nil, cookie, 200).Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		checks := view["checks"].([]any)
		if len(checks) != 1 {
			t.Fatalf("升级丢失历史检测：%v", view)
		}
		check := checks[0].(map[string]any)
		if check["outcome"] != tc.outcome || check["desired_version"] != tc.version {
			t.Fatalf("升级改变主备证据或沿用缺少绑定身份的成员证据：%v", check)
		}
		tasks := check["checkers"].([]any)
		if len(tasks) != 1 || tasks[0].(map[string]any)["id"] != tc.task {
			t.Fatalf("升级丢失检测任务：%v", tasks)
		}
		if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/entry-groups/"+tc.group+"/publication", nil, cookie, 200).Body.Bytes(), &view); err != nil || view["desired_version"] != tc.version {
			t.Fatalf("升级改变组期望版本：%v，%v", view, err)
		}
	}
}
