package forwarding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"nbpass/internal/controlpb"
)

func reserveTestPort(t *testing.T, deps *Dependencies) uint32 {
	t.Helper()
	l, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	t.Cleanup(func() { l.Close() })
	previous := deps.Listen
	var handed atomic.Bool
	deps.Listen = func(network, address string) (net.Listener, error) {
		if network == "tcp4" && address == net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port))) && handed.CompareAndSwap(false, true) {
			return l, nil
		}
		if previous != nil {
			return previous(network, address)
		}
		return net.Listen(network, address)
	}
	return port
}

func cachedTunnelFixture(t *testing.T, deps *Dependencies) *controlpb.ConfigSnapshot {
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
	rule := &controlpb.ForwardRule{Id: "tunnel", EntryGroupId: "entry", Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, ExitGroupId: "exit", ListenPort: reserveTestPort(t, deps), TargetAddress: "127.0.0.1", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), AllowPrivate: true}
	ep := &controlpb.TunnelEndpoint{NodeId: "node", EntryId: "node", Address: "127.0.0.1", Port: reserveTestPort(t, deps), Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS, ServerName: "exit.test", CertificatePem: cert, Credential: strings.Repeat("a", 43)}
	s := &controlpb.ConfigSnapshot{Revision: 1, Rules: []*controlpb.ForwardRule{rule}, ExitGroups: []*controlpb.ExitGroup{{Id: "exit", Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP, Exits: []*controlpb.TunnelEndpoint{ep}}}, TunnelServer: &controlpb.TunnelServer{ListenPort: ep.Port, Transport: ep.Transport, ServerName: ep.ServerName, CertificatePem: cert, PrivateKeyPem: key, Peers: []*controlpb.TunnelPeer{{EntryId: "node", Credential: ep.Credential}}, Grants: []*controlpb.TunnelGrant{{EntryId: "node", Rule: rule}}}}
	s.Digest = SnapshotDigest(s)
	return s
}

// 缺少出口组、节点对凭据、出口私钥或授权中的任何一项都会使真实 TCP 交换失败。
func TestCachedTunnelRestoresDataPlane(t *testing.T) {
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	var disk []byte
	e := NewEngine(Dependencies{Listen: deps.Listen, NodeID: "node", Persist: func(state State) error { var err error; disk, err = json.Marshal(state); return err }})
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
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	s.Rules, s.ExitGroups = nil, nil
	s.Digest = SnapshotDigest(s)
	var disk []byte
	e := NewEngine(Dependencies{Listen: deps.Listen, Persist: func(state State) error { var err error; disk, err = json.Marshal(state); return err }})
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
			deps := Dependencies{}
			s := cachedTunnelFixture(t, &deps)
			if change == "endpoint" {
				// 入口现在必须得到真实出口授权；缓存检查点场景仍只重建入口。
				exit := NewEngine(deps)
				defer exit.Close()
				exitConfig := &controlpb.ConfigSnapshot{Revision: 1, TunnelServer: s.TunnelServer}
				exitConfig.Digest = SnapshotDigest(exitConfig)
				if err := exit.Apply(exitConfig); err != nil {
					t.Fatal(err)
				}
				s.TunnelServer = nil
			} else {
				s.Rules, s.ExitGroups = nil, nil
			}
			s.Digest = SnapshotDigest(s)
			var disk []byte
			writes, failFinal := 0, false
			e := NewEngine(Dependencies{Listen: deps.Listen, Persist: func(state State) error {
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
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	s.Rules[0].ListenPort = s.TunnelServer.ListenPort
	calls := 0
	e := NewEngine(Dependencies{Listen: func(string, string) (net.Listener, error) { calls++; return nil, errors.New("不应监听") }})
	defer e.Close()
	err := e.Restore(State{HighestRevision: 1, HighestDigest: strings.Repeat("a", 64), Rules: s.Rules, Results: []*controlpb.RuleApplyResult{{RuleId: "tunnel", AppliedRevision: 1}}, ExitGroups: s.ExitGroups, TunnelServer: s.TunnelServer})
	if err == nil || calls != 0 {
		t.Fatalf("缓存绕过实际隧道端口约束: err=%v listen=%d", err, calls)
	}
}

func TestTunnelApplyWaitsForMatchingExitGrant(t *testing.T) {
	for _, mode := range []string{"missing_grant", "old_target", "old_generation", "wrong_entry"} {
		t.Run(mode, func(t *testing.T) {
			deps := Dependencies{}
			s := cachedTunnelFixture(t, &deps)
			exitConfig := proto.Clone(s).(*controlpb.ConfigSnapshot)
			exitConfig.Rules, exitConfig.ExitGroups = nil, nil
			if mode == "missing_grant" {
				exitConfig.TunnelServer.Grants = nil
			} else if mode == "old_target" {
				exitConfig.TunnelServer.Grants[0].Rule.TargetAddress = "127.0.0.2"
			} else if mode == "old_generation" {
				exitConfig.TunnelServer.Grants[0].Rule.AuthorizationGeneration = 1
			} else {
				exitConfig.TunnelServer.Grants[0].EntryId = "other-entry"
				exitConfig.TunnelServer.Peers = append(exitConfig.TunnelServer.Peers, &controlpb.TunnelPeer{EntryId: "other-entry", Credential: strings.Repeat("b", 43)})
			}
			exitConfig.Digest = SnapshotDigest(exitConfig)
			var targetDials atomic.Int32
			exit := NewEngine(Dependencies{Listen: deps.Listen, DialTarget: func(context.Context, string) (net.Conn, error) {
				targetDials.Add(1)
				return nil, errors.New("授权检查不应拨号目标")
			}})
			defer exit.Close()
			if err := exit.Apply(exitConfig); err != nil {
				t.Fatal(err)
			}
			var listens atomic.Int32
			entry := NewEngine(Dependencies{Listen: func(network, address string) (net.Listener, error) {
				listens.Add(1)
				return deps.Listen(network, address)
			}})
			defer entry.Close()
			entryConfig := proto.Clone(s).(*controlpb.ConfigSnapshot)
			entryConfig.TunnelServer = nil
			entryConfig.Digest = SnapshotDigest(entryConfig)
			if err := entry.Apply(entryConfig); err == nil || listens.Load() != 0 {
				t.Fatalf("出口没有匹配授权就启动了监听: err=%v listens=%d", err, listens.Load())
			}
			if revision, _ := entry.Applied(); revision != 0 || entry.ApplyError() == "" {
				t.Fatalf("出口未就绪却确认配置: revision=%d error=%q", revision, entry.ApplyError())
			}
			exitConfig.Revision++
			exitConfig.TunnelServer = s.TunnelServer
			exitConfig.Digest = SnapshotDigest(exitConfig)
			if err := exit.Apply(exitConfig); err != nil {
				t.Fatal(err)
			}
			if err := entry.Apply(entryConfig); err != nil || listens.Load() != 1 {
				t.Fatalf("出口授权后未能应用同版快照: err=%v listens=%d", err, listens.Load())
			}
			if targetDials.Load() != 0 {
				t.Fatal("授权就绪检查创建了目标连接")
			}
		})
	}
}

func TestTunnelApplySharesReadinessBudgetAcrossRules(t *testing.T) {
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	s.TunnelServer = nil
	other := proto.Clone(s.Rules[0]).(*controlpb.ForwardRule)
	other.Id, other.ListenPort = "other", reserveTestPort(t, &deps)
	s.Rules = append(s.Rules, other)
	s.Digest = SnapshotDigest(s)
	var attempts atomic.Int32
	e := NewEngine(Dependencies{Listen: deps.Listen, DialTunnel: func(ctx context.Context, _ string) (net.Conn, error) {
		attempts.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	defer e.Close()
	if err := e.Apply(s); err == nil {
		t.Fatal("黑洞出口错误确认配置")
	}
	if attempts.Load() != 1 {
		t.Fatalf("同一配置的规则分别消耗了完整探测预算: %d", attempts.Load())
	}
}

func TestTunnelApplyCancellationStopsReadiness(t *testing.T) {
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	s.TunnelServer = nil
	s.Digest = SnapshotDigest(s)
	entered := make(chan struct{})
	e := NewEngine(Dependencies{Listen: deps.Listen, DialTunnel: func(ctx context.Context, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.ApplyContext(ctx, s) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("未开始探测出口")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("取消后错误确认配置")
		}
	case <-time.After(time.Second):
		<-done
		t.Fatal("取消后仍等待授权探测超时")
	}
}

func TestTunnelApplyUnavailableExitDoesNotStarveHealthyExit(t *testing.T) {
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	exit := NewEngine(deps)
	defer exit.Close()
	exitConfig := &controlpb.ConfigSnapshot{Revision: 1, TunnelServer: s.TunnelServer}
	exitConfig.Digest = SnapshotDigest(exitConfig)
	if err := exit.Apply(exitConfig); err != nil {
		t.Fatal(err)
	}
	s.TunnelServer = nil
	blockedRule := proto.Clone(s.Rules[0]).(*controlpb.ForwardRule)
	blockedRule.Id, blockedRule.ExitGroupId = "blocked", "blocked-exit"
	blockedRule.ListenPort = reserveTestPort(t, &deps)
	blockedEndpoint := proto.Clone(s.ExitGroups[0].Exits[0]).(*controlpb.TunnelEndpoint)
	blockedEndpoint.NodeId, blockedEndpoint.Address = "blocked-node", "127.0.0.2"
	s.ExitGroups = append(s.ExitGroups, &controlpb.ExitGroup{Id: blockedRule.ExitGroupId, Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP, Exits: []*controlpb.TunnelEndpoint{blockedEndpoint}})
	s.Rules = append([]*controlpb.ForwardRule{blockedRule}, s.Rules...)
	s.Digest = SnapshotDigest(s)
	deps.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		host, _, _ := net.SplitHostPort(address)
		if host == "127.0.0.2" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}
	entry := NewEngine(deps)
	defer entry.Close()
	if err := entry.Apply(s); err == nil {
		t.Fatal("黑洞出口未报告部分失败")
	}
	results := entry.State().Results
	if len(results) != 2 || results[0].ApplyError == "" || results[1].ApplyError != "" || results[1].AppliedRevision != s.Revision {
		t.Fatalf("不可达出口阻止了正常出口的规则应用: %v", results)
	}
}

// 监听端口等节点配置元数据不属于逐流授权；同代次与目标
// 已获授权时，预检不能要求两端整条规则 protobuf 完全相等。
func TestTunnelReadinessUsesGenerationAndTargetRatherThanListenerMetadata(t *testing.T) {
	deps := Dependencies{}
	snapshot := cachedTunnelFixture(t, &deps)
	snapshot.Rules[0].AuthorizationGeneration = 7
	exitConfig := proto.Clone(snapshot).(*controlpb.ConfigSnapshot)
	exitConfig.Rules, exitConfig.ExitGroups = nil, nil
	exitConfig.TunnelServer.Grants[0].Rule.ListenPort = reserveTestPort(t, &deps)
	exitConfig.Digest = SnapshotDigest(exitConfig)
	var targetDials atomic.Int32
	exit := NewEngine(Dependencies{Listen: deps.Listen, DialTarget: func(context.Context, string) (net.Conn, error) {
		targetDials.Add(1)
		return nil, errors.New("预检不得拨号目标")
	}})
	defer exit.Close()
	if err := exit.Apply(exitConfig); err != nil {
		t.Fatal(err)
	}
	snapshot.TunnelServer = nil
	snapshot.Digest = SnapshotDigest(snapshot)
	entry := NewEngine(deps)
	defer entry.Close()
	if err := entry.Apply(snapshot); err != nil {
		t.Fatalf("相同授权因监听元数据不同被拒绝: %v", err)
	}
	if revision, digest := entry.Applied(); revision != snapshot.Revision || digest != snapshot.Digest {
		t.Fatalf("入口未确认相同授权: %d %q", revision, digest)
	}
	if targetDials.Load() != 0 {
		t.Fatal("授权预检创建了目标连接")
	}
}
