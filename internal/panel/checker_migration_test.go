package panel

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"testing"
)

func TestCheckerMigrationPreservesVersionFourteen(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for v, m := range migrations[:14] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"INSERT INTO nodes(id,name,created_at) VALUES('n','已有节点',1)", "INSERT INTO node_self_check(node_id,enabled,state,consecutive,last_sequence) VALUES('n',1,'suspected',1,1)", "INSERT INTO self_check_rounds(node_id,round_id,sequence,config_revision,started_at,finished_at,received_at,result,final_result,evidence_source,raw) VALUES('n','round',1,7,1,2,3,'unknown','unknown','self_check',X'7B7D')"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p := openDiskPanel(t, dir)
	var result, source string
	var version int
	if err = p.db.QueryRow("SELECT final_result,evidence_source FROM self_check_rounds WHERE node_id='n'").Scan(&result, &source); err != nil || result != "unknown" || source != "self_check" {
		t.Fatalf("升级破坏原始证据 %s %s %v", result, source, err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 15 {
		t.Fatalf("迁移号 %d %v", version, err)
	}
	for _, table := range []string{"checker_rounds", "checker_tasks"} {
		var count int
		if err = p.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("升级伪造补判 %s %d %v", table, count, err)
		}
	}
}

// 旧版已有快照不含检测配置；升级时必须补齐非默认端口，无需管理员重新保存。
func TestCheckerUpgradeCompilesExistingCustomPort(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for v, m := range migrations[:14] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1))
	}
	snapshot := &controlpb.ConfigSnapshot{Revision: 7}
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	data, _ := proto.Marshal(snapshot)
	if _, err = db.Exec("INSERT INTO nodes(id,name,created_at,desired_revision,check_port) VALUES('n','旧版节点',1,7,18081)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES('n',?,?)", data, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	m, err := p.controlMessage(context.Background(), "n")
	if err != nil {
		t.Fatal(err)
	}
	if m.Snapshot.CheckPort != 18081 || m.DesiredRevision != 8 {
		t.Fatalf("升级未下发既有检测端口: %v", m)
	}
}
