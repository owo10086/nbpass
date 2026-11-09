package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"nbpass/internal/controlpb"
	"nbpass/internal/selfcheck"
	"net"
	"time"
)

// 与原动作共用加密缓存及进度序号；断线继续自测，重建从已落盘轮次续做。
func (r *rotationRunner) recheck(p *controlpb.RotationProgress) {
	r.mu.Lock()
	data := append([]byte(nil), r.state.RecheckConfig...)
	revision := r.state.RecheckRevision
	r.mu.Unlock()
	if len(data) == 0 || p.RecheckComplete {
		return
	}
	var config selfcheck.Config
	if json.Unmarshal(data, &config) != nil || config.Settings.Validate() != nil {
		return
	}
	runner := &selfCheckRunner{deps: r.deps}
	if runner.deps.SelfCheckLookup == nil {
		runner.deps.SelfCheckLookup = func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	if runner.deps.SelfCheckDial == nil {
		runner.deps.SelfCheckDial = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	}
	for len(p.RecheckRounds) < 3 && r.ctx.Err() == nil {
		r.mu.Lock()
		next := r.state.NextCheck
		r.mu.Unlock()
		if next > r.deps.Clock.Now().UnixNano() {
			timer := r.deps.Clock.NewTimer(time.Unix(0, next).Sub(r.deps.Clock.Now()))
			if r.deps.Clock.Now().UnixNano() < next {
				select {
				case <-r.ctx.Done():
					timer.Stop()
					return
				case <-timer.C():
				}
			} else {
				timer.Stop()
			}
		}
		round := runner.execute(r.ctx, selfCheckJournal{Config: config, Revision: max(1, revision)})
		if r.ctx.Err() != nil {
			return
		}
		round.Sequence = uint64(len(p.RecheckRounds) + 1)
		sum := sha256.Sum256([]byte(p.ActionId + ":" + fmtSequence(round.Sequence)))
		round.ID = hex.EncodeToString(sum[:16])
		encoded, _ := json.Marshal(round)
		p.RecheckRounds = append(p.RecheckRounds, encoded)
		p.RecheckComplete = round.Result == "normal" || len(p.RecheckRounds) == 3
		next = r.deps.Clock.Now().Add(time.Duration(config.IntervalSeconds) * time.Second).UnixNano()
		if p.RecheckComplete {
			next = 0
		}
		if r.persist(p, next) != nil || p.RecheckComplete {
			return
		}
	}
}
