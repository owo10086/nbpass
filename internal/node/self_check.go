package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"nbpass/internal/selfcheck"
)

type selfCheckJournal struct {
	Config   selfcheck.Config  `json:"config"`
	Revision uint64            `json:"revision"`
	Sequence uint64            `json:"sequence"`
	NextAt   time.Time         `json:"next_at"`
	Pending  []selfcheck.Round `json:"pending"`
}
type selfCheckRunner struct {
	mu      sync.Mutex
	deps    Dependencies
	state   selfCheckJournal
	changed chan struct{}
	wake    chan struct{}
	wg      sync.WaitGroup
}

func openSelfChecks(ctx context.Context, deps Dependencies) (*selfCheckRunner, error) {
	if deps.SelfCheckStore == nil {
		return nil, nil
	}
	r := &selfCheckRunner{deps: deps, changed: make(chan struct{}, 1), wake: make(chan struct{}, 1)}
	data, err := deps.SelfCheckStore.LoadSelfChecks(ctx)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err = json.Unmarshal(data, &r.state); err != nil {
			return nil, err
		}
		if r.state.Revision > 0 {
			if err = r.state.Config.Settings.Validate(); err != nil {
				return nil, err
			}
		}
	}
	if deps.SelfCheckLookup == nil {
		r.deps.SelfCheckLookup = func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	if deps.SelfCheckDial == nil {
		r.deps.SelfCheckDial = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	}
	r.wg.Add(1)
	go func() { defer r.wg.Done(); r.run(ctx) }()
	return r, nil
}
func (r *selfCheckRunner) persist(ctx context.Context, s selfCheckJournal) error {
	data, err := json.Marshal(s)
	if err == nil {
		err = r.deps.SelfCheckStore.SaveSelfChecks(ctx, data)
	}
	if err == nil {
		r.state = s
	}
	return err
}
func (r *selfCheckRunner) apply(ctx context.Context, data []byte, revision uint64) error {
	if r == nil {
		if len(data) > 0 {
			return errors.New("节点未配置自测持久存储")
		}
		return nil
	}
	config := selfcheck.Config{Settings: selfcheck.Defaults()}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return errors.New("自测配置无效")
		}
		if err := config.Settings.Validate(); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if revision < r.state.Revision {
		return nil
	}
	s := r.state
	if selfcheck.ConfigKey(s.Config) != selfcheck.ConfigKey(config) {
		s.NextAt = r.deps.Clock.Now()
	}
	s.Config, s.Revision = config, revision
	if err := r.persist(ctx, s); err != nil {
		return err
	}
	select {
	case r.changed <- struct{}{}:
	default:
	}
	return nil
}
func (r *selfCheckRunner) batch() [][]byte {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var batch [][]byte
	size := 0
	for _, round := range r.state.Pending {
		data, _ := json.Marshal(round)
		if len(batch) >= 8 || size+len(data) > 48<<10 {
			break
		}
		batch = append(batch, data)
		size += len(data)
	}
	return batch
}
func (r *selfCheckRunner) ack(ctx context.Context, ids []string) error {
	if r == nil {
		return errors.New("未配置自测队列")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ack := map[string]bool{}
	for _, id := range ids {
		ack[id] = true
	}
	s := r.state
	s.Pending = make([]selfcheck.Round, 0, len(s.Pending))
	for _, v := range r.state.Pending {
		if !ack[v.ID] {
			s.Pending = append(s.Pending, v)
		}
	}
	return r.persist(ctx, s)
}
func (r *selfCheckRunner) run(ctx context.Context) {
	for ctx.Err() == nil {
		r.mu.Lock()
		s := r.state
		r.mu.Unlock()
		delay := s.NextAt.Sub(r.deps.Clock.Now())
		if !s.Config.Enabled {
			delay = 24 * time.Hour
		}
		timer := r.deps.Clock.NewTimer(delay)
		// 假时钟可能在计算相对期限与注册计时器之间快进；已到期时直接执行。
		if s.Config.Enabled && !r.deps.Clock.Now().Before(s.NextAt) {
			timer.Stop()
		} else {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-r.changed:
				timer.Stop()
				continue
			case <-timer.C():
			}
		}
		if !s.Config.Enabled {
			continue
		}
		round := r.execute(ctx, s)
		if ctx.Err() != nil {
			return
		}
		// 先落盘再发布给控制流；重试保存时保留同一轮，不重复执行网络探测。
		for ctx.Err() == nil {
			r.mu.Lock()
			next := r.state
			round.Sequence = next.Sequence + 1
			sum := sha256.Sum256([]byte(round.ConfigKey + round.StartedAt.Format(time.RFC3339Nano) + ":" + fmtSequence(round.Sequence)))
			round.ID = hex.EncodeToString(sum[:16])
			next.Sequence = round.Sequence
			next.Pending = append(append([]selfcheck.Round(nil), next.Pending...), round)
			if selfcheck.ConfigKey(next.Config) == round.ConfigKey {
				next.NextAt = round.StartedAt.Add(r.interval(s.Config.Settings, round.Sequence))
				if !next.NextAt.After(r.deps.Clock.Now()) {
					next.NextAt = r.deps.Clock.Now().Add(r.interval(s.Config.Settings, round.Sequence))
				}
			}
			err := r.persist(ctx, next)
			r.mu.Unlock()
			if err == nil {
				select {
				case r.wake <- struct{}{}:
				default:
				}
				break
			}
			retry := r.deps.Clock.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				retry.Stop()
				return
			case <-retry.C():
			}
		}
	}
}

// 节点凭据固定参与哈希，使各节点和各轮错峰；测试可将抖动设置为零。
func (r *selfCheckRunner) interval(s selfcheck.Settings, seq uint64) time.Duration {
	jitter := 0
	if s.JitterSeconds > 0 {
		key := fmtSequence(seq)
		if c, err := r.deps.Store.Load(context.Background()); err == nil {
			key += c.NodeID
		}
		sum := sha256.Sum256([]byte(key))
		value := int(sum[0])<<8 | int(sum[1])
		jitter = value%(2*s.JitterSeconds+1) - s.JitterSeconds
	}
	return time.Duration(s.IntervalSeconds+jitter) * time.Second
}
func fmtSequence(seq uint64) string { return strconv.FormatUint(seq, 10) }
func (r *selfCheckRunner) execute(ctx context.Context, s selfCheckJournal) selfcheck.Round {
	started := r.deps.Clock.Now()
	roundCtx, cancel := timeout(ctx, r.deps.Clock, time.Duration(s.Config.RoundTimeoutSeconds)*time.Second)
	defer cancel()
	result := selfcheck.Round{ConfigRevision: s.Revision, ConfigKey: selfcheck.ConfigKey(s.Config), StartedAt: started, Source: "self_check", Sites: make([]selfcheck.Site, 4)}
	var wg sync.WaitGroup
	for i, host := range s.Config.Sites {
		wg.Add(1)
		go func(i int, host string) {
			defer wg.Done()
			result.Sites[i] = r.checkSite(roundCtx, host, s.Config.Settings)
		}(i, host)
	}
	wg.Wait()
	result.FinishedAt = r.deps.Clock.Now()
	result.Result = selfcheck.Result(result.Sites)
	return result
}
func (r *selfCheckRunner) checkSite(ctx context.Context, host string, s selfcheck.Settings) selfcheck.Site {
	site := selfcheck.Site{Host: host, Result: "unknown", Addresses: []string{}, Connections: []selfcheck.Connection{}}
	for attempt := 0; attempt < s.ResolveAttempts && ctx.Err() == nil; attempt++ {
		site.ResolveAttempts++
		query, cancel := timeout(ctx, r.deps.Clock, time.Duration(s.ResolveTimeoutSeconds)*time.Second)
		addresses, err := r.deps.SelfCheckLookup(query, host)
		if query.Err() != nil {
			err = query.Err()
		}
		cancel()
		if err == nil {
			seen := map[string]bool{}
			for _, address := range addresses {
				ip := net.ParseIP(address)
				if ip != nil && ip.To4() != nil && !seen[ip.String()] {
					site.Addresses = append(site.Addresses, ip.String())
					seen[ip.String()] = true
					if len(site.Addresses) == 2 {
						break
					}
				}
			}
			if len(site.Addresses) > 0 {
				site.ResolveError = ""
				break
			}
		}
		site.ResolveError = "解析失败或超时，未取得 IPv4 地址"
	}
	if len(site.Addresses) == 0 {
		return site
	}
	site.Result = "failed"
	for _, ip := range site.Addresses {
		if ctx.Err() != nil {
			site.Result = "unknown"
			break
		}
		address := net.JoinHostPort(ip, "443")
		started := r.deps.Clock.Now()
		call, cancel := timeout(ctx, r.deps.Clock, time.Duration(s.ConnectTimeoutSeconds)*time.Second)
		conn, err := r.deps.SelfCheckDial(call, address)
		expired := call.Err() != nil
		cancel()
		evidence := selfcheck.Connection{Address: address, Connected: err == nil && conn != nil && !expired, DurationMS: r.deps.Clock.Now().Sub(started).Milliseconds()}
		if conn != nil {
			conn.Close()
		}
		if !evidence.Connected {
			evidence.Error = "TCP 连接失败或超时"
		}
		site.Connections = append(site.Connections, evidence)
		if evidence.Connected {
			site.Result = "normal"
			break
		}
		if ctx.Err() != nil {
			site.Result = "unknown"
			break
		}
	}
	return site
}
