package forwarding

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

// 即使节点对凭据正确，也不能用其他身份、旧代次或自选目标建立业务流。
func TestTunnelPerStreamAuthorizationRejectsForgedRequests(t *testing.T) {
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	var dials atomic.Int32
	e := NewEngine(Dependencies{DialTarget: func(context.Context, string) (net.Conn, error) {
		dials.Add(1)
		a, b := net.Pipe()
		go b.Close()
		return a, nil
	}})
	rule := &controlpb.ForwardRule{Id: "r", AuthorizationGeneration: 7, Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, TargetAddress: "127.0.0.1", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: 443, AllowPrivate: true}
	server, err := e.startTunnelServer(&controlpb.TunnelServer{CertificatePem: cert, PrivateKeyPem: key, Peers: []*controlpb.TunnelPeer{{EntryId: "entry", Credential: strings.Repeat("a", 43)}}, Grants: []*controlpb.TunnelGrant{{EntryId: "entry", Rule: rule}}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	client := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: uint32(server.listener.Addr().(*net.TCPAddr).Port), ServerName: "exit.test", CertificatePem: cert, EntryId: "entry", Credential: strings.Repeat("a", 43)}}
	defer client.close()
	session, err := client.getSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"entry_id", "generation", "target_address", "target_port", "target_family", "rule_id", "missing_generation"} {
		t.Run(field, func(t *testing.T) {
			request := map[string]any{"rule_id": "r", "entry_id": "entry", "generation": 7, "target_address": "127.0.0.1", "target_port": 443, "target_family": 1}
			switch field {
			case "entry_id":
				request[field] = "another-entry"
			case "generation":
				request[field] = 6
			case "target_address":
				request[field] = "127.0.0.2"
			case "target_port":
				request[field] = 444
			case "target_family":
				request[field] = 0
			case "rule_id":
				request[field] = "other-rule"
			case "missing_generation":
				delete(request, "generation")
			}
			stream, err := session.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			stream.SetDeadline(time.Now().Add(time.Second))
			if err := writeTunnelMessage(stream, request); err != nil {
				t.Fatal(err)
			}
			var result tunnelResult
			if err := readTunnelMessage(stream, &result); err != nil {
				t.Fatal(err)
			}
			if result.OK || dials.Load() != 0 {
				t.Fatal("伪造逐流请求通过授权或触发目标拨号")
			}
		})
	}
}
