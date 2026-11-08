package forwarding

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type boundedConn struct {
	net.Conn
	fast atomic.Bool
}

func (c *boundedConn) Read(b []byte) (int, error) {
	if len(b) > 32768 {
		return 0, errors.New("读取缓冲超过 32 KiB")
	}
	return c.Conn.Read(b)
}
func (c *boundedConn) Write(b []byte) (int, error) {
	if len(b) > 32768 {
		return 0, errors.New("写入缓冲超过 32 KiB")
	}
	return c.Conn.Write(b)
}
func (c *boundedConn) WriteTo(io.Writer) (int64, error) {
	c.fast.Store(true)
	return 0, errors.New("WriterTo 绕过固定缓冲")
}
func (c *boundedConn) ReadFrom(io.Reader) (int64, error) {
	c.fast.Store(true)
	return 0, errors.New("ReaderFrom 绕过固定缓冲")
}

// 物理属性：即使两端提供 fast path，双向大数据也只能使用有界缓冲。
func TestDirectCopyBoundsBuffersAndDisablesFastPaths(t *testing.T) {
	client, a := net.Pipe()
	b, target := net.Pipe()
	for _, conn := range []net.Conn{client, a, b, target} {
		t.Cleanup(func() { conn.Close() })
	}
	up, down := &boundedConn{Conn: a}, &boundedConn{Conn: b}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := newDirectInput(ctx, up, cancel, resourceClock{})
	done := make(chan struct{})
	go func() { defer close(done); proxyDirect(ctx, up, down, input, time.Hour) }()
	request, reply := bytes.Repeat([]byte("up"), 200000), bytes.Repeat([]byte("down"), 150000)
	results := make(chan error, 4)
	go func() { _, err := client.Write(request); results <- err }()
	go func() { _, err := target.Write(reply); results <- err }()
	read := func(conn net.Conn, want []byte) {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		got := make([]byte, len(want))
		_, err := io.ReadFull(conn, got)
		if err == nil && !bytes.Equal(got, want) {
			err = errors.New("双向数据不一致")
		}
		results <- err
	}
	go read(client, reply)
	go read(target, request)
	for range 4 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if up.fast.Load() || down.fast.Load() {
		t.Fatal("发生不受限快速复制")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消后复制协程未退出")
	}
}

// 无读者时写入会阻塞；取消必须关闭两端并等待两个复制协程退出。
func TestDirectCancellationUnblocksBackpressure(t *testing.T) {
	client, a := net.Pipe()
	b, target := net.Pipe()
	for _, conn := range []net.Conn{client, a, b, target} {
		t.Cleanup(func() { conn.Close() })
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := newDirectInput(ctx, a, cancel, resourceClock{})
	done := make(chan struct{})
	go func() { defer close(done); proxyDirect(ctx, a, b, input, time.Hour) }()
	wrote := make(chan struct{})
	go func() { defer close(wrote); client.Write(make([]byte, 32768)) }()
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("复制未读入上行数据")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("背压阻止取消清理")
	}
}

// 目标尚未连上时，预读用满 32 KiB 后必须向客户端施加背压；取消仍可退出。
func TestDirectPreDialBufferBudgetAndCancellationWhenFull(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := newDirectInput(ctx, server, cancel, resourceClock{})
	wrote := make(chan error, 1)
	go func() { _, err := client.Write(make([]byte, 32768)); wrote <- err }()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("未能使用完整缓冲预算")
	}
	// Pipe.Write 完成时生产者可能尚未入队，先等所有权移交完成再取消。
	deadline := time.Now().Add(time.Second)
	for len(input.chunks) != 2 {
		if time.Now().After(deadline) {
			t.Fatal("两块缓冲未完成入队")
		}
		time.Sleep(time.Millisecond)
	}
	go func() { _, err := client.Write([]byte{1}); wrote <- err }()
	select {
	case <-wrote:
		t.Fatal("预读超过32KiB仍未施加背压")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	server.Close()
	select {
	case <-input.done:
	case <-time.After(time.Second):
		t.Fatal("满缓冲阻塞了取消")
	}
	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("客户端写入未解除阻塞")
	}
	// 检查所有权移交的块容量总和，而不只检查单次 Read 的参数。
	total := 0
	for len(input.chunks) > 0 {
		total += cap((<-input.chunks).buffer)
	}
	if total != 32768 {
		t.Fatalf("预读持有缓冲=%d，期望32768", total)
	}
}
