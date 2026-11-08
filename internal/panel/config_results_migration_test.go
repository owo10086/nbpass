package panel

import (
	"context"
	"fmt"
	"testing"
)

// 真实 main v10 数据库升级到 v11，端口、隧道、DNS 发布与新增报告表须同时可用。
func TestConfigResultsMigrationPreservesVersionTenData(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for version, migration := range migrations[:10] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"INSERT INTO nodes(id,name,created_at,tunnel_listen_port,check_port) VALUES('n','已有节点',0,23443,23080)",
		"INSERT INTO entry_groups(id,name,publication_status) VALUES('g','已有入口组','服务商已写入')",
		"INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id) VALUES('r','已有隧道','g','tunnel',23444,'8.8.8.8','ipv4',443,0,1,'exit')",
		"INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value) VALUES('g',3,'n','8.8.8.8')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	defer p.Close()
	var version, tunnelPort, checkPort, publicationVersion int
	var status, exitGroup string
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 11 {
		t.Fatalf("升级版本错误: %d %v", version, err)
	}
	if err := p.db.QueryRow("SELECT tunnel_listen_port,check_port FROM nodes WHERE id='n'").Scan(&tunnelPort, &checkPort); err != nil || tunnelPort != 23443 || checkPort != 23080 {
		t.Fatalf("端口约束数据丢失: %d %d %v", tunnelPort, checkPort, err)
	}
	if err := p.db.QueryRow("SELECT exit_group_id FROM forward_rules WHERE id='r'").Scan(&exitGroup); err != nil || exitGroup != "exit" {
		t.Fatalf("隧道引用丢失: %q %v", exitGroup, err)
	}
	if err := p.db.QueryRow("SELECT g.publication_status,p.desired_version FROM entry_groups g JOIN entry_publications p ON p.group_id=g.id WHERE g.id='g'").Scan(&status, &publicationVersion); err != nil || status != "服务商已写入" || publicationVersion != 3 {
		t.Fatalf("发布数据丢失: %q %d %v", status, publicationVersion, err)
	}
	for _, query := range []string{
		"INSERT INTO node_rule_reports(node_id,revision,digest,revocations_applied) VALUES('n',1,'digest',1)",
		"INSERT INTO node_rule_results(node_id,rule_id,entry_group_id,listen_port,applied_revision,apply_error) VALUES('n','r','g',23444,1,'')",
	} {
		if _, err := p.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
}
