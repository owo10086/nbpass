package node

import (
	"container/heap"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

type trafficReady []*controlpb.RuleTraffic

func (h trafficReady) Len() int { return len(h) }
func (h trafficReady) Less(i, j int) bool {
	// 当前连接数与最近流量优先，避免历史补报令实时值长期缺测。
	if h[i].BucketUnix != h[j].BucketUnix {
		return h[i].BucketUnix > h[j].BucketUnix
	}
	return h[i].Sequence < h[j].Sequence
}
func (h trafficReady) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *trafficReady) Push(v any)   { *h = append(*h, v.(*controlpb.RuleTraffic)) }
func (h *trafficReady) Pop() any {
	old := *h
	n := len(old)
	v := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return v
}

func (q *TrafficQueue) indexReady() {
	q.ready = make(trafficReady, 0, len(q.state.Entries))
	q.sequences = make(map[uint64]string, len(q.state.Entries))
	for key, e := range q.state.Entries {
		if !e.Acked {
			q.ready = append(q.ready, e.Sample)
			q.sequences[e.Sample.Sequence] = key
		}
	}
	heap.Init(&q.ready)
}

func (q *TrafficQueue) Batch() []*controlpb.RuleTraffic {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*controlpb.RuleTraffic, 0, trafficBatch)
	selected := make(trafficReady, 0, trafficBatch)
	for len(q.ready) > 0 && len(out) < trafficBatch {
		v := heap.Pop(&q.ready).(*controlpb.RuleTraffic)
		e, ok := q.state.Entries[trafficKey(v)]
		if !ok || e.Acked || e.Sample.Sequence != v.Sequence || time.Unix(v.BucketUnix, 0).Before(q.clock.Now().Add(-24*time.Hour)) {
			continue
		}
		selected = append(selected, v)
		out = append(out, proto.Clone(v).(*controlpb.RuleTraffic))
	}
	for _, v := range selected {
		heap.Push(&q.ready, v)
	}
	return out
}
