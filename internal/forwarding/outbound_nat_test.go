package forwarding

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

// The public address is not OS-local. Only the HTTPS echo boundary and physical
// interface enumeration are simulated; the target accepts a real bound TCP dial.
func TestOutboundNATPublicInputBindsVerifiedLocalAddress(t *testing.T) {
	const public = "89.43.141.167"
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	local := outboundTestLocalIPv4(t)
	deps := Dependencies{
		LocalIPv4: func() ([]netip.Addr, error) { return []netip.Addr{local}, nil },
		PublicIPv4From: func(ctx context.Context, source string) (string, error) {
			if source != local.String() {
				return "", errors.New("wrong local source")
			}
			return public, nil
		},
		DialTarget: func(context.Context, string) (net.Conn, error) {
			t.Error("public NAT source silently fell back to system routing")
			return nil, errors.New("unbound dial forbidden")
		},
	}
	port := reserveTestPort(t, &deps)
	e := NewEngine(deps)
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 1, OutboundIpv4: public, Rules: []*controlpb.ForwardRule{{Id: "nat", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "127.0.0.1", TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), AllowPrivate: true, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatalf("verified public NAT address rejected: %v", err)
	}
	c, err := e.dialTarget(context.Background(), s.Rules[0])
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	accepted, err := target.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if accepted.RemoteAddr().(*net.TCPAddr).IP.String() != local.String() {
		t.Fatal("target saw wrong bound source", accepted.RemoteAddr())
	}
	accepted.Close()
	state := e.State()
	if state.OutboundIPv4 != public {
		t.Fatal("cache lost configured public IP", state.OutboundIPv4)
	}
	e.Close()
	// A restart must verify the mapping again, never trust the previous result.
	deps.PublicIPv4From = func(context.Context, string) (string, error) { return "163.53.52.167", nil }
	restored := NewEngine(deps)
	defer restored.Close()
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	if len(restored.State().Rules) != 0 || !strings.Contains(restored.ApplyError(), "出站 IP") {
		t.Fatal("restart trusted stale NAT mapping", restored.State())
	}
}

func TestOutboundNATSelectsMatchingInterfaceAndWithdrawsChangedMapping(t *testing.T) {
	const public = "89.43.141.167"
	mapped := "10.20.151.167"
	deps := Dependencies{
		DialTarget: func(context.Context, string) (net.Conn, error) {
			t.Error("failed NAT mapping fell back to unbound target dial")
			return nil, errors.New("unbound target forbidden")
		},
		LocalIPv4: func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.20.151.167")}, nil
		},
		PublicIPv4From: func(ctx context.Context, source string) (string, error) {
			if source == mapped {
				return public, nil
			}
			return "163.53.52.167", nil
		},
		DialTargetFrom: func(_ context.Context, _, source string) (net.Conn, error) {
			if source != "10.20.151.167" {
				t.Errorf("wrong binding: %s", source)
			}
			return nil, errors.New("fixture target unavailable")
		},
	}
	port := reserveTestPort(t, &deps)
	e := NewEngine(deps)
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 1, OutboundIpv4: public, Rules: []*controlpb.ForwardRule{{Id: "nat", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "8.8.8.8", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	e.dialTarget(context.Background(), s.Rules[0])
	if !e.OutboundIPv4Applied(public) {
		t.Fatal("matching NAT mapping not applied")
	}
	// Keep the configured public IP and revision unchanged while its matching
	// local interface changes. The engine must replace the old binding.
	mapped = "10.0.0.2"
	if e.OutboundIPv4Applied(public) {
		t.Fatal("changed local binding treated as already applied")
	}
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	mapped = ""
	if e.OutboundIPv4Applied(public) {
		t.Fatal("same snapshot trusted changed NAT mapping")
	}
	if err := e.Apply(s); err == nil {
		t.Fatal("changed NAT mapping was accepted")
	}
	if revision, _ := e.Applied(); revision != 0 || len(e.State().Rules) != 0 {
		t.Fatal("failed mapping retained confirmation/listener")
	}
	if c, err := e.dialTarget(context.Background(), s.Rules[0]); err == nil {
		c.Close()
		t.Fatal("failed mapping allowed target dial")
	}
}

func outboundTestLocalIPv4(t *testing.T) netip.Addr {
	t.Helper()
	ips, err := localIPv4Addresses()
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range ips {
		if ip.IsGlobalUnicast() {
			return ip
		}
	}
	t.Skip("requires an assigned non-loopback IPv4")
	return netip.Addr{}
}

func TestOutboundNATUnreachableOtherInterfaceCannotDiscardVerifiedMatch(t *testing.T) {
	e := NewEngine(Dependencies{PublicIPv4From: func(ctx context.Context, source string) (string, error) {
		if source == "10.20.151.167" {
			return "89.43.141.167", nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}})
	defer e.Close()
	local, err := e.resolveOutboundNAT(context.Background(), "89.43.141.167", []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("10.20.151.167")})
	if err != nil || local != "10.20.151.167" {
		t.Fatalf("other interface discarded verified match: %s %v", local, err)
	}
}

func TestOutboundNATCancellationAndLookupFailureDoNotApply(t *testing.T) {
	for _, tc := range []string{"cancelled", "lookup failure", "different public IP"} {
		t.Run(tc, func(t *testing.T) {
			deps := Dependencies{
				LocalIPv4: func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("10.20.151.167")}, nil },
				PublicIPv4From: func(ctx context.Context, _ string) (string, error) {
					if tc == "cancelled" {
						<-ctx.Done()
						return "", ctx.Err()
					}
					if tc == "lookup failure" {
						return "", errors.New("observer unavailable")
					}
					return "163.53.52.167", nil
				},
			}
			port := reserveTestPort(t, &deps)
			e := NewEngine(deps)
			defer e.Close()
			s := &controlpb.ConfigSnapshot{Revision: 1, OutboundIpv4: "89.43.141.167", Rules: []*controlpb.ForwardRule{{Id: "nat", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "8.8.8.8", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4}}}
			s.Digest = SnapshotDigest(s)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if err := e.ApplyContext(ctx, s); err == nil {
				t.Fatal("unverified mapping applied")
			}
			if len(e.State().Rules) != 0 {
				t.Fatal("unverified mapping enabled listener")
			}
		})
	}
}
