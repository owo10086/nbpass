package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

// 凭据若以明文落盘、权限过宽、重启无法解密或跟随符号链接，本测试失败。
func TestFileStoreEncryptsCredentialsAndRejectsUnsafeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	ctx := context.Background()
	store := FileStore{Dir: dir, Random: rand.Reader}
	value := Credentials{ControlURL: "https://control.example", NodeID: "node-a", Token: "node-token-plaintext-marker-12345678901234567"}
	if err := store.Save(ctx, value); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("缓存文件权限: %v %v", info, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.Contains(data, []byte(value.Token)) {
			t.Fatalf("凭据明文落盘: %v", err)
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("目录权限不是 0700")
	}
	reopened := FileStore{Dir: dir, Random: rand.Reader}
	loaded, err := reopened.Load(ctx)
	if err != nil || loaded != value {
		t.Fatalf("无法恢复凭据: %v", err)
	}
	path := filepath.Join(dir, "credentials.enc")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Load(ctx); err == nil {
		t.Fatal("接受了宽松文件权限")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("do-not-change"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Load(ctx); err == nil {
		t.Fatal("跟随了凭据符号链接")
	}
	if err := reopened.Save(ctx, value); err == nil {
		t.Fatal("覆盖了符号链接")
	}
}
