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
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

const tunnelPath = "/nbpass/tunnel/v1"
const tunnelSubprotocol = "nbpass.tunnel.v1"

type tunnelAuth struct {
	Probe      bool   `json:"probe,omitempty"`
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
}
type tunnelResult struct {
	OK      bool         `json:"ok"`
	Failure failureClass `json:"failure,omitempty"`
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
func yamuxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.StreamOpenTimeout = 3 * time.Second
	c.ConnectionWriteTimeout = 3 * time.Second
	return c
}

// 每个入口—出口复用一个 WSS/yamux 会话。变更只淘汰对应出口会话。
type tunnelClient struct {
	healthMu      sync.Mutex
	unhealthy     bool
	mu            sync.Mutex
	endpoint      *controlpb.TunnelEndpoint
	session       *yamux.Session
	engine        *Engine
	closed        bool
	failures      uint32
	recoverySince time.Time
	lastSuccess   time.Time
	checkedAt     time.Time
	lastProbe     string
	failure       failureClass
	probeCancel   context.CancelFunc
	probeDone     chan struct{}
	failureEpoch  uint64
	sessionGate   chan struct{}
	gateOnce      sync.Once
}

func (c *tunnelClient) close() {
	if c.probeCancel != nil {
		c.probeCancel()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.session != nil {
		c.session.Close()
	}
}
func (c *tunnelClient) getSession(ctx context.Context) (*yamux.Session, error) {
	c.gateOnce.Do(func() { c.sessionGate = make(chan struct{}, 1) })
	select {
	case c.sessionGate <- struct{}{}:
	case <-ctx.Done():
		return nil, failTunnel(failureLocal, ctx.Err())
	}
	defer func() { <-c.sessionGate }()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, failTunnel(failureLocal, net.ErrClosed)
	}
	if c.session != nil && !c.session.IsClosed() {
		session := c.session
		c.mu.Unlock()
		return session, nil
	}
	c.mu.Unlock()
	session, err := c.openSession(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		session.Close()
		return nil, failTunnel(failureLocal, net.ErrClosed)
	}
	c.session = session
	c.engine.tunnelConnections.Add(1)
	go func() { <-session.CloseChan(); c.engine.tunnelConnections.Add(^uint64(0)) }()
	return session, nil
}

// 探测始终走新的 TLS/WSS/凭据握手，不借用已有业务会话。
func (c *tunnelClient) openSession(ctx context.Context, probe ...bool) (*yamux.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	handshakeCtx := ctx
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
			return failTunnel(failureConfig, errors.New("出口证书与固定信任不匹配"))
		}
		return nil
	}}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(handshakeCtx, cancel)
		defer stop()
		defer cancel()
		return c.engine.deps.DialTunnel(dialCtx, address)
	}}
	// 不继承环境代理，不允许重定向或 WSS 降级。
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("隧道不允许重定向") }}
	defer transport.CloseIdleConnections()
	ws, response, err := websocket.Dial(ctx, "wss://"+net.JoinHostPort(ep.Address, strconv.Itoa(int(ep.Port)))+tunnelPath, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{tunnelSubprotocol}})
	if err != nil {
		class := failureLink
		if response != nil {
			if response.StatusCode < 500 {
				class = failureConfig
			}
			if response.Body != nil {
				response.Body.Close()
			}
		}
		return nil, failTunnel(class, err)
	}
	if ws.Subprotocol() != tunnelSubprotocol {
		ws.CloseNow()
		return nil, failTunnel(failureConfig, errors.New("隧道协议不兼容"))
	}
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	ok := false
	defer func() {
		if !ok {
			ws.CloseNow()
		}
	}()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { ws.CloseNow() })
	defer stop()
	// websocket.Dial 已完成完整 TLS 身份验证；凭据此时才进入加密通道。
	if err := writeTunnelMessage(conn, tunnelAuth{Probe: len(probe) > 0 && probe[0], Version: 1, EntryID: ep.EntryId, Credential: ep.Credential}); err != nil {
		return nil, err
	}
	var result tunnelResult
	if err := readTunnelMessage(conn, &result); err != nil {
		return nil, failTunnel(failureLink, err)
	}
	if !result.OK {
		return nil, failTunnel(failureConfig, errors.New("入口—出口凭据被拒绝"))
	}
	session, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		return nil, err
	}
	if _, err := session.Ping(); err != nil {
		session.Close()
		return nil, failTunnel(failureLink, err)
	}
	if !stop() || ctx.Err() != nil {
		session.Close()
		return nil, failTunnel(failureLink, ctx.Err())
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return session, nil
}
func (c *tunnelClient) dial(ctx context.Context, rule *controlpb.ForwardRule) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	handshake, stop := context.WithTimeout(ctx, 3*time.Second)
	session, err := c.getSession(handshake)
	stop()
	if err != nil {
		return nil, streamFailure(session, err)
	}
	stream, err := openTunnelStream(ctx, session)
	if err != nil {
		return nil, streamFailure(session, err)
	}
	ok := false
	defer func() {
		if !ok {
			stream.SetReadDeadline(time.Now())
			go stream.Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	stream.SetDeadline(deadline)
	stopCancel := context.AfterFunc(ctx, func() { stream.SetDeadline(time.Now()) })
	defer stopCancel()
	result, err := exchangeTunnelRequest(ctx, stream, streamAuthorization(c.endpoint.EntryId, rule))
	if err != nil {
		return nil, streamFailure(session, err)
	}
	if !result.OK {
		class := result.Failure
		if class != failureTarget && class != failureLocal && class != failureConfig {
			class = failureConfig
		} // 旧出口的模糊拒绝不能污染健康。
		return nil, failTunnel(class, errors.New("出口拒绝业务流"))
	}
	if !stopCancel() || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stream.SetDeadline(time.Time{})
	ok = true
	return &tunnelStream{Stream: stream}, nil
}

// yamux 的写操作并非所有分支都检查 deadline；逐流授权也受剩余总预算限制。
func exchangeTunnelRequest(ctx context.Context, stream *yamux.Stream, request tunnelRequest) (tunnelResult, error) {
	type response struct {
		result tunnelResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		var result tunnelResult
		if err := writeTunnelMessage(stream, request); err != nil {
			done <- response{result, err}
			return
		}
		err := readTunnelMessage(stream, &result)
		done <- response{result, err}
	}()
	select {
	case <-ctx.Done():
		stream.SetDeadline(time.Now())
		return tunnelResult{}, failTunnel(failureLocal, ctx.Err())
	case r := <-done:
		return r.result, r.err
	}
}

// yamux 的 OpenStream 不接收 context；等待只占剩余预算，迟到流在后台回收。
// 不关闭共享会话，避免一次新流超时中断其他已建立业务。强制 reset 由票11接续。
func openTunnelStream(ctx context.Context, session *yamux.Session) (*yamux.Stream, error) {
	type result struct {
		stream *yamux.Stream
		err    error
	}
	done := make(chan result)
	go func() {
		stream, err := session.OpenStream()
		select {
		case done <- result{stream, err}:
		case <-ctx.Done():
			if stream != nil {
				stream.Close()
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, failTunnel(failureLocal, ctx.Err())
	case r := <-done:
		return r.stream, r.err
	}
}

func streamFailure(session *yamux.Session, err error) error {
	class := failureConfig
	if session == nil || session.IsClosed() {
		class = failureLink
	}
	return failTunnel(class, err)
}

// yamux.Close 只关闭写方向；CloseWrite 保持反向读取，Close 同时解除本地读阻塞。
// 强制流重置及完整取消清理由票 11 在此接续。
type tunnelStream struct{ *yamux.Stream }

func (s *tunnelStream) CloseWrite() error { return s.Stream.Close() }
func (s *tunnelStream) Close() error      { s.Stream.SetReadDeadline(time.Now()); return s.Stream.Close() }

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
	s.http = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: 3 * time.Second, ErrorLog: log.New(io.Discard, "", 0), TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12, Time: e.deps.Now}}
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
	conn := websocket.NetConn(s.ctx, ws, websocket.MessageBinary)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.connections[conn] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.connections, conn); s.mu.Unlock(); s.wg.Done() }()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	var auth tunnelAuth
	if err := readTunnelMessage(conn, &auth); err != nil || auth.Version != 1 || s.authorized(auth.EntryID, auth.Credential, "") == nil {
		writeTunnelMessage(conn, tunnelResult{})
		return
	}
	if err := writeTunnelMessage(conn, tunnelResult{OK: true}); err != nil {
		return
	}
	conn.SetDeadline(time.Time{})
	session, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		return
	}
	defer session.Close()
	if !auth.Probe {
		s.engine.tunnelConnections.Add(1)
		defer s.engine.tunnelConnections.Add(^uint64(0))
	}
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
		if auth.Probe {
			stream.Close()
			return
		} // 探测载体不承载业务，也不计业务/载体指标。
		streams.Add(1)
		go func() { defer streams.Done(); s.serveStream(ctx, &tunnelStream{Stream: stream}, auth) }()
	}
}
func (s *tunnelServer) serveStream(ctx context.Context, stream net.Conn, auth tunnelAuth) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer stream.Close()
	stream.SetDeadline(time.Now().Add(10 * time.Second))
	stop := context.AfterFunc(ctx, func() { stream.Close() })
	defer stop()
	var request tunnelRequest
	if err := readTunnelMessage(stream, &request); err != nil || request.RuleID == "" {
		return
	}
	rule := s.authorizeStream(auth, request)
	if rule == nil {
		writeTunnelMessage(stream, tunnelResult{Failure: failureConfig})
		return
	}
	// 在目标拨号前开始复查，撤销同样取消尚未完成的 DNS/拨号。
	stopAuthorization := s.watchStreamAuthorization(ctx, cancel, stream, auth, request)
	defer stopAuthorization()
	// 本机入口已计量的流不重复计数；其他入口交来的业务在出口计一次。
	if auth.EntryID != s.engine.deps.NodeID {
		s.engine.businessConnections.Add(1)
		defer s.engine.businessConnections.Add(^uint64(0))
	}
	// 目标只来自出口配置，入口不能指定解析结果或任意地址。
	target, err := s.engine.dialTarget(ctx, rule)
	if err != nil {
		writeTunnelMessage(stream, tunnelResult{Failure: classifyTunnel(failTunnel(failureTarget, err))})
		return
	}
	defer target.Close()
	if ctx.Err() != nil || s.authorizeStream(auth, request) == nil {
		writeTunnelMessage(stream, tunnelResult{Failure: failureConfig})
		return
	}
	if err := writeTunnelMessage(stream, tunnelResult{OK: true}); err != nil {
		return
	}
	stream.SetDeadline(time.Time{})
	stopTarget := context.AfterFunc(ctx, func() { target.Close() })
	defer stopTarget()
	proxyTCP(stream, target)
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
