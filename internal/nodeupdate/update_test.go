package nodeupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func manifest(data []byte) Manifest {
	h := sha256.Sum256(data)
	return Manifest{TaskID: "task-1", Version: "0.1.0", Platform: Platform, Size: int64(len(data)), SHA256: hex.EncodeToString(h[:])}
}
func TestManifestBindingsAndDownloadIntegrity(t *testing.T) {
	data := []byte("candidate")
	good := manifest(data)
	for _, field := range []string{"task", "version", "platform", "size", "hash", "body"} {
		t.Run(field, func(t *testing.T) {
			remote := good
			body := data
			switch field {
			case "task":
				remote.TaskID = "other"
			case "version":
				remote.Version = "other"
			case "platform":
				remote.Platform = "darwin/arm64"
			case "size":
				remote.Size++
			case "hash":
				remote.SHA256 = string(bytes.Repeat([]byte("a"), 64))
			case "body":
				body = []byte("tampered!")
			}
			installed := false
			err := Execute(context.Background(), good, SourceFuncs{ManifestFunc: func(context.Context, string) (Manifest, error) { return remote, nil }, ArtifactFunc: func(context.Context, string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }}, func(context.Context, Manifest, []byte, func(string)) error { installed = true; return nil }, func(string) {})
			if err == nil || installed {
				t.Fatal("清单不匹配仍安装")
			}
		})
	}
}

func TestFixedInstallerRejectsPathsAndSymlinks(t *testing.T) {
	for _, kind := range []string{"other_path", "binary_link", "parent_link", "staging_link"} {
		t.Run(kind, func(t *testing.T) {
			root := realTemp(t)
			dir := filepath.Join(root, "bin")
			os.Mkdir(dir, 0700)
			path := filepath.Join(dir, "nbpass-node")
			os.WriteFile(path, []byte("old"), 0700)
			switch kind {
			case "other_path":
				path = filepath.Join(dir, "other")
			case "binary_link":
				os.Remove(path)
				os.Symlink("target", path)
			case "parent_link":
				os.Rename(dir, dir+"-real")
				os.Symlink(dir+"-real", dir)
			case "staging_link":
				os.Symlink(t.TempDir(), filepath.Join(dir, ".nbpass-update"))
			}
			installer := &FixedInstaller{Path: path, allowedDir: dir, Check: func(context.Context, string) (Identity, error) { return Identity{}, nil }}
			if err := installer.Apply(context.Background(), manifest([]byte("new")), []byte("new"), func(string) {}); err == nil {
				t.Fatal("不安全路径未被拒绝")
			}
		})
	}
}

func TestFixedInstallerRollsBackOnlyBinary(t *testing.T) {
	oldMask := syscall.Umask(0077)
	defer syscall.Umask(oldMask)
	for _, failure := range []string{"start", "business", "none"} {
		t.Run(failure, func(t *testing.T) {
			dir := realTemp(t)
			path := filepath.Join(dir, "nbpass-node")
			os.WriteFile(path, []byte("old"), 0700)
			// 模拟更新前已撤权、已有动作去重证据。回滚不得读取或恢复这些状态。
			state := filepath.Join(dir, "authorization-and-rotation")
			os.WriteFile(state, []byte("revoked;action-42"), 0600)
			data := []byte("new")
			m := manifest(data)
			starts := 0
			installer := &FixedInstaller{Path: path, allowedDir: dir,
				Check: func(_ context.Context, p string) (Identity, error) {
					return Identity{Version: m.Version, Platform: m.Platform}, nil
				},
				Restart: func(context.Context) error {
					starts++
					if failure == "start" && starts == 1 {
						return errors.New("start")
					}
					return nil
				},
				Healthy: func(context.Context) error {
					if failure == "business" {
						return errors.New("business")
					}
					return nil
				},
			}
			err := installer.Apply(context.Background(), m, data, func(string) {})
			got, _ := os.ReadFile(path)
			want := "new"
			if failure != "none" {
				want = "old"
				if !errors.Is(err, ErrRolledBack) || starts != 2 {
					t.Fatalf("未回滚: %v starts=%d", err, starts)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0755 {
				t.Fatalf("安装或回滚产物不可由节点用户执行: %o", info.Mode().Perm())
			}
			if string(got) != want {
				t.Fatalf("程序=%s", got)
			}
			s, _ := os.ReadFile(state)
			if string(s) != "revoked;action-42" {
				t.Fatal("回滚覆盖安全状态")
			}
		})
	}
}

func TestCandidateIdentityAndMutationAreRejectedBeforeSwitch(t *testing.T) {
	for _, kind := range []string{"version", "platform", "mutation", "state_format"} {
		t.Run(kind, func(t *testing.T) {
			dir := realTemp(t)
			path := filepath.Join(dir, "nbpass-node")
			os.WriteFile(path, []byte("old"), 0700)
			data := []byte("new")
			m := manifest(data)
			installer := &FixedInstaller{Path: path, allowedDir: dir, Restart: func(context.Context) error { return nil }, Healthy: func(context.Context) error { return nil }, Check: func(_ context.Context, p string) (Identity, error) {
				i := Identity{Version: m.Version, Platform: m.Platform}
				if kind == "version" {
					i.Version = "wrong"
				}
				if kind == "platform" {
					i.Platform = "wrong"
				}
				if kind == "mutation" {
					os.WriteFile(p, []byte("changed"), 0700)
				}
				if kind == "state_format" {
					// A writer of state format 2 would break the rollback binary's format-1 reader.
					raw, _ := json.Marshal(map[string]any{"version": m.Version, "platform": m.Platform, "compatibility": map[string]any{"control_min": 1, "control_max": 2, "tunnel_min": 1, "tunnel_max": 1, "state_min": 1, "state_max": 2, "state_write": 2}})
					json.Unmarshal(raw, &i)
				}
				return i, nil
			}}
			if err := installer.Apply(context.Background(), m, data, func(string) {}); err == nil {
				t.Fatal("候选自检后身份或内容变更未拒绝")
			}
			got, _ := os.ReadFile(path)
			if string(got) != "old" {
				t.Fatal("旧程序已改变")
			}
		})
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
