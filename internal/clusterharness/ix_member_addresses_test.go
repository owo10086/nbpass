package clusterharness_test

import (
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
)

func ixMappedProxy(t *testing.T, target int) (int, *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	count := &atomic.Int32{}
	go func() {
		for {
			incoming, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer incoming.Close()
				outgoing, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(target)))
				if err != nil {
					return
				}
				defer outgoing.Close()
				count.Add(1)
				done := make(chan struct{})
				go func() { io.Copy(outgoing, incoming); close(done) }()
				io.Copy(incoming, outgoing)
				outgoing.Close()
				incoming.Close()
				<-done
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, count
}
