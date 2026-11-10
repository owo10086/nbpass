package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
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

// 同一身份重装须保留全部缓存；换节点、换控制地址或凭据缺失时只清身份状态，不动密钥与凭据。
func TestEnrollmentClearsOnlyForeignIdentityState(t *testing.T) {
	ctx := context.Background()
	current := Credentials{ControlURL: "https://control.example", NodeID: "node-a", Token: "a"}
	identityFiles := []string{"config.enc", "events.enc", "rotation.enc", "self-check.enc", "traffic.enc"}
	prepare := func(t *testing.T, cacheOwner Credentials) FileStore {
		store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
		if err := store.Save(ctx, current); err != nil {
			t.Fatal(err)
		}
		cache, _ := json.Marshal(configCache{Version: 1, NodeID: cacheOwner.NodeID, ControlURL: cacheOwner.ControlURL})
		if err := store.SaveConfig(ctx, cache); err != nil {
			t.Fatal(err)
		}
		for _, name := range identityFiles[1:] {
			if err := os.WriteFile(filepath.Join(store.Dir, name), []byte("x"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return store
	}
	for _, c := range []struct {
		name        string
		hasPrevious bool
		cacheOwner  Credentials
		next        Credentials
		cleared     bool
	}{
		{"同一身份重置 token", true, current, Credentials{ControlURL: current.ControlURL, NodeID: "node-a", Token: "b"}, false},
		{"换节点", true, current, Credentials{ControlURL: current.ControlURL, NodeID: "node-b", Token: "b"}, true},
		{"换控制地址", true, current, Credentials{ControlURL: "https://control-2.example", NodeID: "node-a", Token: "b"}, true},
		{"凭据缺失", false, current, Credentials{ControlURL: current.ControlURL, NodeID: "node-a", Token: "b"}, true},
		{"凭据已是新身份但缓存属旧身份", true, Credentials{ControlURL: current.ControlURL, NodeID: "node-old"}, Credentials{ControlURL: current.ControlURL, NodeID: "node-a", Token: "b"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			store := prepare(t, c.cacheOwner)
			if err := clearForeignState(ctx, Dependencies{Store: store}, c.hasPrevious, current, c.next); err != nil {
				t.Fatal(err)
			}
			for _, name := range identityFiles {
				if _, err := os.Stat(filepath.Join(store.Dir, name)); os.IsNotExist(err) != c.cleared {
					t.Fatalf("%s 清理结果应为 %v: %v", name, c.cleared, err)
				}
			}
			if loaded, err := store.Load(ctx); err != nil || loaded != current {
				t.Fatalf("凭据或本机密钥被清理: %v %v", loaded, err)
			}
		})
	}
}
