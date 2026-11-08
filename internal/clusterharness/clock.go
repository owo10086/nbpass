package clusterharness

import (
	"sync"
	"time"

	"nbpass/internal/timing"
)

type Clock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]time.Time
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
}

func (t *fakeTimer) C() <-chan time.Time { return t.channel }
func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	_, exists := t.clock.timers[t]
	delete(t.clock.timers, t)
	return exists
}
func (c *Clock) NewTimer(d time.Duration) timing.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{clock: c, channel: make(chan time.Time, 1)}
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
