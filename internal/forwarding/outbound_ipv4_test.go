package forwarding

import (
	"context"
	"errors"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestOutboundIPv4RejectsNonLocalAndRevalidatesRestore(t *testing.T) {
	deps := Dependencies{LocalIPv4: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil }}
	port := reserveTestPort(t, &deps)
	e := NewEngine(deps)
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 1, OutboundIpv4: "192.0.2.253", Rules: []*controlpb.ForwardRule{{Id: "direct", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "8.8.8.8", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err == nil || !strings.Contains(err.Error(), "出站 IP") {
		t.Fatalf("非本机源未拒绝: %v", err)
	}
	if len(e.State().Rules) != 0 {
		t.Fatal("非本机源启动了业务监听")
	}
	for _, source := range []string{"::1", "0.0.0.0", "224.0.0.1", "host.test"} {
		s.OutboundIpv4 = source
		s.Digest = SnapshotDigest(s)
		if err := ValidateSnapshot(s); err == nil {
			t.Fatal("接受非法源", source)
		}
	}
}

func TestOutboundIPv4RechecksApplyRestoreAndCorrectDialSeam(t *testing.T) {
	local := true
	enumerationErr := false
	deps := Dependencies{LocalIPv4: func() ([]netip.Addr, error) {
		if enumerationErr {
			return nil, errors.New("interfaces unavailable")
		}
		if local {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, nil
	}}
	port := reserveTestPort(t, &deps)
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	observed := make(chan string, 8)
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			observed <- c.RemoteAddr().(*net.TCPAddr).IP.String()
			c.Close()
		}
	}()
	deps.DialTarget = func(context.Context, string) (net.Conn, error) {
		t.Error("source used unbound seam")
		return nil, errors.New("wrong seam")
	}
	deps.DialTargetFrom = func(ctx context.Context, address, source string) (net.Conn, error) {
		if source != "127.0.0.1" {
			t.Error("source lost", source)
		}
		return (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(source)}}).DialContext(ctx, "tcp4", address)
	}
	e := NewEngine(deps)
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 1, OutboundIpv4: "127.0.0.1", Rules: []*controlpb.ForwardRule{{Id: "d", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "127.0.0.1", TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), AllowPrivate: true, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	c, err := e.dialTarget(context.Background(), s.Rules[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case ip := <-observed:
		if ip != "127.0.0.1" {
			t.Fatal(ip)
		}
	case <-time.After(time.Second):
		t.Fatal("no target")
	}
	state := e.State()
	if state.OutboundIPv4 != "127.0.0.1" {
		t.Fatal(state)
	}
	changed := proto.Clone(s).(*controlpb.ConfigSnapshot)
	changed.OutboundIpv4 = ""
	if SnapshotDigest(changed) == s.Digest {
		t.Fatal("source omitted from digest")
	}
	if e.OutboundIPv4Applied("") {
		t.Fatal("same digest skipped missing source")
	}
	local = false
	if err := e.Apply(s); err == nil {
		t.Fatal("same source not rechecked")
	}
	if len(e.State().Rules) != 0 {
		t.Fatal("retained invalid direct listener")
	}
	e.Close()
	restore := NewEngine(deps)
	defer restore.Close()
	if err := restore.Restore(state); err != nil {
		t.Fatal(err)
	}
	if len(restore.State().Rules) != 0 || !strings.Contains(restore.ApplyError(), "不是本机") {
		t.Fatal("restore trusted old interface", restore.State())
	}
	local = true
	enumerationErr = true
	if err := e.Apply(s); err == nil || !strings.Contains(err.Error(), "interfaces unavailable") {
		t.Fatal("enumeration error ignored", err)
	}
}

func TestOutboundIPv4SourceChangeFinalWriteFailureCannotRestoreUnbound(t *testing.T) {
	for _, source := range []string{"127.0.0.1", ""} {
		t.Run("next="+source, func(t *testing.T) {
			var disk State
			failFinal := false
			writes := 0
			deps := Dependencies{Persist: func(s State) error {
				writes++
				if failFinal && writes%2 == 0 {
					return errors.New("disk full")
				}
				disk = s
				return nil
			}}
			port := reserveTestPort(t, &deps)
			e := NewEngine(deps)
			defer e.Close()
			initial := ""
			if source == "" {
				initial = "127.0.0.1"
			}
			s := &controlpb.ConfigSnapshot{Revision: 1, OutboundIpv4: initial, Rules: []*controlpb.ForwardRule{{Id: "d", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "8.8.8.8", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}}}
			s.Digest = SnapshotDigest(s)
			if err := e.Apply(s); err != nil {
				t.Fatal(err)
			}
			s.Revision = 2
			s.OutboundIpv4 = source
			s.Digest = SnapshotDigest(s)
			failFinal = true
			if err := e.Apply(s); err == nil {
				t.Fatal("write failure confirmed")
			}
			if rev, _ := e.Applied(); rev == 2 {
				t.Fatal("acknowledged failed persistence")
			}
			e.Close()
			if disk.HighestRevision != 2 || disk.OutboundIPv4 != source || len(disk.Rules) != 0 {
				t.Fatal("unsafe source checkpoint", disk)
			}
			restored := NewEngine(Dependencies{})
			defer restored.Close()
			if err := restored.Restore(disk); err != nil {
				t.Fatal(err)
			}
			if len(restored.State().Rules) != 0 {
				t.Fatal("restored old unbound rule")
			}
		})
	}
}

func TestOutboundIPv4RestoreRechecksAddressThatAppearedAfterFailure(t *testing.T) {
	deps := Dependencies{LocalIPv4: func() ([]netip.Addr, error) { return nil, nil }}
	s := cachedTunnelFixture(t, &deps)
	s.Rules, s.ExitGroups = nil, nil
	s.OutboundIpv4 = "127.0.0.1"
	s.Digest = SnapshotDigest(s)
	e := NewEngine(deps)
	if err := e.Apply(s); err == nil {
		e.Close()
		t.Fatal("missing source accepted")
	}
	state := e.State()
	e.Close()
	restored := NewEngine(Dependencies{})
	defer restored.Close()
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	if restored.ApplyError() != "" {
		t.Fatal("restored local source retained historical source error", restored.ApplyError())
	}
	if !restored.OutboundIPv4Applied("127.0.0.1") {
		t.Fatal("newly appeared source not revalidated")
	}
}

func TestOutboundIPv4SameSnapshotFailureWithdrawsFullConfirmation(t *testing.T) {
	local := true
	deps := Dependencies{LocalIPv4: func() ([]netip.Addr, error) {
		if local {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return nil, nil
	}}
	port := reserveTestPort(t, &deps)
	e := NewEngine(deps)
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 1, OutboundIpv4: "127.0.0.1", Rules: []*controlpb.ForwardRule{{Id: "d", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "8.8.8.8", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	local = false
	if err := e.Apply(s); err == nil {
		t.Fatal("lost interface accepted")
	}
	if revision, _ := e.Applied(); revision != 0 {
		t.Fatal("failed same snapshot retained complete confirmation", revision)
	}
}

func TestOutboundIPv4DefaultDialerCannotIgnoreSourceBinding(t *testing.T) {
	// Inject only physical interface enumeration. The real production dialer must
	// try to bind the OS-unavailable source and must not connect the real target.
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	observed := make(chan struct{}, 1)
	go func() {
		c, err := target.Accept()
		if err == nil {
			c.Close()
			observed <- struct{}{}
		}
	}()
	source := "192.0.2.253"
	actual, err := localIPv4Addresses()
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range actual {
		if ip.String() == source {
			t.Fatal("fixture source is OS-local")
		}
	}
	e := NewEngine(Dependencies{LocalIPv4: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr(source)}, nil }})
	defer e.Close()
	rule := &controlpb.ForwardRule{TargetAddress: "127.0.0.1", TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), AllowPrivate: true}
	binding := e.validateOutbound(source)
	if binding.err != nil {
		t.Fatal(binding.err)
	}
	e.outbound.Store(binding)
	c, err := e.dialTarget(context.Background(), rule)
	if c != nil {
		c.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "bind:") {
		t.Fatal("default dialer lost LocalAddr binding", err)
	}
	select {
	case <-observed:
		t.Fatal("unbound fallback reached target")
	default:
	}
	t.Logf("production DialTargetFrom returned %v; target connections=0", err)
}
