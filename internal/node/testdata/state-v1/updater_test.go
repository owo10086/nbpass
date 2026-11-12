package nodeupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"nbpass/internal/controlpb"
)

func TestActualOldUpdaterRejectsWriterTwo(t *testing.T) {
	dir := realTemp(t)
	path := filepath.Join(dir, "nbpass-node")
	if err := os.WriteFile(path, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("new")
	m := manifest(data)
	installer := &FixedInstaller{Path: path, allowedDir: dir,
		Restart: func(context.Context) error { t.Fatal("旧更新器切换了格式 2 候选"); return nil },
		Healthy: func(context.Context) error { return nil },
		Check: func(context.Context, string) (Identity, error) {
			return Identity{Version: m.Version, Platform: m.Platform, Compatibility: &controlpb.Compatibility{StateMin: 1, StateMax: 2, StateWrite: 2}}, nil
		},
	}
	if err := installer.Apply(context.Background(), m, data, func(string) {}); err == nil {
		t.Fatal("真实旧更新器没有拒绝跨格式候选")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "old" {
		t.Fatal("拒绝后改变了旧程序", err)
	}
}
