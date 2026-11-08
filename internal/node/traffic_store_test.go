package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/platform"
)

type trafficDiskClock struct {
	platform.RealClock
	at time.Time
}

func (c trafficDiskClock) Now() time.Time { return c.at }

// 验证合法的全天积压容量、加密文件与重开，避免硬限使后续采样永久停报。
func TestTrafficDiskHolds510RulesForTwentyFourHours(t *testing.T) {
	ctx := context.Background()
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	// 容量/排空独立于时间推进；race慢速运行不能让最旧分钟在断言前过期。
	clock := trafficDiskClock{at: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
	q, err := OpenTrafficQueue(ctx, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	now := clock.Now().UTC()
	minute := now.Truncate(time.Minute)
	samples := make([]*controlpb.RuleTraffic, 0, 510*1440)
	for rule := range 510 {
		for m := range 1440 {
			samples = append(samples, &controlpb.RuleTraffic{RuleId: fmt.Sprintf("%043d", rule), BucketUnix: minute.Add(-time.Duration(m) * time.Minute).Unix(), UploadBytes: 1<<63 - 1, DownloadBytes: 1<<63 - 1, SampledAt: now.Format(time.RFC3339Nano)})
		}
	}
	if err := q.Capture(ctx, strings.Repeat("p", 43), samples); err != nil {
		t.Fatalf("合法24小时积压无法保存: %v", err)
	}
	reopened, err := OpenTrafficQueue(ctx, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.state.Entries) != len(samples) {
		t.Fatalf("重开丢失积压: %d/%d", len(reopened.state.Entries), len(samples))
	}
	// 大积压的批次选择和ACK不应反复序列化全部历史；最新分钟优先恢复实时值。
	first := reopened.Batch()
	if len(first) != trafficBatch || first[0].BucketUnix != minute.Unix() {
		t.Fatalf("实时样本被历史积压阻塞: %v", first)
	}
	seen := 0
	for batch := reopened.Batch(); len(batch) > 0; batch = reopened.Batch() {
		acks := make([]*controlpb.TrafficAck, 0, len(batch))
		for _, v := range batch {
			acks = append(acks, &controlpb.TrafficAck{ProcessInstance: v.ProcessInstance, Sequence: v.Sequence})
		}
		if err := reopened.Ack(ctx, acks); err != nil {
			t.Fatal(err)
		}
		seen += len(batch)
	}
	if seen != len(samples) {
		t.Fatalf("积压排空丢桶: %d/%d", seen, len(samples))
	}
	if err := reopened.Capture(ctx, strings.Repeat("p", 43), nil); err != nil {
		t.Fatal(err)
	}
	compacted, err := OpenTrafficQueue(ctx, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted.state.Entries) > 510 || len(compacted.Batch()) != 0 {
		t.Fatal("已确认的历史积压未在检查点回收")
	}
	path := filepath.Join(store.Dir, "traffic.enc")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(strings.Repeat("p", 43))) {
		t.Fatal("流量文件含明文实例标识")
	}
	for name, mode := range map[string]os.FileMode{store.Dir: 0700, path: 0600, filepath.Join(store.Dir, "node.key"): 0600} {
		info, err := os.Lstat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("流量缓存权限错误: %s %v", name, err)
		}
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenTrafficQueue(ctx, store, clock); err == nil {
		t.Fatal("损坏密文被接受或静默清空")
	}
}

func TestTrafficOldAckCannotRemoveNewerDurableCounters(t *testing.T) {
	ctx := context.Background()
	clock := platform.RealClock{}
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	q, err := OpenTrafficQueue(ctx, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	s := &controlpb.RuleTraffic{RuleId: "r", BucketUnix: clock.Now().Truncate(time.Minute).Unix(), UploadBytes: 4, SampledAt: clock.Now().Format(time.RFC3339Nano)}
	if err := q.Capture(ctx, "process", []*controlpb.RuleTraffic{s}); err != nil {
		t.Fatal(err)
	}
	old := q.Batch()[0]
	s.UploadBytes = 8
	if err := q.Capture(ctx, "process", []*controlpb.RuleTraffic{s}); err != nil {
		t.Fatal(err)
	}
	if err := q.Ack(ctx, []*controlpb.TrafficAck{{ProcessInstance: old.ProcessInstance, Sequence: old.Sequence}}); err != nil {
		t.Fatal(err)
	}
	q, err = OpenTrafficQueue(ctx, store, clock)
	if err != nil {
		t.Fatal(err)
	}
	if batch := q.Batch(); len(batch) != 1 || batch[0].UploadBytes != 8 || batch[0].Sequence <= old.Sequence {
		t.Fatalf("旧确认删除了新累计值: %v", batch)
	}
}
