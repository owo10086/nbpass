package clusterharness

import (
	"context"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
	"os"
	"path/filepath"
)

// Snapshot 仅操作测试环境磁盘；业务验收仍通过 HTTP API。
func (h *Harness) Snapshot(path string) error {
	return panel.BackupDatabase(context.Background(), h.Dependencies.Persistence, path)
}
func (h *Harness) RestoreSnapshot(path string) error {
	h.stop()
	target := filepath.Join(h.Dir, "panel.db")
	for _, p := range []string{target, target + "-wal", target + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := panel.BackupDatabase(context.Background(), platform.SQLite{Path: path}, target); err != nil {
		return err
	}
	if err := panel.PrepareRestore(context.Background(), h.Dependencies.Persistence, h.Clock.Now()); err != nil {
		return err
	}
	return h.start()
}
