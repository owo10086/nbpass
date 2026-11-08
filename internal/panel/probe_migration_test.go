package panel

import (
	"context"
	"fmt"
	"testing"
)

// 物理升级回归：已部署的版本 4 只追加探针表，不重排地区/认证迁移或丢失数据。
func TestProbeMigrationPreservesVersionFourRegionAndAuthData(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, migration := range migrations[:4] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		"INSERT INTO nodes(id, name, created_at) VALUES('existing', '已有节点', 0)",
		"INSERT INTO node_regions(node_id, public_ipv4, auto_name, auto_mainland, override_name, checker_disabled) VALUES('existing', '223.5.5.5', '中国大陆', 1, '人工地区', 1)",
		"INSERT INTO auth_login_failures(source, failures, locked_until) VALUES('192.0.2.1', 2, 123)",
		"INSERT INTO auth_trusted_proxies(prefix) VALUES('192.0.2.0/24')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var version int
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatalf("应升级到版本 5: %d %v", version, err)
	}
	var ip, override string
	var disabled int
	if err := p.db.QueryRow("SELECT public_ipv4, override_name, checker_disabled FROM node_regions WHERE node_id = 'existing'").Scan(&ip, &override, &disabled); err != nil || ip != "223.5.5.5" || override != "人工地区" || disabled != 1 {
		t.Fatalf("地区与角色数据未保留: %q %q %d %v", ip, override, disabled, err)
	}
	var failures, lockedUntil int
	if err := p.db.QueryRow("SELECT failures, locked_until FROM auth_login_failures WHERE source = '192.0.2.1'").Scan(&failures, &lockedUntil); err != nil || failures != 2 || lockedUntil != 123 {
		t.Fatalf("认证限流数据未保留: %d %d %v", failures, lockedUntil, err)
	}
	var prefix string
	if err := p.db.QueryRow("SELECT prefix FROM auth_trusted_proxies").Scan(&prefix); err != nil || prefix != "192.0.2.0/24" {
		t.Fatalf("可信代理未保留: %q %v", prefix, err)
	}
	for _, table := range []string{"probe_realtime", "probe_minutes"} {
		var count int
		if err := p.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("探针表 %s 未正确创建: %d %v", table, count, err)
		}
	}
}
