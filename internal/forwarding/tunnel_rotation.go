package forwarding

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"sync"

	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// 轮换只改变信任和凭据，不重启规则监听。地址、身份或成员变化仍按原语义撤销。
func sameTunnelRoute(a, b *controlpb.TunnelEndpoint) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.RotationId == "" && b.RotationId == "" {
		return proto.Equal(a, b)
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
	return proto.Equal(a, b)
}

func (c *tunnelClient) updateEndpoint(ep *controlpb.TunnelEndpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if proto.Equal(c.endpoint, ep) {
		return
	}
	if c.session != nil {
		if c.users[c.session] == 0 {
			c.session.Close()
		} // 无业务流的载体立即回收。
		c.session = nil
	}
	c.endpoint = proto.Clone(ep).(*controlpb.TunnelEndpoint)
}
func (c *tunnelClient) acquireSession(ctx context.Context) (*yamux.Session, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, err := c.getSessionLocked(ctx)
	if err != nil {
		return nil, "", err
	}
	if c.users == nil {
		c.users = map[*yamux.Session]int{}
	}
	c.users[session]++
	return session, c.endpoint.EntryId, nil
}
func (c *tunnelClient) releaseSession(session *yamux.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.users[session]--
	if c.users[session] == 0 {
		delete(c.users, session)
		if session != c.session {
			session.Close()
		}
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

// 已通过认证的业务流继续按当前入口归属、代次和目标复查；轮换不撤销其授权。
func (s *tunnelServer) authorizeExistingStream(auth tunnelAuth, request tunnelRequest) *controlpb.ForwardRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
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
			if rule != nil {
				return proto.Clone(rule).(*controlpb.ForwardRule)
			}
		}
	}
	return nil
}
