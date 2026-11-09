package panel

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 从已有 rotationMigration 的数据库追加升级，旧事件/动作/节点数据仍可使用。
func TestSelfCheckMigrationPreservesVersionTwelveData(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, migration := range migrations[:12] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"INSERT INTO nodes(id,name,created_at) VALUES('n','已有节点',123)", fmt.Sprintf("INSERT INTO events(node_id,source,event_id,type,occurred_at,received_at,config_revision,message) VALUES('n','panel','kept','node_offline',%d,2,3,'旧事件')", time.Now().Add(-time.Hour).UnixNano()), "INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES('a','n','8.8.8.8','unconfirmed',1,2,3,'secret')"} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	for query, want := range map[string]string{"SELECT message FROM events WHERE event_id='kept'": "旧事件", "SELECT state FROM rotation_actions WHERE id='a'": "unconfirmed", "SELECT name FROM nodes WHERE id='n'": "已有节点"} {
		var got string
		if err := p.db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("升级数据丢失: %s=%q %v", query, got, err)
		}
	}
	for _, table := range []string{"self_check_rounds", "node_self_check", "self_check_settings", "self_check_notifications"} {
		var count int
		if err := p.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("升级错误预置自测数据: %s %v", table, err)
		}
	}
}
