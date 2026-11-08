package node

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"nbpass/internal/timing"
)

const trafficInterval = 5 * time.Second
const trafficBatch = 16

type TrafficStore interface {
	LoadTraffic(context.Context) ([]byte, error)
	SaveTraffic(context.Context, []byte) error
}
type trafficEntry struct {
	Sample *controlpb.RuleTraffic `json:"sample"`
	Acked  bool                   `json:"acked"`
}
type trafficState struct {
	Sequence uint64                  `json:"sequence"`
	Entries  map[string]trafficEntry `json:"entries"`
}
type TrafficQueue struct {
	mu        sync.Mutex
	store     TrafficStore
	clock     timing.Clock
	state     trafficState
	ready     trafficReady
	sequences map[uint64]string
}

func trafficKey(s *controlpb.RuleTraffic) string {
	return s.ProcessInstance + ":" + s.RuleId + ":" + strconv.FormatInt(s.BucketUnix, 10)
}
func trafficAckKey(instance string, sequence uint64) string {
	return instance + ":" + strconv.FormatUint(sequence, 10)
}
func OpenTrafficQueue(ctx context.Context, store TrafficStore, clock timing.Clock) (*TrafficQueue, error) {
	q := &TrafficQueue{store: store, clock: clock, state: trafficState{Entries: map[string]trafficEntry{}}}
	if store == nil {
		return q, nil
	}
	raw, err := store.LoadTraffic(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return nil, err
	}
	if q.state, err = decodeTraffic(raw); err != nil {
		return nil, err
	}
	if q.state.Entries == nil {
		return nil, errors.New("流量缓存损坏")
	}
	for key, e := range q.state.Entries {
		if e.Sample == nil || key != trafficKey(e.Sample) || e.Sample.Sequence == 0 || e.Sample.Sequence > q.state.Sequence {
			return nil, errors.New("流量缓存损坏")
		}
	}
	q.indexReady()
	return q, nil
}
func (q *TrafficQueue) candidate() trafficState {
	s := trafficState{Sequence: q.state.Sequence, Entries: make(map[string]trafficEntry, len(q.state.Entries))}
	minute := q.clock.Now().UTC().Truncate(time.Minute).Unix()
	for k, e := range q.state.Entries {
		// 已确认的闭合桶不再重写；历史零桶不携带任何业务字节或实时价值。
		if e.Sample.BucketUnix < minute && (e.Acked || e.Sample.UploadBytes == 0 && e.Sample.DownloadBytes == 0) {
			continue
		}
		if !time.Unix(e.Sample.BucketUnix, 0).Before(q.clock.Now().Add(-24 * time.Hour)) {
			s.Entries[k] = e
		}
	}
	return s
}
func (q *TrafficQueue) commit(ctx context.Context, s trafficState) error {
	if q.store != nil {
		raw, err := encodeTraffic(s)
		if err != nil {
			return err
		}
		if err = q.store.SaveTraffic(ctx, raw); err != nil {
			return err
		}
	}
	q.state = s
	q.indexReady()
	return nil
}

// Capture 合并桶内累计值；在途副本保持不变，旧 ACK 不会删掉更新后的累计值。
func (q *TrafficQueue) Capture(ctx context.Context, instance string, samples []*controlpb.RuleTraffic) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.candidate()
	minute := q.clock.Now().UTC().Truncate(time.Minute).Unix()
	for _, wire := range samples {
		v := proto.Clone(wire).(*controlpb.RuleTraffic)
		v.ProcessInstance = instance
		if time.Unix(v.BucketUnix, 0).Before(q.clock.Now().Add(-24 * time.Hour)) {
			continue
		}
		if v.BucketUnix < minute && v.UploadBytes == 0 && v.DownloadBytes == 0 {
			continue
		}
		key := trafficKey(v)
		old, exists := s.Entries[key]
		if exists && old.Sample.UploadBytes == v.UploadBytes && old.Sample.DownloadBytes == v.DownloadBytes &&
			(v.BucketUnix != minute || old.Sample.ActiveConnections == v.ActiveConnections && old.Sample.SampledAt == v.SampledAt) {
			continue
		}
		s.Sequence++
		v.Sequence = s.Sequence
		s.Entries[key] = trafficEntry{Sample: v}
	}
	return q.commit(ctx, s)
}

// ACK 只更新内存；下次采样统一持久化。崩溃可重投，面板累计游标持久去重。
// 避免每个补报批次对整个24小时积压再次加密和同步磁盘。
func (q *TrafficQueue) Ack(_ context.Context, acks []*controlpb.TrafficAck) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, a := range acks {
		key, ok := q.sequences[a.Sequence]
		if !ok {
			continue
		}
		e := q.state.Entries[key]
		if e.Sample.ProcessInstance != a.ProcessInstance || e.Sample.Sequence != a.Sequence {
			continue
		}
		e.Acked = true
		q.state.Entries[key] = e
		delete(q.sequences, a.Sequence)
	}
	return nil
}

func sampleTraffic(ctx context.Context, q *TrafficQueue, instance string, engine *forwarding.Engine, updates chan struct{}, done chan struct{}) {
	defer close(done)
	for {
		timer := q.clock.NewTimer(trafficInterval)
		samples := engine.Traffic()
		if q.Capture(ctx, instance, samples) == nil {
			engine.ForgetTraffic(samples)
			select {
			case updates <- struct{}{}:
			default:
			}
		}
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
	}
}
