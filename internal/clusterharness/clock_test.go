package clusterharness

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 定时器已投递仍可能对应尚未消费的控制响应；只有 Stop 才完成这次工作。
func TestWaitForControlIdleWaitsForDeliveredTimerToStop(t *testing.T) {
	clock := &Clock{now: time.Now()}
	pending := clock.NewTimer(15 * time.Second)
	clock.Advance(15 * time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.WaitForControlIdle(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("在途控制定时器已投递就被误认为空闲: %v", err)
	}
	pending.Stop()
	interval := clock.NewTimer(10 * time.Second)
	defer interval.Stop()
	if err := clock.WaitForControlIdle(ctx); err != nil {
		t.Fatalf("业务或心跳间隔不应阻止控制空闲: %v", err)
	}
}
