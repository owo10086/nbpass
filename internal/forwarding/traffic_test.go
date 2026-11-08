package forwarding

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

// 撤销后只保留历史业务字节，不再持续生成空桶占用离线队列。
func TestTrafficRemovedRulesStopCreatingNewBuckets(t *testing.T) {
	now := time.Now().UTC()
	e := NewEngine(Dependencies{Now: func() time.Time { return now }, Listen: func(string, string) (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }})
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 1, Rules: []*controlpb.ForwardRule{{Id: "r", EntryGroupId: "g", ListenPort: 12345, TargetAddress: "8.8.8.8", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, Kind: controlpb.RuleKind_RULE_KIND_DIRECT}}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	if len(e.Traffic()) != 1 {
		t.Fatal("启用规则未生成当前连接样本")
	}
	s = &controlpb.ConfigSnapshot{Revision: 2}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	for _, sample := range e.Traffic() {
		if sample.BucketUnix == now.Truncate(time.Minute).Unix() {
			t.Fatal("已撤销规则仍在持续生成空流量桶")
		}
	}
}

// 分钟切换时快照时间必须与字节桶在同一个锁内读取，否则合法桶会被面板判为未来。
func TestTrafficSnapshotTimestampAndBucketAreConsistent(t *testing.T) {
	minute := time.Now().UTC().Truncate(time.Minute)
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	e := NewEngine(Dependencies{Now: func() time.Time {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return minute.Add(-time.Nanosecond)
		}
		return minute
	}})
	e.traffic.bytes = map[trafficKey]trafficBytes{}
	done := make(chan []*controlpb.RuleTraffic, 1)
	go func() { done <- e.Traffic() }()
	<-entered
	go func() {
		e.traffic.mu.Lock()
		e.traffic.bytes[trafficKey{"r", minute.Unix()}] = trafficBytes{up: 4}
		e.traffic.mu.Unlock()
		close(written)
	}()
	select {
	case <-written:
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for _, sample := range <-done {
		at, err := time.Parse(time.RFC3339Nano, sample.SampledAt)
		if err != nil || time.Unix(sample.BucketUnix, 0).After(at) {
			t.Fatalf("合法字节桶晚于快照时间: %v", sample)
		}
	}
	<-written
}
