package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

func TestMaintenanceBackupAndRestorePreserveSecretsAndInvalidateSessions(t *testing.T) {
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = panel.BcryptPasswords{Cost: 4} })
	c := h.Client()
	c.Do("POST", "/api/auth/setup", map[string]string{"password": "backup-password-01"}, 201)
	c.Do("POST", "/api/auth/login", map[string]string{"password": "backup-password-01"}, 200)
	c.Do("PUT", "/api/secrets/private", map[string]string{"value": "recoverable-secret"}, 200)
	backup := filepath.Join(t.TempDir(), "backup")
	var output bytes.Buffer
	if err := maintenanceCommand(context.Background(), "backup", []string{"-data-dir", h.Dir, "-backup-dir", backup}, &output); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := maintenanceCommand(context.Background(), "restore", []string{"-data-dir", restored, "-backup-dir", backup}, &output); err != nil {
		t.Fatal(err)
	}
	p, err := panel.Open(context.Background(), panel.Config{PublicOrigin: "https://panel.test"}, panel.Dependencies{Clock: h.Clock, Persistence: platform.SQLite{Path: filepath.Join(restored, "panel.db")}, KeyFile: platform.LocalKeyFile{Path: filepath.Join(restored, "panel.key")}, Random: h.Dependencies.Random})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	value, err := p.Secrets().Get(context.Background(), "private")
	if err != nil || string(value) != "recoverable-secret" {
		t.Fatalf("恢复丢失密钥: %s %v", value, err)
	}
	db, err := (platform.SQLite{Path: filepath.Join(restored, "panel.db")}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sessions, state int
	db.QueryRow("SELECT count(*) FROM sessions").Scan(&sessions)
	db.QueryRow("SELECT count(*) FROM recovery_state").Scan(&state)
	if sessions != 0 || state != 1 {
		t.Fatal("恢复没有在启动前原子撤销会话并暂停", sessions, state)
	}
	if err = maintenanceCommand(context.Background(), "restore", []string{"-data-dir", restored, "-backup-dir", backup}, &output); err == nil {
		t.Fatal("覆盖了已有恢复目录")
	}
	if err = os.WriteFile(filepath.Join(backup, "panel.key"), bytes.Repeat([]byte{1}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err = maintenanceCommand(context.Background(), "restore", []string{"-data-dir", filepath.Join(t.TempDir(), "bad"), "-backup-dir", backup}, &output); err == nil {
		t.Fatal("密钥损坏仍被接受")
	}
}

func TestUpgradeBacksUpBeforeFailedMigrationAndNamesRecoveryScope(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := (platform.SQLite{Path: filepath.Join(dir, "panel.db")}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 故意构造不兼容的现有表，使迁移 1 失败。
	if _, err = db.Exec("CREATE TABLE administrator(conflict TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err = (platform.LocalKeyFile{Path: filepath.Join(dir, "panel.key")}).LoadOrCreate(ctx, strings.NewReader(strings.Repeat("k", 32))); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "before-upgrade")
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err = maintenanceCommand(ctx, "upgrade", []string{"-data-dir", dir, "-backup-dir", backup, "-program", program}, &output)
	if err == nil {
		t.Fatal("冲突迁移未失败")
	}
	original, e := (platform.SQLite{Path: filepath.Join(dir, "panel.db")}).Open(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer original.Close()
	var version int
	var column string
	if e = original.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != 0 {
		t.Fatal("失败迁移改变了原库版本", version, e)
	}
	if e = original.QueryRow("SELECT name FROM pragma_table_info('administrator')").Scan(&column); e != nil || column != "conflict" {
		t.Fatal("失败迁移改变原表", column, e)
	}
	for _, name := range []string{"panel.db", "panel.key", "nbpass", "manifest.json", "RECOVERY.md"} {
		if _, e := os.Stat(filepath.Join(backup, name)); e != nil {
			t.Fatal("迁移前没有完成备份", name, e)
		}
	}
	for _, part := range []string{"程序", "数据库", "密钥", "restore"} {
		if !strings.Contains(output.String()+err.Error(), part) {
			t.Fatal("遗漏恢复范围", part, output.String(), err)
		}
	}
}
