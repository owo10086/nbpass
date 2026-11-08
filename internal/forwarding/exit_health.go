package forwarding

import (
	"context"
	"math/rand/v2"
	"sort"
	"time"

	"nbpass/internal/controlpb"
)

func exitProbeDelay() time.Duration {
	return 4500*time.Millisecond + time.Duration(rand.Int64N(int64(time.Second)+1))
}

func (c *tunnelClient) recordProbe(now time.Time, class failureClass) {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	c.recordProbeLocked(now, class)
}
func (c *tunnelClient) recordProbeLocked(now time.Time, class failureClass) {
	c.checkedAt, c.failure = now, class
	switch class {
	case "":
		c.lastProbe = "success"
		c.failures = 0
		if c.unhealthy {
			if c.recoverySince.IsZero() || now.Sub(c.lastSuccess) > 11*time.Second {
				c.recoverySince = now
			}
			if now.Sub(c.recoverySince) >= 60*time.Second {
				c.unhealthy = false
				c.recoverySince = time.Time{}
			}
		}
		c.lastSuccess = now
	case failureLink:
		c.lastProbe = "failure"
		c.failures++
		c.recoverySince = time.Time{}
		if c.failures >= 3 {
			c.unhealthy = true
		}
	default:
		// 取消、并发满、本地资源、配置/授权都不是链路失败。
		c.lastProbe = "incomplete"
		c.recoverySince = time.Time{}
	}
}

func (c *tunnelClient) probeOnce(ctx context.Context) {
	select {
	case c.engine.probeSlots <- struct{}{}:
		defer func() { <-c.engine.probeSlots }()
		c.healthMu.Lock()
		epoch := c.failureEpoch
		c.healthMu.Unlock()
		probe, stop := context.WithTimeout(ctx, 3*time.Second)
		session, err := c.openSession(probe, true)
		class := failureClass("")
		if err != nil {
			class = classifyTunnel(err)
		}
		if ctx.Err() != nil {
			class = failureLocal
		}
		stop()
		if session != nil {
			session.Close()
		}
		c.healthMu.Lock()
		defer c.healthMu.Unlock()
		if epoch == c.failureEpoch {
			c.recordProbeLocked(c.engine.deps.Clock.Now(), class)
		}
	default:
		c.recordProbe(c.engine.deps.Clock.Now(), failureLocal)
	}
}
func (c *tunnelClient) startProbes() {
	ctx, cancel := context.WithCancel(context.Background())
	c.probeCancel, c.probeDone = cancel, make(chan struct{})
	go func() {
		defer close(c.probeDone)
		timer := c.engine.deps.Clock.NewTimer(exitProbeDelay())
		defer func() { timer.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C():
			}
			timer = c.engine.deps.Clock.NewTimer(exitProbeDelay())
			c.probeOnce(ctx)
		}
	}()
}

// ExitHealth 可并发采样；数据只代表当前入口到出口的链路观测。
func (e *Engine) ExitHealth() []*controlpb.ExitHealth {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	result := make([]*controlpb.ExitHealth, 0, len(e.tunnelClients))
	for id, c := range e.tunnelClients {
		c.healthMu.Lock()
		state := "healthy"
		if c.lastProbe == "" {
			state = "unknown"
		}
		if c.unhealthy {
			state = "unhealthy"
			if !c.recoverySince.IsZero() {
				state = "recovering"
			}
		}
		h := &controlpb.ExitHealth{ExitId: id, State: state, ConsecutiveFailures: c.failures, LastProbe: c.lastProbe, FailureClass: string(c.failure)}
		if !c.recoverySince.IsZero() {
			h.RecoverySinceUnix = c.recoverySince.Unix()
		}
		if !c.checkedAt.IsZero() {
			h.CheckedAtUnix = c.checkedAt.Unix()
		}
		c.healthMu.Unlock()
		result = append(result, h)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ExitId < result[j].ExitId })
	return result
}
