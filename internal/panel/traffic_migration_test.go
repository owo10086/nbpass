package panel

import (
	"context"
	"fmt"
	"testing"
)

// 存储物理升级回归：版本9的规则不变，补报授权回填，既有迁移不重排。
func TestTrafficMigrationPreservesVersionNineRulesAndSources(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for version, migration := range migrations[:9] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO nodes(id,name,created_at) VALUES('entry','已有入口',0)`,
		`INSERT INTO entry_groups(id,name) VALUES('group','已有入口组')`,
		`INSERT INTO entry_group_members VALUES('group','entry',0)`,
		`INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled) VALUES('rule','已有规则','group','direct',12345,'8.8.8.8','ipv4',443,0,1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	defer p.Close()
	var version, count int
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("未追加迁移10: %d %v", version, err)
	}
	if err := p.db.QueryRow(`SELECT count(*) FROM rule_traffic_sources WHERE node_id='entry' AND rule_id='rule'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("既有入口授权未回填: %d %v", count, err)
	}
	var target string
	if err := p.db.QueryRow(`SELECT target_address FROM forward_rules WHERE id='rule'`).Scan(&target); err != nil || target != "8.8.8.8" {
		t.Fatalf("升级改动既有规则: %q %v", target, err)
	}
}
