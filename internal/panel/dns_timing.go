package panel

import (
	"context"
	"time"
)

// 包含假服务与真实 HTTP 在内的所有 DNS 调用共享注入时钟期限。
func (p *Panel) dnsContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	timer := p.deps.Clock.NewTimer(10 * time.Second)
	go func() {
		defer timer.Stop()
		select {
		case <-timer.C():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
