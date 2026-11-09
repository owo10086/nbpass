package clusterharness_test

import (
	"context"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
)

// 控制流被快进中断后，重连定时器可能晚于前置检查的落盘才注册。
// 在原六秒看门狗内推进注入时钟，先等在途消息完成，不用真实等待
// 代替假时钟重连，也不改变动作期限、节点逻辑或终态断言。
func awaitRotationAfterReconnect(t *testing.T, h *clusterharness.Harness, c *clusterharness.Client, path, want string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var state map[string]any
	for ctx.Err() == nil {
		result := c.Do("GET", path, nil, 200)
		state, _ = result["action"].(map[string]any)
		if state != nil && state["state"] == want {
			return state
		}
		if err := h.Clock.WaitForControlIdle(ctx); err != nil {
			break
		}
		h.Clock.Advance(time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("重连后换 IP 未进入 %s: %v", want, state)
	return nil
}
