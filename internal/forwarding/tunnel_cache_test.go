package forwarding

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"nbpass/internal/controlpb"
)

func cacheTestPort(t *testing.T) uint32 {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	return port
}

func cachedTunnelFixture(t *testing.T) *controlpb.ConfigSnapshot {
	t.Helper()
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { target.Close() })
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	rule := &controlpb.ForwardRule{Id: "tunnel", EntryGroupId: "entry", Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, ExitGroupId: "exit", ListenPort: cacheTestPort(t), TargetAddress: "127.0.0.1", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), AllowPrivate: true}
	ep := &controlpb.TunnelEndpoint{NodeId: "node", EntryId: "node", Address: "127.0.0.1", Port: cacheTestPort(t), Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS, ServerName: "exit.test", CertificatePem: cert, Credential: strings.Repeat("a", 43)}
	s := &controlpb.ConfigSnapshot{Revision: 1, Rules: []*controlpb.ForwardRule{rule}, ExitGroups: []*controlpb.ExitGroup{{Id: "exit", Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP, Exits: []*controlpb.TunnelEndpoint{ep}}}, TunnelServer: &controlpb.TunnelServer{ListenPort: ep.Port, Transport: ep.Transport, ServerName: ep.ServerName, CertificatePem: cert, PrivateKeyPem: key, Peers: []*controlpb.TunnelPeer{{EntryId: "node", Credential: ep.Credential}}, Grants: []*controlpb.TunnelGrant{{EntryId: "node", Rule: rule}}}}
	s.Digest = SnapshotDigest(s)
	return s
}

// 缺少出口组、节点对凭据、出口私钥或授权中的任何一项都会使真实 TCP 交换失败。
func TestCachedTunnelRestoresDataPlane(t *testing.T) {
	s := cachedTunnelFixture(t)
	var disk []byte
	e := NewEngine(Dependencies{NodeID: "node", Persist: func(state State) error { var err error; disk, err = json.Marshal(state); return err }})
	defer e.Close()
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	e.Close()
	var state State
	if err := json.Unmarshal(disk, &state); err != nil {
		t.Fatal(err)
	}
	restored := NewEngine(Dependencies{NodeID: "node"})
	defer restored.Close()
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(s.Rules[0].ListenPort))), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("cached-tunnel")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 13)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "cached-tunnel" {
		t.Fatalf("缓存隧道未恢复: %q %v", got, err)
	}
}

// 出口监听恢复失败时不能复用旧进程的完整成功确认，即使没有入口规则。
func TestCachedTunnelBindFailureClearsAppliedRevision(t *testing.T) {
	s := cachedTunnelFixture(t)
	s.Rules, s.ExitGroups = nil, nil
	s.Digest = SnapshotDigest(s)
	var disk []byte
	e := NewEngine(Dependencies{Persist: func(state State) error { var err error; disk, err = json.Marshal(state); return err }})
	defer e.Close()
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	e.Close()
	occupied, err := net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(s.TunnelServer.ListenPort))))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var state State
	if err := json.Unmarshal(disk, &state); err != nil {
		t.Fatal(err)
	}
	restored := NewEngine(Dependencies{})
	defer restored.Close()
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	if revision, _ := restored.Applied(); revision != 0 || restored.ApplyError() == "" {
		t.Fatalf("出口 bind 失败仍确认旧版本: %d %q", revision, restored.ApplyError())
	}
}

// 缓存也必须先做端口预检，不能靠部分 bind 失败掩盖无效快照。
func TestCachedDuplicatePortsRejectedBeforeListening(t *testing.T) {
	rules := []*controlpb.ForwardRule{}
	results := []*controlpb.RuleApplyResult{}
	for _, id := range []string{"first", "second"} {
		rules = append(rules, &controlpb.ForwardRule{Id: id, EntryGroupId: "g", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: 12345, TargetAddress: "8.8.8.8", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: 443})
		results = append(results, &controlpb.RuleApplyResult{RuleId: id, AppliedRevision: 1})
	}
	calls := 0
	e := NewEngine(Dependencies{Listen: func(string, string) (net.Listener, error) { calls++; return nil, errors.New("不应监听") }})
	defer e.Close()
	err := e.Restore(State{HighestRevision: 1, HighestDigest: strings.Repeat("a", 64), AppliedRevision: 1, AppliedDigest: strings.Repeat("a", 64), Rules: rules, Results: results})
	if err == nil || calls != 0 {
		t.Fatalf("缓存绕过端口预检: err=%v listen=%d", err, calls)
	}
}

// 最终持久化失败只能恢复检查点：变化的出口和已撤销的授权都不能复活。
func TestTunnelCheckpointDoesNotRestoreSupersededConfiguration(t *testing.T) {
	for _, change := range []string{"endpoint", "grant"} {
		t.Run(change, func(t *testing.T) {
			s := cachedTunnelFixture(t)
			if change == "endpoint" {
				s.TunnelServer = nil
			} else {
				s.Rules, s.ExitGroups = nil, nil
			}
			s.Digest = SnapshotDigest(s)
			var disk []byte
			writes, failFinal := 0, false
			e := NewEngine(Dependencies{Persist: func(state State) error {
				writes++
				if failFinal && writes == 2 {
					return errors.New("最终写盘失败")
				}
				var err error
				disk, err = json.Marshal(state)
				return err
			}})
			defer e.Close()
			if err := e.Apply(s); err != nil {
				t.Fatal(err)
			}
			next := proto.Clone(s).(*controlpb.ConfigSnapshot)
			next.Revision = 2
			var oldPort uint32
			if change == "endpoint" {
				oldPort = s.Rules[0].ListenPort
				next.ExitGroups[0].Exits[0].Credential = strings.Repeat("b", 43)
			} else {
				oldPort = s.TunnelServer.ListenPort
				next.TunnelServer.Grants = nil
			}
			next.Digest = SnapshotDigest(next)
			writes, failFinal = 0, true
			if err := e.Apply(next); err == nil {
				t.Fatal("未注入最终写盘失败")
			}
			e.Close()
			var state State
			if err := json.Unmarshal(disk, &state); err != nil {
				t.Fatal(err)
			}
			restored := NewEngine(Dependencies{})
			defer restored.Close()
			if err := restored.Restore(state); err != nil {
				t.Fatal(err)
			}
			l, err := net.Listen("tcp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(int(oldPort))))
			if err != nil {
				t.Fatalf("已变化规则或旧授权监听被恢复: %v", err)
			}
			l.Close()
			if err := restored.Apply(s); err == nil {
				t.Fatal("接受了撤销前的旧快照")
			}
		})
	}
}

func TestCachedTunnelPortConflictRejectedBeforeListening(t *testing.T) {
	s := cachedTunnelFixture(t)
	s.Rules[0].ListenPort = s.TunnelServer.ListenPort
	calls := 0
	e := NewEngine(Dependencies{Listen: func(string, string) (net.Listener, error) { calls++; return nil, errors.New("不应监听") }})
	defer e.Close()
	err := e.Restore(State{HighestRevision: 1, HighestDigest: strings.Repeat("a", 64), Rules: s.Rules, Results: []*controlpb.RuleApplyResult{{RuleId: "tunnel", AppliedRevision: 1}}, ExitGroups: s.ExitGroups, TunnelServer: s.TunnelServer})
	if err == nil || calls != 0 {
		t.Fatalf("缓存绕过实际隧道端口约束: err=%v listen=%d", err, calls)
	}
}
