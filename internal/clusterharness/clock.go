package clusterharness

import (
	"context"
	"sync"
	"time"

	"nbpass/internal/timing"
)

type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]time.Time
	// 15 秒是节点与面板的控制发送/响应/独立补报确认期限。
	// 已投递的定时器仍在途，直至消费者 Stop；不能只检查 timers。
	controlTimers  map[*fakeTimer]struct{}
	controlChanged chan struct{}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	if d < 0 {
		panic("测试时钟不能后退")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for timer, deadline := range c.timers {
		if !deadline.After(c.now) {
			delete(c.timers, timer)
			timer.channel <- c.now
		}
	}
}

type fakeTimer struct {
	clock   *Clock
	channel chan time.Time
	control bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.channel }
func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	_, exists := t.clock.timers[t]
	delete(t.clock.timers, t)
	if t.control {
		delete(t.clock.controlTimers, t)
		if t.clock.controlChanged != nil {
			close(t.clock.controlChanged)
			t.clock.controlChanged = nil
		}
	}
	return exists
}
func (c *Clock) NewTimer(d time.Duration) timing.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, channel: make(chan time.Time, 1), control: d == 15*time.Second}
	if timer.control {
		if c.controlTimers == nil {
			c.controlTimers = make(map[*fakeTimer]struct{})
		}
		c.controlTimers[timer] = struct{}{}
	}
	if c.timers == nil {
		c.timers = make(map[*fakeTimer]time.Time)
	}
	if d <= 0 {
		timer.channel <- c.now
	} else {
		c.timers[timer] = c.now.Add(d)
	}
	return timer
}

// WaitForControlIdle 供健康节点的假时钟推进使用：HTTP 可见的心跳落库
// 早于响应消费，必须等在途控制发送和确认全部结束，才能继续快进。
// 故障/离线测试仍直接 Advance，保持真实超时与断连的验证能力。
func (c *Clock) WaitForControlIdle(ctx context.Context) error {
	for {
		c.mu.Lock()
		if len(c.controlTimers) == 0 {
			c.mu.Unlock()
			return nil
		}
		if c.controlChanged == nil {
			c.controlChanged = make(chan struct{})
		}
		changed := c.controlChanged
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
