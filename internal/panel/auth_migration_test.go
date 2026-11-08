package panel

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// 只检查迁移物理属性：票 02 的数据库升级后不应丢失既有认证资料和节点。
func TestAuthMigrationPreservesVersionTwoCredentialsAndNodes(t *testing.T) {
	dir := t.TempDir()
	deps := diskDependencies(dir)
	db, err := deps.Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for version, migration := range migrations[:2] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("existing-admin-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	tokenHash := sha256.Sum256([]byte("existing-node-token"))
	if _, err := db.Exec("INSERT INTO administrator(id, password_hash) VALUES(1, ?)", string(passwordHash)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO nodes(id, name, token_hash, created_at) VALUES('existing', '已有节点', ?, 0)", tokenHash[:]); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	defer p.Close()
	var preservedHash string
	if err := p.db.QueryRow("SELECT password_hash FROM administrator WHERE id = 1").Scan(&preservedHash); err != nil {
		t.Fatal(err)
	}
	if preservedHash != string(passwordHash) {
		t.Fatal("认证迁移修改了原密码哈希")
	}
	var count int
	if err := p.db.QueryRow("SELECT count(*) FROM nodes WHERE token_hash = ?", tokenHash[:]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("节点凭据未保留: %d %v", count, err)
	}
	if err := p.db.QueryRow("SELECT count(*) FROM auth_trusted_proxies").Scan(&count); err != nil || count != 0 {
		t.Fatalf("迁移不应默认信任代理: %d %v", count, err)
	}
}
