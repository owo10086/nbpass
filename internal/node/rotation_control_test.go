package node

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/rotation"
)

// 物理重放边界：解除只释放节点锁，原许可在落盘、重建后仍不能再次执行。
func TestRotationReleasedActionRetainsReplayProtectionAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	disk := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	clock := fixedRotationClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	var calls atomic.Int32
	deps := Dependencies{Clock: clock, RotationStore: disk, RotationHTTP: func(context.Context, rotation.Config, rotation.Variables) error { calls.Add(1); return nil }}
	r, err := openRotations(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.save(&controlpb.RotationProgress{ActionId: "a", OldIp: "8.8.8.8", State: "unconfirmed", Reason: "unconfirmed", Attempts: 5, ObservationComplete: true}, 0); err != nil {
		t.Fatal(err)
	}
	r.submit(ctx, "n", &controlpb.RotationCommand{NodeId: "n", ActionId: "a", OldIp: "8.8.8.8", Operation: "release", ControlSequence: 1})
	r.wg.Wait()
	for range 2 {
		r.submit(ctx, "n", &controlpb.RotationCommand{NodeId: "n", ActionId: "a", OldIp: "8.8.8.8", IssuedAt: clock.Now().UnixNano(), ExpiresAt: clock.Now().Add(time.Minute).UnixNano(), ConfigJson: []byte(`{"url":"https://vendor.example/","method":"POST"}`)})
		r.wg.Wait()
		if p := r.snapshot(); p.State != "released" || p.Attempts != 5 || p.ControlSequence != 1 || calls.Load() != 0 {
			t.Fatalf("解除后重放了原许可: %v 调用%d", p, calls.Load())
		}
		r, err = openRotations(ctx, deps)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRotationLegacyOfflineCompletionCanBeReconciled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	disk := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	// 票28原始JSON没有新字段；模拟离线时已经完成五次观察但面板尚未收到。
	if err := disk.SaveRotation(ctx, []byte(`{"current":"a","results":{"a":{"action_id":"a","old_ip":"8.8.8.8","state":"unconfirmed","reason":"unconfirmed","sequence":7,"attempts":5}}}`)); err != nil {
		t.Fatal(err)
	}
	clock := fixedRotationClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	query := func(context.Context) (string, error) { return "1.1.1.1", nil }
	deps := Dependencies{Clock: clock, RotationStore: disk, RotationIPv4Sources: [2]func(context.Context) (string, error){query, query}}
	r, err := openRotations(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	if p := r.snapshot(); !p.ObservationComplete || p.Sequence <= 7 || p.Attempts != 5 {
		t.Fatalf("旧缓存补报无法开放核对/解除: %v", p)
	}
	r, err = openRotations(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	r.submit(ctx, "n", &controlpb.RotationCommand{NodeId: "n", ActionId: "a", OldIp: "8.8.8.8", Operation: "reconcile", ControlSequence: 1})
	r.wg.Wait()
	if p := r.snapshot(); p.State != "changed" || p.NewIp != "1.1.1.1" || p.Attempts != 5 {
		t.Fatalf("旧缓存无法只读核对: %v", p)
	}
}
