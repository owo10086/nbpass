package forwarding

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// yamux 缓存底层写期限，空闲后才刷新。NetConn 的共享写定时器若已开始
// 回调，刷新不能阻止旧回调取消下一次写入。保留 NetConn 的读流适配，
// 每次实际 WSS 写入使用独立 context，避免过期定时器跨越两次写入。
// 载体写入及其期限由单一 yamux 写循环管理，认证阶段则串行使用。
type tunnelCarrier struct {
	net.Conn
	ws            *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	writeDeadline time.Time
}

func newTunnelCarrier(parent context.Context, ws *websocket.Conn) net.Conn {
	ctx, cancel := context.WithCancel(parent)
	return &tunnelCarrier{Conn: websocket.NetConn(ctx, ws, websocket.MessageBinary), ws: ws, ctx: ctx, cancel: cancel}
}

func (c *tunnelCarrier) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.writeDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *tunnelCarrier) SetDeadline(deadline time.Time) error {
	c.SetWriteDeadline(deadline)
	return c.Conn.SetReadDeadline(deadline)
}

func (c *tunnelCarrier) Write(b []byte) (int, error) {
	c.mu.Lock()
	deadline := c.writeDeadline
	c.mu.Unlock()
	ctx := c.ctx
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	if err := c.ws.Write(ctx, websocket.MessageBinary, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *tunnelCarrier) Close() error {
	c.cancel()
	c.Conn.SetReadDeadline(time.Time{})
	// 业务 FIN 已由 yamux 处理；载体回收不再等待对端 WSS 关闭握手。
	return c.ws.CloseNow()
}
