package panel

import (
	"context"
	"fmt"
	"testing"
)

// 从已合并的22版升级，不把无法绑定当前IP的历史正常轮伪装成新证据。
func TestReadinessMigrationPreservesVersionTwentyTwoAndInvalidatesEvidence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for v, m := range migrations[:22] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','旧节点',0); INSERT INTO node_regions(node_id,public_ipv4) VALUES('n','8.8.8.8'); INSERT INTO entry_groups(id,name,auto_failback_paused) VALUES('g','旧组',1); INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	var version, count int
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal(version, err)
	}
	if err = p.db.QueryRow("SELECT count(*) FROM node_domestic_evidence").Scan(&count); err != nil || count != 0 {
		t.Fatal("升级应等待新证据", count, err)
	}
	if _, err = p.db.Exec(`INSERT INTO node_domestic_evidence(node_id,public_ipv4,observed_at,source) VALUES('n','8.8.8.8',1,'self_check'); UPDATE node_regions SET public_ipv4='1.1.1.1' WHERE node_id='n'; UPDATE node_regions SET public_ipv4='8.8.8.8' WHERE node_id='n'`); err != nil {
		t.Fatal(err)
	}
	if err = p.db.QueryRow("SELECT count(*) FROM node_domestic_evidence").Scan(&count); err != nil || count != 0 {
		t.Fatal("A→B→A复活旧证据", count, err)
	}
	if err = p.db.QueryRow("SELECT auto_failback_paused FROM entry_groups WHERE id='g'").Scan(&count); err != nil || count != 1 {
		t.Fatal("丢失旧组暂停", count, err)
	}
}
