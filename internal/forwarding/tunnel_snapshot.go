package forwarding

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/netip"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

func tunnelCertificate(data []byte, name string) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		return nil, errors.New("出口证书无效")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("出口证书无效")
	}
	if name == "" || cert.VerifyHostname(name) != nil || cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil {
		return nil, errors.New("出口证书身份或自签签名无效")
	}
	return cert, nil
}
func validateTunnelSnapshot(s *controlpb.ConfigSnapshot) error {
	invalid := errors.New("隧道配置无效")
	groups := map[string]bool{}
	endpoints := map[string]*controlpb.TunnelEndpoint{}
	for _, g := range s.ExitGroups {
		if g == nil || g.Id == "" || groups[g.Id] || g.Strategy != controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP || len(g.Exits) == 0 {
			return invalid
		}
		groups[g.Id] = true
		seen := map[string]bool{}
		for _, ep := range g.Exits {
			if ep == nil || ep.NodeId == "" || seen[ep.NodeId] || ep.EntryId == "" || len(ep.Credential) != 43 || ep.Port < 1 || ep.Port > 65535 || ep.Transport != controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS {
				return invalid
			}
			seen[ep.NodeId] = true
			ip, err := netip.ParseAddr(ep.Address)
			if err != nil || !AllowedIP(ip, true) {
				return invalid
			}
			if len(ep.TrustedCertificatesPem) > 1 {
				return invalid
			}
			for _, cert := range ep.TrustedCertificatesPem {
				if _, err := tunnelCertificate(cert, ep.ServerName); err != nil {
					return err
				}
			}
			if _, err := tunnelCertificate(ep.CertificatePem, ep.ServerName); err != nil {
				return err
			}
			if previous := endpoints[ep.NodeId]; previous != nil && !proto.Equal(previous, ep) {
				return invalid
			}
			endpoints[ep.NodeId] = ep
		}
	}
	for _, r := range s.Rules {
		if r.Kind == controlpb.RuleKind_RULE_KIND_TUNNEL {
			var group *controlpb.ExitGroup
			for _, g := range s.ExitGroups {
				if g.Id == r.ExitGroupId {
					group = g
					break
				}
			}
			if group == nil {
				return invalid
			}
			if len(group.Exits) > 1 && len(r.AuthorizedExitIds) == 0 {
				return invalid
			}
			seen := map[string]bool{}
			for _, id := range r.AuthorizedExitIds {
				found := false
				for _, ep := range group.Exits {
					found = found || ep.NodeId == id
				}
				if !found || seen[id] {
					return invalid
				}
				seen[id] = true
			}
		}
		if r.Kind == controlpb.RuleKind_RULE_KIND_TUNNEL && !groups[r.ExitGroupId] || r.Kind == controlpb.RuleKind_RULE_KIND_DIRECT && r.ExitGroupId != "" {
			return invalid
		}
	}
	if server := s.TunnelServer; server != nil {
		if server.ListenPort < 1 || server.ListenPort > 65535 || server.Transport != controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS {
			return invalid
		}
		if _, err := tunnelCertificate(server.CertificatePem, server.ServerName); err != nil {
			return err
		}
		if _, err := tls.X509KeyPair(server.CertificatePem, server.PrivateKeyPem); err != nil {
			return invalid
		}
		peers := map[string]bool{}
		for _, p := range server.Peers {
			if p == nil || p.EntryId == "" || peers[p.EntryId] || len(p.Credential) != 43 {
				return invalid
			}
			if len(p.AdditionalCredentials) > 1 {
				return invalid
			}
			for _, credential := range p.AdditionalCredentials {
				if len(credential) != 43 {
					return invalid
				}
			}
			peers[p.EntryId] = true
		}
		grants := map[string]bool{}
		for _, grant := range server.Grants {
			if grant == nil || !peers[grant.EntryId] || grant.Rule == nil {
				return invalid
			}
			r := grant.Rule
			key := grant.EntryId + ":" + r.Id
			if r.Id == "" || grants[key] || r.Kind != controlpb.RuleKind_RULE_KIND_TUNNEL || r.TargetFamily != controlpb.AddressFamily_ADDRESS_FAMILY_IPV4 || r.TargetPort < 1 || r.TargetPort > 65535 {
				return invalid
			}
			if err := ValidateTarget(r.TargetAddress, r.AllowPrivate); err != nil {
				return err
			}
			grants[key] = true
		}
	}
	return nil
}
