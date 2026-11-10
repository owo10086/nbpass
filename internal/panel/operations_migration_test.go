package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// 用升级前的磁盘作为输入，只从鉴权 API 验证业务资料和新故障接口。
func TestOperationsMigrationPreservesExistingObjects(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations {
		if m == incidentsMigration {
			break
		}
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	statements := []string{`INSERT INTO nodes(id,name,created_at,desired_revision,applied_revision) VALUES('n','升级前入口',0,7,7)`, `INSERT INTO entry_groups(id,name) VALUES('g','升级前入口组')`, `INSERT INTO entry_group_members VALUES('g','n',0)`, `INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id) VALUES('r','升级前规则','g','direct',23456,'8.8.8.8','ipv4',443,0,1,'')`}
	for _, q := range statements {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixNano()
	if _, err = db.Exec(`INSERT INTO events(node_id,source,event_id,type,occurred_at,received_at,config_revision,message) VALUES('n','node','old','upgrade_fixture',?,?,7,'升级前事件')`, now, now); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	defer p.Close()
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "operations-upgrade-test-password"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "operations-upgrade-test-password"}, nil, 200).Result().Cookies()[0]
	var nodes struct {
		Nodes []struct {
			ID      string `json:"id"`
			Desired int    `json:"desired_revision"`
		}
	}
	if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/nodes", nil, cookie, 200).Body.Bytes(), &nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes.Nodes) != 1 || nodes.Nodes[0].ID != "n" || nodes.Nodes[0].Desired != 7 {
		t.Fatalf("升级改变节点或配置版本: %+v", nodes)
	}
	var view operationsView
	if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/overview", nil, cookie, 200).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Rules) != 1 || view.Rules[0].ID != "r" || view.Rules[0].Status != "unknown" {
		t.Fatalf("升级丢失规则或虚报正常: %+v", view.Rules)
	}
	var events struct {
		Events []json.RawMessage `json:"events"`
	}
	if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/events?type=upgrade_fixture", nil, cookie, 200).Body.Bytes(), &events); err != nil || len(events.Events) != 1 {
		t.Fatalf("升级丢失事件: %v %s", err, events.Events)
	}
	diskRequest(t, p, "GET", "/api/incidents", nil, cookie, 200)
}
