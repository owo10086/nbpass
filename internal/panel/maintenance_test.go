package panel

import (
	"context"
	"nbpass/internal/platform"
	"os"
	"path/filepath"
	"testing"
)

// 丢弃 WAL 的文件复制会丢失已提交数据；在线备份必须包含同一事务的完整结果。
func TestOnlineBackupIncludesCommittedWALAndDoesNotMigrateSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := platform.SQLite{Path: filepath.Join(dir, "source.db")}
	db, err := source.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`PRAGMA wal_autocheckpoint=0; CREATE TABLE sample(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO sample VALUES(1,'committed'); PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "backup.db")
	if err = BackupDatabase(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	copyDB, err := (platform.SQLite{Path: target}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var value string
	if err = copyDB.QueryRow("SELECT value FROM sample WHERE id=1").Scan(&value); err != nil || value != "committed" {
		t.Fatalf("备份丢失提交: %q %v", value, err)
	}
	var version int
	db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 1 {
		t.Fatal("备份擅自迁移源数据库")
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("备份权限无效", err)
	}
	if err = BackupDatabase(ctx, source, target); err == nil {
		t.Fatal("覆盖了已有备份")
	}
}
