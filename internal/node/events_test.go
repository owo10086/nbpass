package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"nbpass/internal/eventlog"
	"nbpass/internal/platform"
)

func queueOnDisk(t *testing.T, limits EventLimits) (*EventQueue, FileStore) {
	t.Helper()
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	q, err := OpenEventQueue(context.Background(), store, platform.RealClock{}, rand.Reader, limits)
	if err != nil {
		t.Fatal(err)
	}
	return q, store
}

// 字节上限独立于数量上限生效；密文原子替换后仍有最小权限，重开不丢缺口。
func TestEventQueueByteBoundEncryptedPersistenceAndFrozenGaps(t *testing.T) {
	ctx := context.Background()
	q, store := queueOnDisk(t, EventLimits{Count: 10, Bytes: 4096})
	message := "private-event-" + strings.Repeat("x", 2000)
	for range 3 {
		if err := q.Record(ctx, "sync_failed", message, 3); err != nil {
			t.Fatal(err)
		}
	}
	q, err := OpenEventQueue(ctx, store, platform.RealClock{}, rand.Reader, EventLimits{Count: 10, Bytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := q.Batch(ctx)
	if err != nil || len(batch) != 2 || batch[0].Gap == nil || batch[0].Gap.Count != 2 {
		t.Fatalf("字节限额或持久缺口错误: %+v %v", batch, err)
	}
	frozenID := batch[0].ID
	for range 3 {
		if err := q.Record(ctx, "sync_failed", message, 4); err != nil {
			t.Fatal(err)
		}
	}
	retry, err := q.Batch(ctx)
	if err != nil || retry[0].ID != frozenID || retry[0].Gap.Count != 2 {
		t.Fatalf("已发送缺口被修改: %+v %v", retry, err)
	}
	if err := q.Ack(ctx, []string{batch[0].ID, batch[1].ID}); err != nil {
		t.Fatal(err)
	}
	remaining, err := q.Batch(ctx)
	if err != nil || len(remaining) != 2 || remaining[0].Gap == nil || remaining[0].Gap.Count != 2 || remaining[0].ID == frozenID {
		t.Fatalf("确认旧批次误删新增缺口: %+v %v", remaining, err)
	}
	path := filepath.Join(store.Dir, "events.enc")
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte("private-event-")) {
		t.Fatal("事件文件包含明文或不可读")
	}
	for name, mode := range map[string]os.FileMode{store.Dir: 0700, path: 0600, filepath.Join(store.Dir, "node.key"): 0600} {
		info, err := os.Lstat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("事件缓存权限: %s %v %v", name, info, err)
		}
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenEventQueue(ctx, store, platform.RealClock{}, rand.Reader, EventLimits{}); err == nil {
		t.Fatal("损坏密文被接受或清空")
	}
}

type failedEventDisk struct {
	FileStore
	fail bool
}

func (s *failedEventDisk) SaveEvents(ctx context.Context, data []byte) error {
	if s.fail {
		return errors.New("disk full")
	}
	return s.FileStore.SaveEvents(ctx, data)
}

func TestEventQueueWriteAndAckFailurePreserveDurableState(t *testing.T) {
	ctx := context.Background()
	_, disk := queueOnDisk(t, EventLimits{})
	store := &failedEventDisk{FileStore: disk}
	q, err := OpenEventQueue(ctx, store, platform.RealClock{}, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Record(ctx, "sync_failed", "保留我", 1); err != nil {
		t.Fatal(err)
	}
	before, err := q.Batch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if err := q.Record(ctx, "sync_failed", "不能假装成功", 2); err == nil {
		t.Fatal("写失败未返回")
	}
	if err := q.Ack(ctx, []string{before[0].ID}); err == nil {
		t.Fatal("确认写失败未返回")
	}
	store.fail = false
	after, err := q.Batch(ctx)
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("写失败改变内存状态: %v %v", after, err)
	}
	q, err = OpenEventQueue(ctx, store, platform.RealClock{}, rand.Reader, EventLimits{})
	if err != nil {
		t.Fatal(err)
	}
	after, err = q.Batch(ctx)
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatal("写失败改变磁盘状态")
	}
}

func TestEventQueueConcurrentProducersKeepEveryEventOnce(t *testing.T) {
	ctx := context.Background()
	q, _ := queueOnDisk(t, EventLimits{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 10 {
				if err := q.Record(ctx, "sync_failed", "并发事件", 1); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for {
		batch, err := q.Batch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) == 0 {
			break
		}
		if len(batch) > eventlog.MaxBatch {
			t.Fatal("超过控制消息批次上限")
		}
		ids := []string{}
		for _, e := range batch {
			if seen[e.ID] {
				t.Fatal("并发事件 ID 重复")
			}
			seen[e.ID] = true
			ids = append(ids, e.ID)
		}
		if err := q.Ack(ctx, ids); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 40 {
		t.Fatalf("并发写事件丢失: %d", len(seen))
	}
}

func TestEventQueueAckOfEvictedInflightEventCancelsPendingGap(t *testing.T) {
	ctx := context.Background()
	q, _ := queueOnDisk(t, EventLimits{Count: 1})
	if err := q.Record(ctx, "sync_failed", "A", 1); err != nil {
		t.Fatal(err)
	}
	batch, err := q.Batch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Record(ctx, "sync_failed", "B", 2); err != nil {
		t.Fatal(err)
	}
	if err := q.Ack(ctx, []string{batch[0].ID}); err != nil {
		t.Fatal(err)
	}
	remaining, err := q.Batch(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].Message != "B" {
		t.Fatalf("已确认事件不应产生缺口: %+v %v", remaining, err)
	}
}
