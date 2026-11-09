package panel

import (
	"context"
	"fmt"
	"testing"
)

func TestCompatibilityMigrationPreservesVersionThirtyAndAllowsNodeDeletion(t *testing.T) {
	dir := t.TempDir()
	deps := diskDependencies(dir)
	db, err := deps.Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:30] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO nodes(id,name,created_at,maintenance_until) VALUES('n','原有节点',0,123)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	var until, version int
	if err = p.db.QueryRow("SELECT maintenance_until FROM nodes WHERE id='n'").Scan(&until); err != nil || until != 123 {
		t.Fatal("兼容迁移破坏维护证据", err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal("迁移未升级至当前版本", version, err)
	}
	if _, err = p.db.Exec("INSERT INTO node_compatibility(node_id,report) VALUES('n','{}')"); err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec("DELETE FROM nodes WHERE id='n'"); err != nil {
		t.Fatal("能力记录阻止无引用节点删除", err)
	}
}
