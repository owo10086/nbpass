package forwarding

import (
	"context"
	"errors"
	"io"
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
			stream, err := session.OpenStream(context.Background())
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

// 授权在拨号返回时已撤销、但还没到下一秒复查，也不能交付成功响应。
// 拨号失败同样保留明确拒绝响应；两条路径都必须释放目标和共享预算。
func TestTunnelRejectsAfterTargetDialAndReleasesBudget(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "dial_failure"
		if revoke {
			name = "revoked_before_confirmation"
		}
		t.Run(name, func(t *testing.T) {
			client, stream := net.Pipe()
			defer client.Close()
			target, peer := net.Pipe()
			defer target.Close()
			defer peer.Close()
			rule := &controlpb.ForwardRule{Id: "r", AuthorizationGeneration: 7, Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, TargetAddress: "127.0.0.1", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, AllowPrivate: true}
			auth := tunnelAuth{EntryID: "entry", Credential: strings.Repeat("a", 43)}
			config := &controlpb.TunnelServer{Peers: []*controlpb.TunnelPeer{{EntryId: auth.EntryID, Credential: auth.Credential}}, Grants: []*controlpb.TunnelGrant{{EntryId: auth.EntryID, Rule: rule}}}
			server := &tunnelServer{config: config}
			engine := NewEngine(Dependencies{Limits: ResourceLimits{MaxConnections: 1, MaxConnectionsPerRule: 1, MaxPendingDials: 1}, DialTarget: func(context.Context, string) (net.Conn, error) {
				if !revoke {
					return nil, errors.New("目标不可达")
				}
				server.update(&controlpb.TunnelServer{Peers: config.Peers})
				return target, nil
			}})
			server.engine = engine
			lease := engine.resources.reserveTunnel()
			if lease == nil {
				t.Fatal("未预占出口资源")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				server.serveLimitedStream(context.Background(), stream, auth, lease)
			}()
			defer func() { client.Close(); <-done }()
			client.SetDeadline(time.Now().Add(2 * time.Second))
			request := tunnelRequest{RuleID: "r", EntryID: "entry", Generation: 7, TargetAddress: "127.0.0.1", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}
			if err := writeTunnelMessage(client, request); err != nil {
				t.Fatal(err)
			}
			var result tunnelResult
			if err := readTunnelMessage(client, &result); err != nil {
				t.Fatalf("未收到完整拒绝响应: %v", err)
			}
			if result.OK {
				t.Fatal("目标拨号失败或授权已撤销仍交付成功响应")
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("拒绝后流生命周期未退出")
			}
			if revoke {
				peer.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
					t.Fatalf("已拨目标未关闭: %v", err)
				}
			}
			if business, _ := engine.Connections(); business != 0 {
				t.Fatalf("拒绝后仍计入业务连接: %d", business)
			}
			// 节点、规则和待拨号三个上限均为 1，任一槽未释放都会拒绝。
			next := engine.resources.acquire("r")
			if next == nil {
				t.Fatal("拒绝后未归还共享预算")
			}
			next.release()
		})
	}
}
