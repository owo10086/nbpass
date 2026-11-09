package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/publicip"
	"nbpass/internal/rotation"
)

type RotationStore interface {
	LoadRotation(context.Context) ([]byte, error)
	SaveRotation(context.Context, []byte) error
}

type rotationJournal struct {
	HistorySince    int64                                  `json:"history_since"`
	AttemptedAt     map[string]int64                       `json:"attempted_at"`
	RecheckConfig   []byte                                 `json:"recheck_config"`
	RecheckRevision uint64                                 `json:"recheck_revision"`
	Current         string                                 `json:"current"`
	Results         map[string]*controlpb.RotationProgress `json:"results"`
	RetiredBefore   int64                                  `json:"retired_before"`
	NextCheck       int64                                  `json:"next_check"`
}
type rotationRunner struct {
	mu      sync.Mutex
	deps    Dependencies
	ctx     context.Context
	state   rotationJournal
	wake    chan struct{}
	running bool
	wg      sync.WaitGroup
}

func openRotations(ctx context.Context, deps Dependencies) (*rotationRunner, error) {
	if deps.RotationStore == nil {
		return nil, nil
	}
	r := &rotationRunner{ctx: ctx, deps: deps, wake: make(chan struct{}, 1), state: rotationJournal{Results: map[string]*controlpb.RotationProgress{}}}
	data, err := deps.RotationStore.LoadRotation(ctx)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, &r.state); err != nil || len(r.state.Results) > 4096 || r.state.Results == nil {
			return nil, errors.New("换 IP 动作缓存损坏")
		}
	}
	// 新日志从创建时刻开始证明覆盖范围；旧日志缺少时间证据时从本次升级起保护。
	if r.state.HistorySince == 0 {
		r.state.HistorySince = deps.Clock.Now().UnixNano()
	}
	if r.state.AttemptedAt == nil {
		r.state.AttemptedAt = map[string]int64{}
	}
	encoded, err := json.Marshal(r.state)
	if err != nil {
		return nil, err
	}
	if err = deps.RotationStore.SaveRotation(ctx, encoded); err != nil {
		return nil, err
	}
	// 票28的缓存不含完成标记；离线完成的终态也要以新序号补报，才能开放核对。
	if current := r.state.Results[r.state.Current]; current != nil && current.State == "unconfirmed" && current.Attempts == 5 && !current.ObservationComplete {
		completed := proto.Clone(current).(*controlpb.RotationProgress)
		completed.ObservationComplete = true
		if err := r.save(completed, 0); err != nil {
			return nil, err
		}
	}
	if current := r.state.Results[r.state.Current]; current != nil && (current.State == "started" || current.State == "observing" || (current.State == "unconfirmed" && current.Attempts < 5 && !current.ObservationComplete)) {
		r.running = true
		r.wg.Add(1)
		go func() { defer r.wg.Done(); defer r.finished(); r.observe(current) }()
	} else if current := r.state.Results[r.state.Current]; current != nil && current.State == "changed" && !current.RecheckComplete && len(r.state.RecheckConfig) > 0 {
		r.running = true
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer r.finished()
			r.recheck(proto.Clone(current).(*controlpb.RotationProgress))
		}()
	}
	return r, nil
}
func (r *rotationRunner) finished() { r.mu.Lock(); r.running = false; r.mu.Unlock() }
func (r *rotationRunner) snapshot() *controlpb.RotationProgress {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.state.Results[r.state.Current]
	if p == nil {
		return nil
	}
	return proto.Clone(p).(*controlpb.RotationProgress)
}
func (r *rotationRunner) save(p *controlpb.RotationProgress, next int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	results := make(map[string]*controlpb.RotationProgress, len(r.state.Results)+1)
	for k, v := range r.state.Results {
		results[k] = v
	}
	copy := proto.Clone(p).(*controlpb.RotationProgress)
	copy.Sequence++
	// 封存发行时间水位，压缩前的许可即使仍在两分钟内也不能再次执行。
	cutoff := r.state.RetiredBefore
	if _, known := results[p.ActionId]; !known && len(results) >= 4096 {
		cutoff = r.deps.Clock.Now().UnixNano()
		results = map[string]*controlpb.RotationProgress{}
	}
	results[p.ActionId] = copy
	attempts := make(map[string]int64, len(r.state.AttemptedAt)+1)
	for id, at := range r.state.AttemptedAt {
		if results[id] != nil {
			attempts[id] = at
		}
	}
	since := r.state.HistorySince
	if cutoff > r.state.RetiredBefore {
		since = r.deps.Clock.Now().UnixNano()
	}
	if p.State == "started" && attempts[p.ActionId] == 0 {
		attempts[p.ActionId] = r.deps.Clock.Now().UnixNano()
	}
	if p.State == "cancelled" && attempts[p.ActionId] == 0 {
		attempts[p.ActionId] = r.deps.Clock.Now().UnixNano()
	}
	state := rotationJournal{HistorySince: since, AttemptedAt: attempts, RecheckConfig: r.state.RecheckConfig, RecheckRevision: r.state.RecheckRevision, Current: p.ActionId, Results: results, NextCheck: next, RetiredBefore: cutoff}
	data, err := json.Marshal(state)
	if err == nil {
		err = r.deps.RotationStore.SaveRotation(r.ctx, data)
	}
	if err != nil {
		return err
	}
	r.state = state
	p.Sequence = copy.Sequence
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// 进度写失败只重试本地持久化；永不重新调用商家接口。
func (r *rotationRunner) persist(p *controlpb.RotationProgress, next int64) error {
	for {
		if err := r.save(p, next); err == nil {
			return nil
		}
		timer := r.deps.Clock.NewTimer(time.Second)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return r.ctx.Err()
		case <-timer.C():
		}
	}
}
func (r *rotationRunner) blocksIPObservation() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.state.Results[r.state.Current]
	return r.running || (p != nil && !rotationTerminal(p.State))
}

func rotationTerminal(state string) bool {
	return state == "changed" || state == "cancelled" || state == "released"
}

func (r *rotationRunner) submit(online context.Context, nodeID string, c *controlpb.RotationCommand) {
	if r == nil || c == nil {
		return
	}
	r.mu.Lock()
	if c.NodeId != nodeID || c.ActionId == "" || len(c.ActionId) > 128 || len(c.ConfigJson) > 8192 || r.running {
		r.mu.Unlock()
		return
	}
	if c.Operation != "" {
		r.submitControlLocked(online, c)
		return
	}
	if _, ok := r.state.Results[c.ActionId]; ok {
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default:
		}
		return
	}
	if old := r.state.Results[r.state.Current]; old != nil && !rotationTerminal(old.State) {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	r.wg.Add(1)
	go func() { defer r.wg.Done(); defer r.finished(); r.execute(online, c) }()
}
func (r *rotationRunner) query(ctx context.Context) (string, bool, error) {
	var values [2]string
	for i, source := range r.deps.RotationIPv4Sources {
		qctx, cancel := timeout(ctx, r.deps.Clock, 5*time.Second)
		value, err := source(qctx)
		if qctx.Err() != nil {
			err = qctx.Err()
		}
		cancel()
		if err != nil {
			return "", false, err
		}
		ip, err := publicip.Parse(value)
		if err != nil {
			return "", false, err
		}
		values[i] = ip
	}
	if values[0] != values[1] {
		return "", true, errors.New("查询源不一致")
	}
	return values[0], false, nil
}
func (r *rotationRunner) execute(online context.Context, c *controlpb.RotationCommand) {
	p := &controlpb.RotationProgress{ActionId: c.ActionId, OldIp: c.OldIp, State: "cancelled"}
	cancel := func(reason string) {
		p.State = "cancelled"
		p.Reason = reason
		p.ObservationComplete = true
		_ = r.persist(p, 0)
	}
	valid := func() bool {
		if online.Err() != nil {
			cancel("offline")
			return false
		}
		now := r.deps.Clock.Now().UnixNano()
		r.mu.Lock()
		retired := r.state.RetiredBefore
		current := r.state.Current
		r.mu.Unlock()
		if (c.IssuedAt <= retired && c.ActionId != current) || now >= c.ExpiresAt || c.ExpiresAt-c.IssuedAt > int64(2*time.Minute) || now < c.IssuedAt {
			cancel("expired")
			return false
		}
		return true
	}
	if !valid() {
		return
	}
	ip, unsafe, err := r.query(online)
	p.UnsafeNetwork = unsafe
	if err != nil {
		if unsafe {
			cancel("inconsistent")
		} else {
			cancel("query_failed")
		}
		return
	}
	if ip != c.OldIp {
		cancel("old_ip_mismatch")
		return
	}
	locals, err := r.deps.RotationLocalIPv4(online)
	if err != nil || len(locals) != 1 || locals[0] != ip {
		p.UnsafeNetwork = true
	}
	if c.Automatic && p.UnsafeNetwork {
		cancel("unsafe_network")
		return
	}
	var config rotation.Config
	if json.Unmarshal(c.ConfigJson, &config) != nil || config.Validate() != nil {
		cancel("invalid_config")
		return
	}
	if !valid() {
		return
	}
	r.mu.Lock()
	r.state.RecheckConfig = append([]byte(nil), c.RecheckConfig...)
	r.state.RecheckRevision = c.RecheckRevision
	r.mu.Unlock()
	p.State, p.Reason = "started", "started"
	if r.save(p, 0) != nil {
		cancel("action_storage_failed")
		return
	} // 落盘失败绝不能调用外部接口。
	if r.deps.Events != nil {
		if r.deps.Events.Record(r.ctx, "ip_rotation_started", "换 IP 动作 "+p.ActionId+" 已开始", 0) != nil {
			cancel("event_storage_failed")
			return
		}
	}
	if !valid() {
		p.State = "cancelled"
		return
	}
	callCtx, stop := timeout(r.ctx, r.deps.Clock, 60*time.Second)
	err = r.deps.RotationHTTP(callCtx, config, rotation.Variables{NodeID: c.NodeId, NodeName: c.NodeName, OldIP: c.OldIp, ActionID: c.ActionId, Timestamp: r.deps.Clock.Now().UTC().Format(time.RFC3339Nano)})
	if callCtx.Err() != nil {
		err = callCtx.Err()
	}
	stop()
	p.State, p.Reason = "observing", "observing"
	if err != nil {
		p.State, p.Reason = "unconfirmed", "http_unconfirmed"
	}
	if r.persist(p, r.deps.Clock.Now().Add(60*time.Second).UnixNano()) != nil {
		return
	}
	r.observe(p)
}
func (r *rotationRunner) observe(input *controlpb.RotationProgress) {
	p := proto.Clone(input).(*controlpb.RotationProgress)
	if p.State == "started" {
		p.State, p.Reason = "unconfirmed", "interrupted"
		if r.persist(p, r.deps.Clock.Now().Add(time.Minute).UnixNano()) != nil {
			return
		}
	}
	for p.Attempts < 5 {
		r.mu.Lock()
		next := r.state.NextCheck
		r.mu.Unlock()
		if next == 0 {
			next = r.deps.Clock.Now().Add(60 * time.Second).UnixNano()
		}
		timer := r.deps.Clock.NewTimer(time.Unix(0, next).Sub(r.deps.Clock.Now()))
		// 注入时钟可能在计算相对期限和注册计时器之间快进。
		if r.deps.Clock.Now().Before(time.Unix(0, next)) {
			select {
			case <-r.ctx.Done():
				timer.Stop()
				return
			case <-timer.C():
			}
		} else {
			timer.Stop()
		}
		if r.ctx.Err() != nil {
			return
		}
		ip, unsafe, err := r.query(r.ctx)
		p.Attempts++
		// 换后的短暂双源差异属于结果未知，不把它当作永久 NAT/多 IP 结论。
		if err == nil && ip != p.OldIp {
			p.State, p.Reason, p.NewIp, p.ObservationComplete = "changed", "changed", ip, true
			if r.persist(p, 0) == nil && r.deps.Events != nil {
				_ = r.deps.Events.Record(r.ctx, "ip_rotation_changed", "换 IP 动作 "+p.ActionId+" 已观察到公网 IPv4 变化，可达性尚未复检", 0)
			}
			r.recheck(p)
			return
		}
		// 一旦结果不确定，即使下一次仍看到旧 IP，也不能自动解除冻结。
		if unsafe {
			p.State, p.Reason = "unconfirmed", "observation_inconsistent"
		} else if err != nil {
			p.State, p.Reason = "unconfirmed", "observation_query_failed"
		}
		if p.Attempts == 5 {
			p.State, p.ObservationComplete = "unconfirmed", true
			if p.Reason == "observing" || p.Reason == "started" {
				p.Reason = "unconfirmed"
			}
		}
		if r.persist(p, r.deps.Clock.Now().Add(60*time.Second).UnixNano()) != nil {
			return
		}
	}
}
func localPublicIPv4(context.Context) ([]string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	for _, address := range addresses {
		ip, _, e := net.ParseCIDR(address.String())
		if e != nil {
			continue
		}
		if value, e := publicip.Parse(ip.String()); e == nil {
			unique[value] = true
		}
	}
	result := make([]string, 0, len(unique))
	for ip := range unique {
		result = append(result, ip)
	}
	return result, nil
}
