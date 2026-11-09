package forwarding

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	yamux "github.com/libp2p/go-yamux/v5"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

const tunnelPath = "/nbpass/tunnel/v1"
const tunnelSubprotocol = "nbpass.tunnel.v1"

type tunnelAuth struct {
	Version    int    `json:"version"`
	EntryID    string `json:"entry_id"`
	Credential string `json:"credential"`
}
type tunnelRequest struct {
	RuleID        string                  `json:"rule_id"`
	EntryID       string                  `json:"entry_id,omitempty"`
	Generation    uint64                  `json:"generation,omitempty"`
	TargetAddress string                  `json:"target_address,omitempty"`
	TargetPort    uint32                  `json:"target_port,omitempty"`
	TargetFamily  controlpb.AddressFamily `json:"target_family,omitempty"`
	// Check 仅核验同一逐流授权，不拨目标、不创建业务流。
	Check *controlpb.ForwardRule `json:"check,omitempty"`
}
type tunnelResult struct {
	OK    bool `json:"ok"`
	Ready bool `json:"ready,omitempty"`
	// 仅认证响应声明预检能力；旧节点缺省不支持。
	CheckSupported bool `json:"check_supported,omitempty"`
}

// 有界长度帧精确消费控制消息，不会吞掉紧随其后的业务字节。
func writeTunnelMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > 4096 {
		return errors.New("隧道消息过大")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	if _, err = w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
func readTunnelMessage(r io.Reader, v any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > 4096 {
		return errors.New("隧道消息长度无效")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// 能力绑定到经过 TLS/凭据认证的具体会话，重连时重新协商。
type tunnelSession struct {
	*yamux.Session
	checkSupported bool
}

// 每个入口—出口复用一个 WSS/yamux 会话。变更只淘汰对应出口会话。
type tunnelClient struct {
	mu       tunnelMutex
	openMu   tunnelMutex
	endpoint *controlpb.TunnelEndpoint
	session  *tunnelSession
	engine   *Engine
	closed   bool
}

func (c *tunnelClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.session != nil {
		c.session.Close()
	}
}
func (c *tunnelClient) getSession(ctx context.Context) (*tunnelSession, error) {
	if err := c.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer c.mu.Unlock()
	if c.closed {
		return nil, net.ErrClosed
	}
	if c.session != nil && !c.session.IsClosed() {
		return c.session, nil
	}
	ep := c.endpoint
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ep.CertificatePem) {
		return nil, errors.New("出口证书无效")
	}
	pinned, err := tunnelCertificate(ep.CertificatePem, ep.ServerName)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: ep.ServerName, MinVersion: tls.VersionTLS12, Time: c.engine.deps.Now, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, pinned.Raw) {
			return errors.New("出口证书与固定信任不匹配")
		}
		return nil
	}}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return c.engine.deps.DialTunnel(ctx, address)
	}}
	// 不继承环境代理，不允许重定向或 WSS 降级。
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("隧道不允许重定向") }}
	defer transport.CloseIdleConnections()
	ws, response, err := websocket.Dial(ctx, "wss://"+net.JoinHostPort(ep.Address, strconv.Itoa(int(ep.Port)))+tunnelPath, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{tunnelSubprotocol}})
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, errors.New("出口 WSS 握手失败")
	}
	if ws.Subprotocol() != tunnelSubprotocol {
		ws.CloseNow()
		return nil, errors.New("隧道协议不兼容")
	}
	conn := newTunnelCarrier(context.Background(), ws)
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stopAuth := context.AfterFunc(ctx, func() { ws.CloseNow() })
	defer stopAuth()
	// websocket.Dial 已完成完整 TLS 身份验证；凭据此时才进入加密通道。
	if err := writeTunnelMessage(conn, tunnelAuth{Version: 1, EntryID: ep.EntryId, Credential: ep.Credential}); err != nil {
		return nil, err
	}
	var result tunnelResult
	if err := readTunnelMessage(conn, &result); err != nil || !result.OK {
		return nil, errors.New("入口—出口凭据被拒绝")
	}
	if !stopAuth() || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	conn.SetDeadline(time.Time{})
	session, err := yamux.Client(conn, c.engine.yamuxClientConfig(), nil)
	if err != nil {
		return nil, err
	}
	c.session = &tunnelSession{Session: session, checkSupported: result.CheckSupported}
	ok = true
	c.engine.tunnelConnections.Add(1)
	go func() { <-session.CloseChan(); c.engine.tunnelConnections.Add(^uint64(0)) }()
	return c.session, nil
}
func (c *tunnelClient) dial(ctx context.Context, rule *controlpb.ForwardRule) (net.Conn, error) {
	return c.open(ctx, streamAuthorization(c.endpoint.EntryId, rule))
}

func (c *tunnelClient) ready(ctx context.Context, rule *controlpb.ForwardRule) error {
	ctx, cancel := c.engine.tunnelTimeout(ctx, c.engine.resources.limits.TunnelHandshakeTimeout)
	defer cancel()
	// 预检也预占入口的节点/规则/待拨号预算；同机出口沿用已认证身份去重。
	lease := c.engine.resources.acquire(rule.Id)
	if lease == nil {
		return errTunnelCapacity
	}
	defer lease.release()
	request := streamAuthorization(c.endpoint.EntryId, rule)
	request.Check = rule
	stream, err := c.open(ctx, request)
	if stream != nil {
		stream.Close()
	}
	return err
}

func (c *tunnelClient) open(ctx context.Context, request tunnelRequest) (net.Conn, error) {
	ctx, cancel := c.engine.tunnelTimeout(ctx, c.engine.resources.limits.DialTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handshake, stop := c.engine.tunnelTimeout(ctx, c.engine.resources.limits.TunnelHandshakeTimeout)
	session, err := c.getSession(handshake)
	stop()
	if err != nil {
		return nil, err
	}
	if request.Check != nil && !session.checkSupported {
		// 旧出口不识别 Check，发送它会误创建业务流。旧端沿用票09的
		// 面板授权确认门禁/既有零代次兼容；新业务流仍由出口逐流授权。
		return nil, ctx.Err()
	}
	stream, err := c.openStream(ctx, session.Session)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			(&tunnelStream{Stream: stream, engine: c.engine}).Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	stream.SetDeadline(deadline)
	stopCancel := context.AfterFunc(ctx, func() { stream.SetDeadline(time.Now()) })
	defer stopCancel()
	if err := writeTunnelMessage(stream, request); err != nil {
		return nil, err
	}
	var result tunnelResult
	if err := readTunnelMessage(stream, &result); err != nil || !result.OK {
		return nil, errors.New("出口拒绝规则或无法连接目标")
	}
	if request.Check != nil && !result.Ready {
		return nil, errors.New("出口未确认规则授权")
	}
	if !stopCancel() || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stream.SetDeadline(time.Time{})
	ok = true
	return &tunnelStream{Stream: stream, engine: c.engine}, nil
}

// CloseWrite 保留反向数据，Close 用 RST 立即回收本地与远端流。
type tunnelStream struct {
	*yamux.Stream
	engine    *Engine
	closeOnce sync.Once
	closeErr  error
}

func (s *tunnelStream) CloseWrite() error { return s.control(s.Stream.CloseWrite) }
func (s *tunnelStream) Close() error {
	// Reset 会先唤醒读写、再删除流槽；并发关闭必须等同一次完整清理结束。
	s.closeOnce.Do(func() { s.closeErr = s.control(s.Stream.Reset) })
	return s.closeErr
}

type tunnelServer struct {
	engine      *Engine
	mu          sync.Mutex
	config      *controlpb.TunnelServer
	http        *http.Server
	listener    net.Listener
	ctx         context.Context
	cancel      context.CancelFunc
	connections map[net.Conn]struct{}
	closed      bool
	wg          sync.WaitGroup
}

func (e *Engine) startTunnelServer(config *controlpb.TunnelServer) (*tunnelServer, error) {
	cert, err := tls.X509KeyPair(config.CertificatePem, config.PrivateKeyPem)
	if err != nil {
		return nil, errors.New("出口证书或私钥无效")
	}
	listener, err := e.deps.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(config.ListenPort))))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &tunnelServer{engine: e, config: proto.Clone(config).(*controlpb.TunnelServer), listener: listener, ctx: ctx, cancel: cancel, connections: map[net.Conn]struct{}{}}
	s.http = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: e.resources.limits.TunnelHandshakeTimeout, ErrorLog: log.New(io.Discard, "", 0), TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, Time: e.deps.Now}}
	go s.http.Serve(tls.NewListener(listener, s.http.TLSConfig))
	return s, nil
}
func (s *tunnelServer) update(config *controlpb.TunnelServer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = proto.Clone(config).(*controlpb.TunnelServer)
}
func (s *tunnelServer) close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	for conn := range s.connections {
		conn.Close()
	}
	s.mu.Unlock()
	s.http.Close()
	// Close 可能早于 Serve 注册监听器；仍须同步释放实际端口供重启恢复。
	s.listener.Close()
	s.wg.Wait()
}
func (s *tunnelServer) authorized(entry, credential, ruleID string) *controlpb.ForwardRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	peerOK := false
	for _, peer := range s.config.Peers {
		if peer.EntryId == entry && subtle.ConstantTimeCompare([]byte(peer.Credential), []byte(credential)) == 1 {
			peerOK = true
			break
		}
	}
	if !peerOK {
		return nil
	}
	if ruleID == "" {
		return &controlpb.ForwardRule{}
	}
	for _, grant := range s.config.Grants {
		if grant.EntryId == entry && grant.Rule.Id == ruleID {
			return proto.Clone(grant.Rule).(*controlpb.ForwardRule)
		}
	}
	return nil
}
func (s *tunnelServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || r.URL.Path != tunnelPath || r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{tunnelSubprotocol}})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	if ws.Subprotocol() != tunnelSubprotocol {
		return
	}
	conn := newTunnelCarrier(s.ctx, ws)
	defer conn.Close()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.connections[conn] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.connections, conn); s.mu.Unlock(); s.wg.Done() }()
	authCtx, stopAuthTimer := s.engine.tunnelTimeout(s.ctx, s.engine.resources.limits.TunnelHandshakeTimeout)
	defer stopAuthTimer()
	stopAuth := context.AfterFunc(authCtx, func() { ws.CloseNow() })
	defer stopAuth()
	conn.SetDeadline(time.Now().Add(s.engine.resources.limits.TunnelHandshakeTimeout))
	var auth tunnelAuth
	if err := readTunnelMessage(conn, &auth); err != nil || auth.Version != 1 || s.authorized(auth.EntryID, auth.Credential, "") == nil {
		writeTunnelMessage(conn, tunnelResult{})
		return
	}
	if err := writeTunnelMessage(conn, tunnelResult{OK: true, CheckSupported: true}); err != nil {
		return
	}
	if !stopAuth() || authCtx.Err() != nil {
		return
	}
	stopAuthTimer()
	conn.SetDeadline(time.Time{})
	session, err := yamux.Server(conn, s.engine.yamuxConfig(), nil)
	if err != nil {
		return
	}
	defer session.Close()
	s.engine.tunnelConnections.Add(1)
	defer s.engine.tunnelConnections.Add(^uint64(0))
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	go func() {
		select {
		case <-session.CloseChan():
			cancel()
		case <-ctx.Done():
		}
	}()
	var streams sync.WaitGroup
	defer streams.Wait()
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		var lease *resourceLease
		if auth.EntryID != s.engine.deps.NodeID {
			lease = s.engine.resources.reserveTunnel()
			if lease == nil {
				(&tunnelStream{Stream: stream, engine: s.engine}).Close()
				continue
			}
		}
		streams.Add(1)
		go func() {
			defer streams.Done()
			s.serveLimitedStream(ctx, &tunnelStream{Stream: stream, engine: s.engine}, auth, lease)
		}()
	}
}
func proxyTCP(client, target net.Conn) {
	done := make(chan struct{}, 1)
	copyHalf := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if closer, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		if err != nil {
			client.Close()
			target.Close()
		}
	}
	go func() { copyHalf(target, client); done <- struct{}{} }()
	copyHalf(client, target)
	<-done
}
