package forwarding

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"nbpass/internal/timing"
)

const directBufferSize = 32 * 1024

func (r *listenerRule) acceptDirect(client net.Conn) {
	lease := r.engine.resources.acquire(r.config.Id)
	if lease == nil {
		client.Close()
		return
	}
	if !r.track(client) {
		lease.release()
		return
	}
	finishTraffic := r.engine.meterConnection(r.config.Id)
	r.engine.businessConnections.Add(1)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer lease.release()
		defer finishTraffic()
		defer r.engine.businessConnections.Add(^uint64(0))
		defer r.untrack(client)
		r.serveDirect(client, lease)
	}()
}

func (r *listenerRule) serveDirect(client net.Conn, lease *resourceLease) {
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	input := newDirectInput(ctx, client, cancel, r.engine.deps.Clock)
	defer func() { cancel(); client.Close(); <-input.done }()
	target, err := r.engine.dialTargetWithin(ctx, r.config, r.engine.resources.limits.DialTimeout, r.engine.outboundBinding().source)
	lease.dialDone()
	if err != nil {
		return
	}
	if !r.track(target) {
		return
	}
	defer r.untrack(target)
	client, target = r.engine.meteredConns(r.config.Id, client, target)
	proxyDirect(ctx, client, target, input, r.engine.resources.limits.IdleTimeout)
}

// 仅分配一块 32 KiB，分成两块循环移交所有权。生产者、队列、消费者
// 共用这些块；拨号后直接写目标，不另建上行复制缓冲。
type directInput struct {
	ctx      context.Context
	free     chan []byte
	chunks   chan directChunk
	done     chan struct{}
	activity *directActivity
}

type directChunk struct {
	buffer []byte
	n      int
	err    error
}

func newDirectInput(ctx context.Context, client net.Conn, cancel context.CancelFunc, clock timing.Clock) *directInput {
	r := &directInput{ctx: ctx, free: make(chan []byte, 2), chunks: make(chan directChunk, 2), done: make(chan struct{}), activity: &directActivity{clock: clock, last: clock.Now()}}
	buffer := make([]byte, directBufferSize)
	r.free <- buffer[: directBufferSize/2 : directBufferSize/2]
	r.free <- buffer[directBufferSize/2:]
	go func() {
		defer close(r.done)
		for {
			var buffer []byte
			select {
			case <-ctx.Done():
				return
			case buffer = <-r.free:
			}
			n, err := client.Read(buffer)
			if n > 0 {
				r.activity.touch()
			}
			// FIN 是写半关闭；只有 RST 等读取错误取消整条业务连接。
			if err != nil && err != io.EOF {
				cancel()
			}
			select {
			case <-ctx.Done():
				return
			case r.chunks <- directChunk{buffer, n, err}:
			}
			if err != nil {
				return
			}
		}
	}()
	return r
}

func (r *directInput) copyTo(dst io.Writer) error {
	for {
		var chunk directChunk
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case chunk = <-r.chunks:
		}
		if chunk.n > 0 {
			n, err := dst.Write(chunk.buffer[:chunk.n])
			if err != nil {
				return err
			}
			if n != chunk.n {
				return io.ErrShortWrite
			}
		}
		if chunk.err == io.EOF {
			return nil
		}
		if chunk.err != nil {
			return chunk.err
		}
		select {
		case <-r.ctx.Done():
			return r.ctx.Err()
		case r.free <- chunk.buffer:
		}
	}
}

type directActivity struct {
	mu    sync.Mutex
	clock timing.Clock
	last  time.Time
}

func (a *directActivity) touch() { a.mu.Lock(); a.last = a.clock.Now(); a.mu.Unlock() }
func (a *directActivity) remaining(idle time.Duration) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return idle - a.clock.Now().Sub(a.last)
}

// 两方向共享空闲时间；单向持续传输或半关闭后的回复都能续期。
// 仅暴露 Reader/Writer，禁止 io.Copy 的 WriterTo/ReaderFrom 绕过固定缓冲。
func proxyDirect(ctx context.Context, client, target net.Conn, input *directInput, idle time.Duration) {
	closeBoth := func() { client.Close(); target.Close() }
	stopCancel := context.AfterFunc(ctx, closeBoth)
	defer stopCancel()
	activity := input.activity
	activity.touch()
	stopIdle, idleDone := make(chan struct{}), make(chan struct{})
	timer := activity.clock.NewTimer(idle)
	go func() {
		defer close(idleDone)
		defer func() { timer.Stop() }()
		for {
			select {
			case <-stopIdle:
				return
			case <-timer.C():
				remaining := activity.remaining(idle)
				if remaining <= 0 {
					closeBoth()
					return
				}
				timer = activity.clock.NewTimer(remaining)
			}
		}
	}()
	defer func() { close(stopIdle); <-idleDone }()
	finishHalf := func(dst net.Conn, err error) {
		if err != nil {
			closeBoth()
			return
		}
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			if err := closer.CloseWrite(); err != nil {
				closeBoth()
			}
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		finishHalf(target, input.copyTo(activityWriter{target, activity.touch}))
	}()
	_, err := io.CopyBuffer(activityWriter{client, activity.touch}, activityReader{target, activity.touch}, make([]byte, directBufferSize))
	finishHalf(client, err)
	<-done
}

type activityReader struct {
	io.Reader
	touch func()
}

func (r activityReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if n > 0 {
		r.touch()
	}
	return n, err
}

type activityWriter struct {
	io.Writer
	touch func()
}

func (w activityWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	if n > 0 {
		w.touch()
	}
	return n, err
}

type resourceClock struct{}

func (resourceClock) Now() time.Time                        { return time.Now() }
func (resourceClock) NewTimer(d time.Duration) timing.Timer { return resourceTimer{time.NewTimer(d)} }

type resourceTimer struct{ *time.Timer }

func (t resourceTimer) C() <-chan time.Time { return t.Timer.C }
