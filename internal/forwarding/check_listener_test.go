package forwarding

import (
	"fmt"
	"nbpass/internal/controlpb"
	"net"
	"strconv"
	"testing"
	"time"
)

// 检测监听随快照生效、改端口撤销旧监听，离线从实际缓存恢复；bind 失败不能确认配置。
func TestCheckListenerApplyRestoreAndFailure(t *testing.T) {
	port := func() uint32 {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
		return uint32(l.Addr().(*net.TCPAddr).Port)
	}
	a, b := port(), port()
	e := NewEngine(Dependencies{})
	defer e.Close()
	apply := func(rev uint64, p uint32) error {
		s := &controlpb.ConfigSnapshot{Revision: rev, CheckPort: p}
		s.Digest = SnapshotDigest(s)
		return e.Apply(s)
	}
	if err := apply(1, a); err != nil {
		t.Fatal(err)
	}
	dial := func(p uint32) error {
		c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(p))), time.Second)
		if c != nil {
			c.Close()
		}
		return err
	}
	if err := dial(a); err != nil {
		t.Fatal("检测端口未监听", err)
	}
	if err := apply(2, b); err != nil {
		t.Fatal(err)
	}
	if dial(a) == nil {
		t.Fatal("旧检测端口未撤销")
	}
	state := e.State()
	e.Close()
	e = NewEngine(Dependencies{})
	defer e.Close()
	if err := e.Restore(state); err != nil {
		t.Fatal(err)
	}
	if err := dial(b); err != nil {
		t.Fatal("离线恢复未监听", err)
	}
	other := NewEngine(Dependencies{})
	defer other.Close()
	s := &controlpb.ConfigSnapshot{Revision: 3, CheckPort: b}
	s.Digest = SnapshotDigest(s)
	if other.Apply(s) == nil {
		t.Fatal("bind 失败却确认配置")
	}
	if rev, _ := other.Applied(); rev == 3 {
		t.Fatal("失败配置推进应用版本")
	}
}

// 旧缓存缺少检测字段时沿用默认监听；新配置撤销检查点中的0则不能恢复旧端口。
func TestCheckListenerLegacyCacheAndRevocationCheckpoint(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := uint32(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	l, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := uint32(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	var checkpoint State
	e := NewEngine(Dependencies{DefaultCheckPort: a, Persist: func(s State) error {
		if s.HighestRevision == 2 && checkpoint.HighestRevision == 0 {
			checkpoint = s
		}
		return nil
	}})
	s := &controlpb.ConfigSnapshot{Revision: 1}
	s.Digest = SnapshotDigest(s)
	if err = e.Apply(s); err != nil {
		t.Fatal(err)
	}
	old := State{HighestRevision: 1, HighestDigest: s.Digest, AppliedRevision: 1, AppliedDigest: s.Digest}
	e.Close()
	e = NewEngine(Dependencies{DefaultCheckPort: a})
	if err = e.Restore(old); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(a))))
	if err != nil {
		t.Fatal("旧版缓存未启用默认检测监听", err)
	}
	conn.Close()
	e.Close()
	e = NewEngine(Dependencies{DefaultCheckPort: a, Persist: func(s State) error {
		if s.HighestRevision == 2 && checkpoint.HighestRevision == 0 {
			checkpoint = s
		}
		return nil
	}})
	if err = e.Apply(s); err != nil {
		t.Fatal(err)
	}
	s = &controlpb.ConfigSnapshot{Revision: 2, CheckPort: b}
	s.Digest = SnapshotDigest(s)
	if err = e.Apply(s); err != nil {
		t.Fatal(err)
	}
	e.Close()
	e = NewEngine(Dependencies{DefaultCheckPort: a})
	defer e.Close()
	if err = e.Restore(checkpoint); err != nil {
		t.Fatal(err)
	}
	conn, err = net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(a))), time.Second)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("撤销检查点恢复了已废弃的默认检测端口")
	}
}

// 旧版自定义检测端口未写入缓存，18080仍可能是合法业务端口；离线升级优先恢复业务。
func TestCheckListenerLegacyCachePreservesBusinessOnDefaultPort(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	defer l.Close()
	rule := &controlpb.ForwardRule{Id: "legacy-rule", EntryGroupId: "g", ListenPort: port, TargetAddress: "127.0.0.1", TargetPort: 1, AllowPrivate: true, Kind: controlpb.RuleKind_RULE_KIND_DIRECT, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}
	snapshot := &controlpb.ConfigSnapshot{Revision: 1, Rules: []*controlpb.ForwardRule{rule}}
	snapshot.Digest = SnapshotDigest(snapshot)
	legacy := State{HighestRevision: 1, HighestDigest: snapshot.Digest, AppliedRevision: 1, AppliedDigest: snapshot.Digest, Rules: snapshot.Rules, Results: []*controlpb.RuleApplyResult{{RuleId: rule.Id, AppliedRevision: 1}}}
	// 保留真实端口所有权直到引擎接管，避免多个并行工作目录抢占临时端口。
	e := NewEngine(Dependencies{DefaultCheckPort: port, Listen: func(network, address string) (net.Listener, error) {
		if address != net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port))) {
			return nil, fmt.Errorf("意外监听地址 %s", address)
		}
		return l, nil
	}})
	defer e.Close()
	if err = e.Restore(legacy); err != nil {
		t.Fatal(err)
	}
	s := e.State()
	if len(s.Rules) != 1 || s.Results[0].ApplyError != "" || s.AppliedRevision != 1 {
		t.Fatalf("默认检测监听抢占旧业务: %+v", s)
	}
	if s.CheckPort != 0 {
		t.Fatal("旧缓存的业务端口被当作默认检测端口")
	}
}
