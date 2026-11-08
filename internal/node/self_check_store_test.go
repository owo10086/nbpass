package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"nbpass/internal/selfcheck"
)

// 检查加密/权限/损坏拒绝，以及确认写失败不能提前消费队列的磁盘契约。
func TestSelfCheckFileStoreAndFailedAcknowledgement(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "node")
	disk := FileStore{Dir: dir, Random: rand.Reader}
	store := &failingSelfCheckStore{FileStore: disk}
	r := &selfCheckRunner{deps: Dependencies{SelfCheckStore: store}}
	journal := selfCheckJournal{Sequence: 4, Pending: []selfcheck.Round{{ID: "self-check-private-evidence-marker", Sequence: 4}}}
	if err := r.persist(ctx, journal); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "self-check.enc")
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte(journal.Pending[0].ID)) {
		t.Fatalf("自测明文落盘: %v", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("自测缓存权限错误")
	}
	store.fail = true
	if err := r.ack(ctx, []string{journal.Pending[0].ID}); err == nil {
		t.Fatal("确认写失败未报告错误")
	}
	if len(r.batch()) != 1 {
		t.Fatal("确认写失败提前消费了内存队列")
	}
	restored, err := disk.LoadSelfChecks(ctx)
	if err != nil || !bytes.Contains(restored, []byte(journal.Pending[0].ID)) {
		t.Fatal("确认写失败丢失磁盘队列", err)
	}
	store.fail = false
	if err := r.ack(ctx, []string{journal.Pending[0].ID}); err != nil {
		t.Fatal(err)
	}
	if len(r.batch()) != 0 {
		t.Fatal("确认未消费队列")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.LoadSelfChecks(ctx); err == nil {
		t.Fatal("接受宽松权限")
	}
	if err := disk.SaveSelfChecks(ctx, []byte("x")); err == nil {
		t.Fatal("覆盖不安全文件")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	data[0] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.LoadSelfChecks(ctx); err == nil {
		t.Fatal("缓存损坏未拒绝")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.LoadSelfChecks(ctx); err == nil {
		t.Fatal("读取了符号链接")
	}
	if err := disk.SaveSelfChecks(ctx, []byte("x")); err == nil {
		t.Fatal("覆盖了符号链接")
	}
}

type failingSelfCheckStore struct {
	FileStore
	fail bool
}

func (s *failingSelfCheckStore) SaveSelfChecks(ctx context.Context, data []byte) error {
	if s.fail {
		return errors.New("假自测缓存写失败")
	}
	return s.FileStore.SaveSelfChecks(ctx, data)
}
