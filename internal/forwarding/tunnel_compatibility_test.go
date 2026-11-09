package forwarding

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// 固定 main09 的线协议：认证响应只有 ok，没有预检能力；任何流都被当作业务流。
// 若新入口无协商就发 Check，会误拨目标，并因没有 Ready 响应而永远拒绝监听。
func TestTunnelRollingUpgradeDoesNotProbeLegacyExit(t *testing.T) {
	for _, generation := range []uint64{0, 7} {
		t.Run(strconv.FormatUint(generation, 10), func(t *testing.T) {
			deps := Dependencies{}
			snapshot := cachedTunnelFixture(t, &deps)
			rule := proto.Clone(snapshot.Rules[0]).(*controlpb.ForwardRule)
			rule.AuthorizationGeneration = generation
			snapshot.Rules[0].AuthorizationGeneration = generation
			// 代表已有授权仅修改入口监听端口，授权代次和目标保持不变。
			snapshot.Rules[0].ListenPort = reserveTestPort(t, &deps)
			serverConfig := snapshot.TunnelServer
			pair, err := tls.X509KeyPair(serverConfig.CertificatePem, serverConfig.PrivateKeyPem)
			if err != nil {
				t.Fatal(err)
			}
			var businessStreams atomic.Int32
			legacy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{tunnelSubprotocol}})
				if err != nil {
					return
				}
				defer ws.CloseNow()
				conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
				var auth tunnelAuth
				if readTunnelMessage(conn, &auth) != nil || auth.EntryID != "node" || auth.Credential != serverConfig.Peers[0].Credential {
					return
				}
				if writeTunnelMessage(conn, struct {
					OK bool `json:"ok"`
				}{true}) != nil {
					return
				}
				session, err := yamux.Server(conn, yamuxConfig())
				if err != nil {
					return
				}
				defer session.Close()
				var streams sync.WaitGroup
				defer streams.Wait()
				for {
					stream, err := session.AcceptStream()
					if err != nil {
						return
					}
					streams.Add(1)
					go func() {
						defer streams.Done()
						defer stream.Close()
						stream.SetDeadline(time.Now().Add(time.Second))
						// 故意不声明 Check，模拟旧节点忽略未知 JSON 字段。
						var request struct {
							RuleID        string                  `json:"rule_id"`
							EntryID       string                  `json:"entry_id"`
							Generation    uint64                  `json:"generation"`
							TargetAddress string                  `json:"target_address"`
							TargetPort    uint32                  `json:"target_port"`
							TargetFamily  controlpb.AddressFamily `json:"target_family"`
						}
						if readTunnelMessage(stream, &request) != nil || request.RuleID != rule.Id || request.EntryID != auth.EntryID || request.Generation != rule.AuthorizationGeneration || request.TargetAddress != rule.TargetAddress || request.TargetPort != rule.TargetPort || request.TargetFamily != rule.TargetFamily {
							return
						}
						businessStreams.Add(1)
						target, err := net.DialTimeout("tcp4", net.JoinHostPort(rule.TargetAddress, strconv.Itoa(int(rule.TargetPort))), time.Second)
						if err != nil {
							return
						}
						defer target.Close()
						if writeTunnelMessage(stream, struct {
							OK bool `json:"ok"`
						}{true}) != nil {
							return
						}
						stream.SetDeadline(time.Time{})
						proxyTCP(&tunnelStream{Stream: stream}, target)
					}()
				}
			}))
			legacy.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
			legacy.StartTLS()
			defer legacy.Close()
			snapshot.ExitGroups[0].Exits[0].Port = uint32(legacy.Listener.Addr().(*net.TCPAddr).Port)
			snapshot.TunnelServer = nil
			snapshot.Digest = SnapshotDigest(snapshot)
			entry := NewEngine(deps)
			defer entry.Close()
			if err := entry.Apply(snapshot); err != nil {
				t.Fatalf("旧出口阻塞滚动升级入口: %v", err)
			}
			if businessStreams.Load() != 0 {
				t.Fatal("授权预检向旧出口创建了业务流")
			}
			conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(snapshot.Rules[0].ListenPort))), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(time.Second))
			if _, err := conn.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 4)
			if _, err := io.ReadFull(conn, data); err != nil || string(data) != "ping" {
				t.Fatalf("旧出口业务不通: %q %v", data, err)
			}
			if businessStreams.Load() != 1 {
				t.Fatalf("业务流数量错误: %d", businessStreams.Load())
			}
		})
	}
}

// 票22旧入口发送 check:{完整规则}，新出口须接受旧对象线格式，
// 但仍按同一授权校验拒绝旧目标，且不能因为监听端口元数据不同拒绝合法预检。
func TestTunnelAcceptsLegacyRuleObjectReadinessCheck(t *testing.T) {
	deps := Dependencies{}
	snapshot := cachedTunnelFixture(t, &deps)
	var targetDials atomic.Int32
	deps.DialTarget = func(context.Context, string) (net.Conn, error) {
		targetDials.Add(1)
		return nil, net.ErrClosed
	}
	engine := NewEngine(deps)
	server, err := engine.startTunnelServer(snapshot.TunnelServer)
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	client := &tunnelClient{engine: engine, endpoint: snapshot.ExitGroups[0].Exits[0]}
	defer client.close()
	session, err := client.getSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{true, false} {
		check := proto.Clone(snapshot.Rules[0]).(*controlpb.ForwardRule)
		check.ListenPort++
		if !allowed {
			check.TargetPort++
		}
		stream, err := session.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		stream.SetDeadline(time.Now().Add(time.Second))
		if err := writeTunnelMessage(stream, map[string]any{"rule_id": check.Id, "check": check}); err != nil {
			t.Fatal(err)
		}
		var result tunnelResult
		if err := readTunnelMessage(stream, &result); err != nil {
			t.Fatalf("旧入口预检响应失败: %v", err)
		}
		if result.OK != allowed || result.Ready != allowed {
			t.Fatalf("旧入口授权校验错误: allowed=%v result=%+v", allowed, result)
		}
	}
	if targetDials.Load() != 0 {
		t.Fatal("旧入口预检创建了目标连接")
	}
}
