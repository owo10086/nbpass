package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"nbpass/internal/rotation"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/platform"
	"nbpass/internal/timing"
)

type oneShotRotationDisk struct {
	FileStore
	mu        sync.Mutex
	failState string
	failed    bool
}

func (s *oneShotRotationDisk) SaveRotation(ctx context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var state rotationJournal
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if !s.failed && state.Results[state.Current].State == s.failState {
		s.failed = true
		return errors.New("temporary disk failure")
	}
	return s.FileStore.SaveRotation(ctx, data)
}

// 物理故障注入：观察进度/终态的一次临时写失败不能使执行器永久停止。
func TestRotationObservationRetriesTransientPersistenceFailure(t *testing.T) {
	for _, state := range []string{"observing", "changed", "unconfirmed"} {
		t.Run(state, func(t *testing.T) {
			disk := &oneShotRotationDisk{FileStore: FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}, failState: state}
			clock := fastRotationClock{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			query := func(context.Context) (string, error) {
				if state == "changed" {
					return "1.1.1.1", nil
				}
				return "8.8.8.8", nil
			}
			r, err := openRotations(ctx, Dependencies{Clock: clock, RotationStore: disk, RotationIPv4Sources: [2]func(context.Context) (string, error){query, query}})
			if err != nil {
				t.Fatal(err)
			}
			p := &controlpb.RotationProgress{ActionId: "action", OldIp: "8.8.8.8", State: "started", Reason: "started"}
			if err := r.save(p, 0); err != nil {
				t.Fatal(err)
			}
			r.observe(p)
			result := r.snapshot()
			expected := "unconfirmed"
			if state == "changed" {
				expected = "changed"
			}
			if result.State != expected {
				t.Fatalf("临时写失败导致停止: %v", result)
			}
			data, err := disk.LoadRotation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) == 0 {
				t.Fatal("结果未持久化")
			}
		})
	}
}

type fastRotationClock struct{}

func (fastRotationClock) Now() time.Time { return time.Now() }
func (fastRotationClock) NewTimer(time.Duration) timing.Timer {
	return platform.RealClock{}.NewTimer(time.Millisecond)
}

func TestRotationJournalUsesEncryptedRestrictedFile(t *testing.T) {
	s := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	ctx := context.Background()
	if err := s.SaveRotation(ctx, []byte(`{"current":"private-action-marker","results":{}}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, "rotation.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private-action-marker")) {
		t.Fatal("动作缓存明文落盘")
	}
	info, err := os.Stat(filepath.Join(s.Dir, "rotation.enc"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("动作缓存权限无效")
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(filepath.Join(s.Dir, "rotation.enc"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadRotation(ctx); err == nil {
		t.Fatal("接受损坏的动作密文")
	}
}

func TestRotationHistoryCompactionRejectsOldPermitsWithoutRepeatingHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	disk := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	clock := fixedRotationClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	var calls int
	query := func(context.Context) (string, error) { return "8.8.8.8", nil }
	r, err := openRotations(ctx, Dependencies{Clock: clock, RotationStore: disk, RotationIPv4Sources: [2]func(context.Context) (string, error){query, query}, RotationLocalIPv4: func(context.Context) ([]string, error) { return []string{"8.8.8.8"}, nil }, RotationHTTP: func(context.Context, rotation.Config, rotation.Variables) error { calls++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4096; i++ {
		id := fmt.Sprint(i)
		r.state.Results[id] = &controlpb.RotationProgress{ActionId: id, OldIp: "8.8.8.8", State: "changed"}
	}
	if err := r.save(&controlpb.RotationProgress{ActionId: "new", OldIp: "8.8.8.8", State: "changed"}, 0); err != nil {
		t.Fatal(err)
	}
	if len(r.state.Results) > 4096 {
		t.Fatal("历史未压缩")
	}
	// 压缩水位与旧许可发行时间相等时也不能重放，即使原许可尚未过期。
	r.execute(ctx, &controlpb.RotationCommand{ActionId: "0", NodeId: "n", OldIp: "8.8.8.8", IssuedAt: clock.Now().UnixNano(), ExpiresAt: clock.Now().Add(time.Minute).UnixNano(), ConfigJson: []byte(`{"url":"https://vendor.example/","method":"POST"}`)})
	if calls != 0 || r.snapshot().State != "cancelled" {
		t.Fatal("压缩后重放了旧许可")
	}
}

type fixedRotationClock struct{ now time.Time }

func (c fixedRotationClock) Now() time.Time { return c.now }
func (fixedRotationClock) NewTimer(d time.Duration) timing.Timer {
	return platform.RealClock{}.NewTimer(d)
}
