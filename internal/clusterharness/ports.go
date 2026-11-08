package clusterharness

import (
	"net"
	"strconv"
	"sync"
)

type portReservations struct {
	mu        sync.Mutex
	listeners map[int]net.Listener
}

// ReserveTCPPort 持有真实监听器，避免选端口到节点 bind 之间被出站临时端口占用。
// 预留期间不代表节点已经就绪；业务验证仍须先等待配置确认。
func (h *Harness) ReserveTCPPort() int {
	h.t.Helper()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		h.t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	h.ports.mu.Lock()
	defer h.ports.mu.Unlock()
	if h.ports.listeners == nil {
		h.ports.listeners = make(map[int]net.Listener)
	}
	h.ports.listeners[port] = listener
	return port
}

// ListenTCP 将预留监听器一次性交给节点；之后关闭与重启均走真实 socket 生命周期。
// 注入监听故障或物理端口映射的测试也应在其钩子内调用此方法。
func (h *Harness) ListenTCP(network, address string) (net.Listener, error) {
	host, portText, err := net.SplitHostPort(address)
	if err == nil && network == "tcp4" && (host == "0.0.0.0" || host == "127.0.0.1") {
		port, _ := strconv.Atoi(portText)
		h.ports.mu.Lock()
		listener := h.ports.listeners[port]
		delete(h.ports.listeners, port)
		h.ports.mu.Unlock()
		if listener != nil {
			return listener, nil
		}
	}
	return net.Listen(network, address)
}

func (p *portReservations) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for port, listener := range p.listeners {
		listener.Close()
		delete(p.listeners, port)
	}
}
