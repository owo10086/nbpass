package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteCreatesPrivateFilesAndKeepsWALOnReopen(t *testing.T) {
	for _, existingDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_directory=%t", existingDir), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if existingDir {
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "panel.db")
			for attempt := range 2 {
				db, err := (SQLite{Path: path}).Open(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				var mode string
				if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
					t.Fatal(err)
				}
				if mode != "wal" {
					t.Errorf("open %d: journal_mode = %q, want wal", attempt, mode)
				}
				if _, err := db.Exec("CREATE TABLE IF NOT EXISTS permission_probe (value INTEGER); INSERT INTO permission_probe VALUES (1)"); err != nil {
					t.Fatal(err)
				}
				assertDiskMode(t, dir, os.ModeDir|0700)
				for _, suffix := range []string{"", "-wal", "-shm"} {
					assertDiskMode(t, path+suffix, 0600)
				}
				info, err := os.Stat(path + "-wal")
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() == 0 {
					t.Fatal("write did not produce a nonempty WAL")
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				assertDiskMode(t, path, 0600)
			}
		})
	}
}

func TestLocalKeyFileCreatesPrivateStableKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "panel.key")
	file := LocalKeyFile{Path: path}
	key, err := file.LoadOrCreate(context.Background(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	assertDiskMode(t, dir, os.ModeDir|0700)
	assertDiskMode(t, path, 0600)
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != 32 || !bytes.Equal(onDisk, key) {
		t.Fatal("key file must contain the returned 32-byte key")
	}
	reloaded, err := file.LoadOrCreate(context.Background(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reloaded, key) {
		t.Fatal("reopening replaced the local key")
	}
}

func TestLocalKeyFileRejectsUnsafeFiles(t *testing.T) {
	tests := []struct {
		name   string
		mode   os.FileMode
		length int
		link   bool
	}{
		{name: "symlink", mode: 0600, length: 32, link: true},
		{name: "dangling_symlink", link: true},
		{name: "no_permissions", mode: 0000, length: 32},
		{name: "read_only", mode: 0400, length: 32},
		{name: "group_readable", mode: 0640, length: 32},
		{name: "world_readable", mode: 0644, length: 32},
		{name: "group_writable", mode: 0660, length: 32},
		{name: "executable", mode: 0700, length: 32},
		{name: "empty", mode: 0600, length: 0},
		{name: "short", mode: 0600, length: 31},
		{name: "long", mode: 0600, length: 33},
		{name: "oversized", mode: 0600, length: 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "panel.key")
			fixture := path
			if tt.link {
				fixture = path + ".target"
				if err := os.Symlink(fixture, path); err != nil {
					t.Fatal(err)
				}
			}
			if tt.name != "dangling_symlink" {
				if err := os.WriteFile(fixture, bytes.Repeat([]byte{0x5a}, tt.length), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(fixture, tt.mode); err != nil {
					t.Fatal(err)
				}
			}
			key, err := (LocalKeyFile{Path: path}).LoadOrCreate(context.Background(), rand.Reader)
			if err == nil || len(key) != 0 {
				t.Fatalf("unsafe key accepted: returned %d bytes, error %v", len(key), err)
			}
		})
	}
}

func assertDiskMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Errorf("%s: mode = %v, want %v", filepath.Base(path), info.Mode(), want)
	}
}
