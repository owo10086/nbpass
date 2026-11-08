package clusterharness

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"

	"nbpass/internal/node"
)

// Node 的网络开关只控制环境，业务结果仍须通过面板 HTTP API 观察。
type Node struct {
	Done        chan error
	cancel      context.CancelFunc
	finished    chan struct{}
	mu          sync.Mutex
	blocked     bool
	connections map[net.Conn]bool
	store       node.Store
	silent      bool
	config      node.Config
	deps        node.Dependencies
}

type memoryCredentials struct {
	mu    sync.Mutex
	value node.Credentials
}

func (s *memoryCredentials) Load(context.Context) (node.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value.Token == "" {
		return node.Credentials{}, os.ErrNotExist
	}
	return s.value, nil
}
func (s *memoryCredentials) Save(_ context.Context, value node.Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
	return nil
}

func (h *Harness) StartNode(ticket string, options ...func(*node.Config, *node.Dependencies)) *Node {
	h.t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(h.Control.Certificate())
	n := &Node{store: node.FileStore{Dir: filepath.Join(h.t.TempDir(), "node"), Random: rand.Reader}, connections: make(map[net.Conn]bool)}
	n.config = node.Config{ControlURL: h.Control.URL, InstallationTicket: ticket}
	n.deps = node.Dependencies{Clock: h.Clock, Store: n.store, Random: rand.Reader, RootCAs: roots, DialContext: n.dial}
	for _, option := range options {
		option(&n.config, &n.deps)
	}
	n.start()
	h.t.Cleanup(n.Stop)
	return n
}

func (n *Node) start() {
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	n.Done, n.finished = make(chan error, 1), make(chan struct{})
	go func() { defer close(n.finished); n.Done <- node.Run(ctx, n.config, n.deps) }()
}
func (n *Node) Stop()    { n.cancel(); <-n.finished }
func (n *Node) Restart() { n.Stop(); n.start() }

// Credentials 返回节点自身存储的副本，供模拟凭据重放，绝不读取面板内部状态。
func (n *Node) Credentials() (node.Store, error) {
	value, err := n.deps.Store.Load(context.Background())
	if err != nil {
		return nil, err
	}
	return &memoryCredentials{value: value}, nil
}
func (n *Node) Disconnect() {
	n.mu.Lock()
	n.blocked = true
	connections := n.connections
	n.connections = make(map[net.Conn]bool)
	n.mu.Unlock()
	for conn := range connections {
		conn.Close()
	}
}
func (n *Node) Restore() { n.mu.Lock(); n.blocked = false; n.mu.Unlock() }

// Blackhole 丢弃后续控制数据，不主动关闭连接，模拟没有 FIN/RST 的失联。
func (n *Node) Blackhole() { n.mu.Lock(); n.silent = true; n.mu.Unlock() }

func (h *Harness) CloneNode(original *Node, claimedID string) *Node {
	h.t.Helper()
	value, err := original.deps.Store.Load(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	if claimedID != "" {
		value.NodeID = claimedID
	}
	return h.StartNode("", func(_ *node.Config, deps *node.Dependencies) { deps.Store = &memoryCredentials{value: value} })
}

type controlLink struct {
	net.Conn
	node *Node
}

func (c *controlLink) Write(data []byte) (int, error) {
	c.node.mu.Lock()
	silent := c.node.silent
	c.node.mu.Unlock()
	if silent {
		return len(data), nil
	}
	return c.Conn.Write(data)
}
func (c *controlLink) Close() error {
	c.node.mu.Lock()
	delete(c.node.connections, c)
	c.node.mu.Unlock()
	return c.Conn.Close()
}
func (n *Node) dial(ctx context.Context, address string) (net.Conn, error) {
	n.mu.Lock()
	blocked := n.blocked
	n.mu.Unlock()
	if blocked {
		return nil, errors.New("测试台断开控制链路")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.blocked {
		conn.Close()
		return nil, errors.New("测试台断开控制链路")
	}
	link := &controlLink{Conn: conn, node: n}
	n.connections[link] = true
	return link, nil
}
