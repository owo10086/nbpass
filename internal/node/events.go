package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"sync"

	"nbpass/internal/eventlog"
	"nbpass/internal/forwarding"
	"nbpass/internal/timing"
)

type EventStore interface {
	LoadEvents(context.Context) ([]byte, error)
	SaveEvents(context.Context, []byte) error
}

type EventLimits struct{ Count, Bytes int }

type eventState struct {
	Events   []eventlog.Event          `json:"events"`
	Sending  []eventlog.Event          `json:"sending"`
	Pending  map[string]eventlog.Event `json:"pending"`
	Frozen   []eventlog.Event          `json:"frozen"`
	gapBytes int
	gapCount int
}

// EventQueue 先持久化再发布内存状态。Frozen 缺口发出后保持不变，新缺口单独合并。
type EventQueue struct {
	mu     sync.Mutex
	store  EventStore
	clock  timing.Clock
	random io.Reader
	limits EventLimits
	state  eventState
}

func OpenEventQueue(ctx context.Context, store EventStore, clock timing.Clock, random io.Reader, limits EventLimits) (*EventQueue, error) {
	if store == nil || clock == nil || random == nil {
		return nil, errors.New("事件队列依赖不完整")
	}
	if limits.Count == 0 {
		limits.Count = 4096
	}
	if limits.Bytes == 0 {
		limits.Bytes = 4 << 20
	}
	if limits.Count < 1 || limits.Bytes < 4096 || limits.Count > 4096 || limits.Bytes > 4<<20 {
		return nil, errors.New("事件队列容量无效")
	}
	q := &EventQueue{store: store, clock: clock, random: random, limits: limits}
	data, err := store.LoadEvents(ctx)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if len(data) > 8<<20 {
			return nil, errors.New("事件队列文件过大")
		}
		if err := json.Unmarshal(data, &q.state); err != nil {
			return nil, err
		}
		if len(q.state.Events) > 4096 || len(q.state.Sending) > eventlog.MaxBatch || len(q.state.Pending) > forwarding.ChainResourceScopeCount+2 || len(q.state.Frozen) > eventlog.MaxBatch {
			return nil, errors.New("事件队列损坏")
		}
		all := append(append(append([]eventlog.Event{}, q.state.Events...), q.state.Sending...), q.state.Frozen...)
		for _, e := range all {
			if err := e.Validate(); err != nil {
				return nil, err
			}
		}
		for reason, e := range q.state.Pending {
			if err := e.Validate(); err != nil || e.Gap == nil || reason != gapKey(e.Gap.Reason, e.Message) {
				return nil, errors.New("事件缺口损坏")
			}
		}
	}
	q.state = q.candidate()
	if q.state.gapBytes > forwarding.ChainResourceScopeBytes || q.state.gapCount > forwarding.ChainResourceScopeCount {
		return nil, errors.New("事件缺口容量无效")
	}
	return q, nil
}

func (q *EventQueue) candidate() eventState {
	s := q.state
	s.Events = append([]eventlog.Event{}, s.Events...)
	s.Sending = append([]eventlog.Event{}, s.Sending...)
	s.Frozen = append([]eventlog.Event{}, s.Frozen...)
	s.Pending = make(map[string]eventlog.Event, 2)
	for k, v := range q.state.Pending {
		s.Pending[k] = v
	}
	s.gapBytes, s.gapCount = 0, 0
	for _, e := range s.Pending {
		if _, ok := forwarding.DecodeChainResourceScope(e.Message); ok {
			s.gapBytes += resourceGapBytes(e)
			s.gapCount++
		}
	}
	for _, e := range s.Frozen {
		if _, ok := forwarding.DecodeChainResourceScope(e.Message); ok {
			s.gapBytes += resourceGapBytes(e)
			s.gapCount++
		}
	}
	return s
}

func (q *EventQueue) commit(ctx context.Context, s eventState) error {
	data, err := json.Marshal(s)
	if err == nil {
		err = q.store.SaveEvents(ctx, data)
	}
	if err == nil {
		q.state = s
	}
	return err
}

func (q *EventQueue) id() (string, error) {
	b := make([]byte, 24)
	if _, err := io.ReadFull(q.random, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (q *EventQueue) lost(s *eventState, e eventlog.Event, reason string) error {
	message := "节点本地事件存在缺口"
	var chain forwarding.ChainEvent
	if e.Type == "chain_flow" && json.Unmarshal([]byte(e.Message), &chain) == nil && chain.Observation.ResourceOrder != nil {
		scope := forwarding.ResourceScope(chain.Observation)
		if scope.Valid() {
			message = scope.Message()
		}
	}
	return q.mergeGap(s, eventlog.Gap{Count: 1, From: e.OccurredAt, To: e.OccurredAt, Reason: reason}, message)
}

// Reserve fixed space for ID/time/count fields, so a full budget never
// prevents updating an already retained scope's newest loss boundary.
func resourceGapBytes(e eventlog.Event) int { raw, _ := json.Marshal(e.Message); return len(raw) + 512 }
func gapKey(reason, message string) string {
	if scope, ok := forwarding.DecodeChainResourceScope(message); ok {
		return reason + ":" + scope.Message()
	}
	return reason
}
func (q *EventQueue) mergeGap(s *eventState, gap eventlog.Gap, message string) error {
	key := gapKey(gap.Reason, message)
	_, scoped := forwarding.DecodeChainResourceScope(message)
	g, ok := s.Pending[key]
	oldSize := 0
	if ok && scoped {
		oldSize = resourceGapBytes(g)
	}
	if !ok {
		id, err := q.id()
		if err != nil {
			return err
		}
		g = eventlog.Event{ID: id, Type: "event_gap", OccurredAt: q.clock.Now(), Message: message, Gap: &eventlog.Gap{From: gap.From, To: gap.To, Reason: gap.Reason}}
	}
	combined := *g.Gap
	combined.Count += gap.Count
	if gap.From.Before(combined.From) {
		combined.From = gap.From
	}
	if gap.To.After(combined.To) {
		combined.To = gap.To
	}
	g.Gap = &combined
	if scoped {
		size := resourceGapBytes(g)
		if (!ok && s.gapCount >= forwarding.ChainResourceScopeCount) || s.gapBytes-oldSize+size > forwarding.ChainResourceScopeBytes {
			return q.mergeGap(s, gap, "节点本地事件存在缺口（精细配置归属容量已满）")
		}
		s.gapBytes += size - oldSize
		if !ok {
			s.gapCount++
		}
	}
	s.Pending[key] = g
	return nil
}

// One durable transaction publishes all distinct origin candidates and any
// explicit upstream overflow. A failed save changes neither queue nor dedup.
func (q *EventQueue) recordChainBatch(ctx context.Context, events []eventlog.Event, gap *forwarding.ChainResourceGaps) error {
	if len(events) == 0 && gap == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.candidate()
	for _, event := range events {
		id, err := q.id()
		if err != nil {
			return err
		}
		event.ID = id
		if err := event.Validate(); err != nil {
			return err
		}
		s.Events = append(s.Events, event)
	}
	if gap != nil {
		for scope, loss := range gap.Scopes {
			if err := q.mergeGap(&s, loss, scope.Message()); err != nil {
				return err
			}
		}
		if gap.Unscoped.Count > 0 {
			if err := q.mergeGap(&s, gap.Unscoped, "节点本地事件存在缺口（精细配置归属不可用）"); err != nil {
				return err
			}
		}
	}
	if err := q.trim(&s); err != nil {
		return err
	}
	return q.commit(ctx, s)
}

func (q *EventQueue) trim(s *eventState) error {
	kept := s.Events[:0]
	for _, e := range s.Events {
		if e.OccurredAt.Before(q.clock.Now().Add(-eventlog.BackfillWindow)) {
			if err := q.lost(s, e, "expired"); err != nil {
				return err
			}
		} else {
			kept = append(kept, e)
		}
	}
	s.Events = kept
	bytes := 0
	for _, e := range s.Events {
		data, _ := json.Marshal(e)
		bytes += len(data)
	}
	for len(s.Events) > q.limits.Count || bytes > q.limits.Bytes {
		e := s.Events[0]
		data, _ := json.Marshal(e)
		bytes -= len(data)
		if err := q.lost(s, e, "overflow"); err != nil {
			return err
		}
		s.Events = s.Events[1:]
	}
	return nil
}

// Record 供同步、出口切换、自测等生产者调用；禁止传入凭据和原始请求内容。
func (q *EventQueue) Record(ctx context.Context, kind, message string, revision uint64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	id, err := q.id()
	if err != nil {
		return err
	}
	e := eventlog.Event{ID: id, Type: kind, OccurredAt: q.clock.Now(), ConfigRevision: revision, Message: message}
	if err := e.Validate(); err != nil {
		return err
	}
	s := q.candidate()
	s.Events = append(s.Events, e)
	if err := q.trim(&s); err != nil {
		return err
	}
	return q.commit(ctx, s)
}

func (q *EventQueue) Batch(ctx context.Context) ([]eventlog.Event, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.candidate()
	if err := q.trim(&s); err != nil {
		return nil, err
	}
	// 单独保留一个有界的在途批次，不把可能已送达、尚待确认的事件算成丢失。
	kept := s.Sending[:0]
	for _, e := range s.Sending {
		if e.OccurredAt.Before(q.clock.Now().Add(-eventlog.BackfillWindow)) {
			if err := q.lost(&s, e, "expired"); err != nil {
				return nil, err
			}
		} else {
			kept = append(kept, e)
		}
	}
	s.Sending = kept
	if len(s.Frozen) == 0 && len(s.Sending) == 0 {
		keys := make([]string, 0, len(s.Pending))
		for key := range s.Pending {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if len(s.Frozen) >= eventlog.MaxBatch {
				break
			}
			s.Frozen = append(s.Frozen, s.Pending[key])
			delete(s.Pending, key)
		}
	}
	if len(s.Sending) == 0 {
		n := eventlog.MaxBatch - len(s.Frozen)
		if n > len(s.Events) {
			n = len(s.Events)
		}
		s.Sending = append(s.Sending, s.Events[:n]...)
		s.Events = s.Events[n:]
	}
	// 无变化时不进行磁盘写入，空队列不增加每次活性的写放大。
	before, _ := json.Marshal(q.state)
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		if err := q.commit(ctx, s); err != nil {
			return nil, err
		}
	}
	batch := append(append([]eventlog.Event{}, s.Frozen...), s.Sending...)
	if len(batch) > eventlog.MaxBatch {
		batch = batch[:eventlog.MaxBatch]
	}
	return batch, nil
}

func (q *EventQueue) Ack(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	acked := make(map[string]bool, len(ids))
	for _, id := range ids {
		acked[id] = true
	}
	s := q.candidate()
	keep := func(input []eventlog.Event) []eventlog.Event {
		out := input[:0]
		for _, e := range input {
			if !acked[e.ID] {
				out = append(out, e)
			}
		}
		return out
	}
	s.Events = keep(s.Events)
	s.Sending = keep(s.Sending)
	s.Frozen = keep(s.Frozen)
	return q.commit(ctx, s)
}
