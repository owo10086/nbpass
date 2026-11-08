package forwarding

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

// 新直连生命周期必须统计准入后的连接；预读只在目标成功写入后计量。
func TestDirectTrafficPreDialAdmissionAndFailedDial(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "成功"
		if failed {
			name = "拨号失败"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			client, ingress := net.Pipe()
			target, backend := net.Pipe()
			started, release := make(chan struct{}), make(chan struct{})
			now := time.Now()
			e := NewEngine(Dependencies{Now: func() time.Time { return now }, Limits: ResourceLimits{MaxConnections: 1}, DialTarget: func(ctx context.Context, _ string) (net.Conn, error) {
				close(started)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
				}
				if failed {
					return nil, errors.New("拒绝拨号")
				}
				return target, nil
			}})
			r := &listenerRule{engine: e, ctx: ctx, config: &controlpb.ForwardRule{Id: "r", TargetAddress: "8.8.8.8", TargetPort: 443}, connections: map[net.Conn]struct{}{}}
			e.registerTrafficRule("r")
			t.Cleanup(func() {
				cancel()
				client.Close()
				ingress.Close()
				target.Close()
				backend.Close()
				r.wg.Wait()
			})
			r.acceptDirect(ingress)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("未开始拨号")
			}
			client.SetDeadline(time.Now().Add(3 * time.Second))
			backend.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := client.Write([]byte("pre-dial")); err != nil {
				t.Fatal(err)
			}
			assertTraffic := func(up, down, active uint64) {
				t.Helper()
				samples := e.Traffic()
				if len(samples) != 1 || samples[0].UploadBytes != up || samples[0].DownloadBytes != down || samples[0].ActiveConnections != active {
					t.Fatalf("流量期望 %d/%d/%d，得到 %v", up, down, active, samples)
				}
			}
			assertTraffic(0, 0, 1)
			denied, incoming := net.Pipe()
			defer denied.Close()
			r.acceptDirect(incoming)
			if _, err := denied.Read(make([]byte, 1)); err == nil {
				t.Fatal("超限连接未关闭")
			}
			assertTraffic(0, 0, 1)
			close(release)
			if failed {
				r.wg.Wait()
				assertTraffic(0, 0, 0)
				return
			}
			got := make([]byte, 8)
			if _, err := io.ReadFull(backend, got); err != nil || string(got) != "pre-dial" {
				t.Fatalf("预读字节丢失: %q %v", got, err)
			}
			written := make(chan error, 1)
			go func() { _, err := backend.Write([]byte("reply")); written <- err }()
			got = make([]byte, 5)
			if _, err := io.ReadFull(client, got); err != nil || string(got) != "reply" {
				t.Fatalf("回复字节丢失: %q %v", got, err)
			}
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			cancel()
			r.wg.Wait()
			assertTraffic(8, 5, 0)
		})
	}
}

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
