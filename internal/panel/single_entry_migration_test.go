package panel

import (
	"context"
	"fmt"
	"testing"
)

// 重建父表不能丢掉级联外键下的流量、授权或原有组入口。
func TestSingleEntryMigrationPreservesRulesAndDependentRows(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	previous := 0
	for i, m := range migrations {
		if m == crossGroupRotationMigration {
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
	statements := []string{
		`INSERT INTO nodes(id,name,created_at) VALUES('n','旧入口',0)`,
		`INSERT INTO entry_groups(id,name) VALUES('g','旧组')`,
		`INSERT INTO entry_group_members VALUES('g','n',0)`,
		`INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id) VALUES('r','旧规则','g','direct',23456,'8.8.8.8','ipv4',443,0,1,'')`,
		`INSERT INTO rule_traffic_totals VALUES('r',123,456)`,
		`INSERT INTO rule_traffic_cursors VALUES('n','旧进程','r',0,2,123,456)`,
		`INSERT INTO rule_traffic_realtime VALUES('n','r','旧进程',2,1,0,0)`,
		`INSERT INTO rule_traffic_sources VALUES('n','r')`,
		`INSERT INTO rule_authorizations VALUES('r',3,'旧授权指纹')`,
		`INSERT INTO tunnel_authorization_confirmations VALUES('n','n','r',3)`,
	}
	for _, s := range statements {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	var count int
	if err = p.db.QueryRow(`SELECT count(*) FROM forward_rules WHERE id='r' AND entry_group_id='g' AND entry_node_id IS NULL AND listen_port=23456`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("原入口组未保留: %d %v", count, err)
	}
	for _, table := range []string{"rule_traffic_totals", "rule_traffic_cursors", "rule_traffic_realtime", "rule_traffic_sources", "rule_authorizations", "tunnel_authorization_confirmations"} {
		if err = p.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("迁移丢失 %s: %d %v", table, count, err)
		}
	}
	var up, down, generation int
	if err = p.db.QueryRow(`SELECT upload_bytes,download_bytes,generation FROM rule_traffic_totals JOIN rule_authorizations USING(rule_id)`).Scan(&up, &down, &generation); err != nil || up != 123 || down != 456 || generation != 3 {
		t.Fatalf("累计量或授权高水位改变: %d/%d/%d %v", up, down, generation, err)
	}
	for _, s := range []string{
		`UPDATE forward_rules SET entry_node_id='n' WHERE id='r'`,
		`UPDATE forward_rules SET entry_group_id=NULL WHERE id='r'`,
		`UPDATE forward_rules SET entry_group_id='' WHERE id='r'`,
		`UPDATE forward_rules SET entry_group_id=NULL,entry_node_id='不存在' WHERE id='r'`,
	} {
		if _, err = p.db.Exec(s); err == nil {
			t.Fatalf("数据库接受非法入口: %s", s)
		}
	}
	if _, err = p.db.Exec(`UPDATE forward_rules SET entry_group_id=NULL,entry_node_id='n' WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec(`DELETE FROM nodes WHERE id='n'`); err == nil {
		t.Fatal("丢失节点外键")
	}
	rows, err := p.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("重建留下外键破坏")
	}
	rows.Close()
	if _, err = p.db.Exec(`DELETE FROM forward_rules WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"rule_traffic_totals", "rule_traffic_cursors", "rule_traffic_realtime", "rule_traffic_sources", "rule_authorizations", "tunnel_authorization_confirmations"} {
		if err = p.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("丢失级联外键 %s: %d %v", table, count, err)
		}
	}
}
