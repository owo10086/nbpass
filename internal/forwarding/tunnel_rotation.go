package forwarding

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"sync"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

func sameEndpointConfiguration(a, b *controlpb.TunnelEndpoint) bool {
	a = proto.Clone(a).(*controlpb.TunnelEndpoint)
	b = proto.Clone(b).(*controlpb.TunnelEndpoint)
	a.AddressId = NormalizeAddressID(a.AddressId)
	b.AddressId = NormalizeAddressID(b.AddressId)
	return proto.Equal(a, b)
}

// 轮换只改变信任和凭据，不重启规则监听。地址、身份或成员变化仍按原语义撤销。
func sameTunnelRoute(a, b *controlpb.TunnelEndpoint) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.RotationId == "" && b.RotationId == "" {
		return sameEndpointConfiguration(a, b)
	}
	a = proto.Clone(a).(*controlpb.TunnelEndpoint)
	b = proto.Clone(b).(*controlpb.TunnelEndpoint)
	a.CertificatePem = nil
	a.TrustedCertificatesPem = nil
	a.Credential = ""
	a.RotationId = ""
	b.CertificatePem = nil
	b.TrustedCertificatesPem = nil
	b.Credential = ""
	b.RotationId = ""
	return sameEndpointConfiguration(a, b)
}

func sameTunnelGroupRoutes(a, b *controlpb.ExitGroup) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Id != b.Id || a.Strategy != b.Strategy || len(a.Exits) != len(b.Exits) {
		return false
	}
	for i := range a.Exits {
		if !sameTunnelRoute(a.Exits[i], b.Exits[i]) {
			return false
		}
	}
	return true
}

// 端点发布后不再修改；健康探测和业务拨号各自取一致的材料快照。
func (c *tunnelClient) currentEndpoint() *controlpb.TunnelEndpoint {
	c.endpointMu.RLock()
	defer c.endpointMu.RUnlock()
	return c.endpoint
}

func (c *tunnelClient) updateEndpoint(ep *controlpb.TunnelEndpoint) {
	if proto.Equal(c.currentEndpoint(), ep) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if proto.Equal(c.endpoint, ep) {
		return
	}
	c.usersMu.Lock()
	old := c.session
	closeOld := old != nil && c.users[old] == 0
	c.session = nil
	c.usersMu.Unlock()
	if closeOld {
		old.Close() // 无业务流的载体立即回收。
	}
	c.endpointMu.Lock()
	c.endpoint = proto.Clone(ep).(*controlpb.TunnelEndpoint)
	c.endpointMu.Unlock()
}
func (c *tunnelClient) acquireSession(ctx context.Context) (*tunnelSession, error) {
	if err := c.mu.LockContext(ctx); err != nil {
		return nil, failTunnel(failureLocal, err)
	}
	defer c.mu.Unlock()
	session, err := c.getSessionLocked(ctx)
	if err != nil {
		return nil, err
	}
	c.usersMu.Lock()
	defer c.usersMu.Unlock()
	if c.users == nil {
		c.users = map[*tunnelSession]int{}
	}
	c.users[session]++
	return session, nil
}
func (c *tunnelClient) releaseSession(session *tunnelSession) {
	// 引用归还不能排在另一载体的网络握手后面，避免突破调用方预算。
	c.usersMu.Lock()
	c.users[session]--
	closeOld := false
	if c.users[session] == 0 {
		delete(c.users, session)
		closeOld = session != c.session
	}
	c.usersMu.Unlock()
	if closeOld {
		session.Close()
	}
}

type rotatingTunnelStream struct {
	*tunnelStream
	once    sync.Once
	release func()
}

func (s *rotatingTunnelStream) Close() error {
	err := s.tunnelStream.Close()
	s.once.Do(s.release)
	return err
}

// TLS 配置在握手时读取当前证书。更新不会关闭监听或已有 WSS/yamux 连接。
func (s *tunnelServer) currentCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cert, err := tls.X509KeyPair(s.config.CertificatePem, s.config.PrivateKeyPem)
	return &cert, err
}

func validPeerCredential(peer *controlpb.TunnelPeer, credential string) bool {
	matched := subtle.ConstantTimeCompare([]byte(peer.Credential), []byte(credential))
	for _, extra := range peer.AdditionalCredentials {
		matched |= subtle.ConstantTimeCompare([]byte(extra), []byte(credential))
	}
	return matched == 1
}

// 普通凭据替换仍撤销已认证业务；只有显式轮换过渡保留会话的撤销代次。
// 代次只在出口进程内使用，不改变逐规则授权代次或离线缓存格式。
func (s *tunnelServer) initCredentialEpochs() {
	if s.credentialEpochs == nil {
		s.credentialEpochs = map[string]uint64{}
		for _, peer := range s.config.Peers {
			s.credentialEpochs[peer.EntryId] = 0
		}
	}
}

func (s *tunnelServer) updateCredentialEpochs(config *controlpb.TunnelServer) {
	s.initCredentialEpochs()
	previous := map[string]*controlpb.TunnelPeer{}
	for _, peer := range s.config.Peers {
		previous[peer.EntryId] = peer
	}
	next := map[string]uint64{}
	for _, peer := range config.Peers {
		epoch := s.credentialEpochs[peer.EntryId]
		if previous[peer.EntryId] == nil || s.config.RotationId == "" && config.RotationId == "" && !proto.Equal(previous[peer.EntryId], peer) {
			s.nextCredentialEpoch++
			epoch = s.nextCredentialEpoch
		}
		next[peer.EntryId] = epoch
	}
	s.credentialEpochs = next
}

func (s *tunnelServer) authenticate(auth *tunnelAuth) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.initCredentialEpochs()
	for _, peer := range s.config.Peers {
		if peer.EntryId == auth.EntryID && validPeerCredential(peer, auth.Credential) {
			auth.credentialEpoch = s.credentialEpochs[auth.EntryID]
			return true
		}
	}
	return false
}

// 已通过认证的业务流继续按当前入口归属、代次和目标复查；轮换不撤销其授权。
func (s *tunnelServer) authorizeExistingStream(auth tunnelAuth, request tunnelRequest) *controlpb.ForwardRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.initCredentialEpochs()
	if epoch, present := s.credentialEpochs[auth.EntryID]; !present || epoch != auth.credentialEpoch {
		return nil
	}
	peerPresent := false
	for _, peer := range s.config.Peers {
		if peer.EntryId == auth.EntryID {
			peerPresent = true
			break
		}
	}
	if !peerPresent {
		return nil
	}
	for _, grant := range s.config.Grants {
		if grant.EntryId == auth.EntryID && grant.Rule.Id == request.RuleID {
			rule := matchStreamAuthorization(grant.Rule, auth, request)
			if rule != nil && grantAllowsSelected(grant, request) {
				return proto.Clone(rule).(*controlpb.ForwardRule)
			}
		}
	}
	return nil
}
