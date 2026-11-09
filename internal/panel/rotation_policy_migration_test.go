package panel

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRotationPolicyMigrationPreservesVersionTwenty(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for v, m := range migrations[:20] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixNano()
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','旧节点',0);INSERT INTO entry_groups(id,name,auto_failback_paused) VALUES('g','旧入口组',1);INSERT INTO rotation_settings(node_id,secret_name) VALUES('n','old-secret');INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name,sequence,attempts,observation_complete) VALUES('changed','n','8.8.8.8','changed',?,?,?,'old-secret',8,1,1),('cancelled','n','8.8.8.8','cancelled',?,?,?,'old-secret',1,0,1),('frozen','n','8.8.8.8','unconfirmed',?,?,?,'old-secret',10,5,1)`, now, now, now, now, now, now, now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var version, count int
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal(version, err)
	}
	if err = p.db.QueryRow("SELECT count(*) FROM rotation_quota WHERE node_id='n'").Scan(&count); err != nil || count != 2 {
		t.Fatal("升级未保留实际尝试", count, err)
	}
	var state string
	var seq uint64
	var auto, paused bool
	if err = p.db.QueryRow("SELECT state,sequence FROM rotation_actions WHERE id='frozen'").Scan(&state, &seq); err != nil || state != "unconfirmed" || seq != 10 {
		t.Fatal("升级改变旧动作", state, seq, err)
	}
	if err = p.db.QueryRow("SELECT automatic_enabled FROM rotation_settings WHERE node_id='n'").Scan(&auto); err != nil || auto {
		t.Fatal("升级默认开启了自动换 IP", auto, err)
	}
	if err = p.db.QueryRow("SELECT auto_failback_paused FROM entry_groups WHERE id='g'").Scan(&paused); err != nil || !paused {
		t.Fatal("升级清空手动切换暂停", paused, err)
	}
}
