package forwarding

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
	"strconv"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

func TestDomainCandidatesUseDistinctIPv4AndEnforceBudget(t *testing.T) {
	attempts := []string{}
	e := NewEngine(Dependencies{LookupIPv4: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("203.0.113.2"), netip.MustParseAddr("203.0.113.3"), netip.MustParseAddr("203.0.113.4")}, nil
	}, DialTarget: func(ctx context.Context, address string) (net.Conn, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("拨号没有期限")
		}
		attempts = append(attempts, address)
		return nil, errors.New("目标不可达")
	}})
	_, err := e.dialTarget(context.Background(), &controlpb.ForwardRule{TargetAddress: "example.test", TargetPort: 443})
	if err == nil || !reflect.DeepEqual(attempts, []string{"203.0.113.1:443", "203.0.113.2:443", "203.0.113.3:443"}) {
		t.Fatalf("候选/预算: %v, %v", attempts, err)
	}
}

func TestDomainFallbackReachesRealIPv4AndChecksResolvedPrivateIP(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("target"))
	}()
	port := uint32(l.Addr().(*net.TCPAddr).Port)
	e := NewEngine(Dependencies{LookupIPv4: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.2"), netip.MustParseAddr("127.0.0.1")}, nil
	}})
	rule := &controlpb.ForwardRule{TargetAddress: "private.test", TargetPort: port}
	if c, err := e.dialTarget(context.Background(), rule); err == nil {
		c.Close()
		t.Fatal("域名绕过私网许可")
	}
	rule.AllowPrivate = true
	c, err := e.dialTarget(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(time.Second))
	b := make([]byte, 6)
	if _, err := io.ReadFull(c, b); err != nil || string(b) != "target" {
		t.Fatalf("第二个 A 未连通: %q %v", b, err)
	}
}
func TestPrivateAddressPolicy(t *testing.T) {
	for _, host := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "127.0.0.1", "169.254.169.254", "100.64.0.1"} {
		ip := netip.MustParseAddr(host)
		if AllowedIP(ip, false) || !AllowedIP(ip, true) {
			t.Errorf("私网许可: %s", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "224.0.0.1", "255.255.255.255", "::1"} {
		if AllowedIP(netip.MustParseAddr(host), true) {
			t.Errorf("非法目标: %s", host)
		}
	}
	if !AllowedIP(netip.MustParseAddr("8.8.8.8"), false) {
		t.Fatal("公网被拒")
	}
}
func TestSnapshotRejectsTamperingAndVersionRollback(t *testing.T) {
	e := NewEngine(Dependencies{})
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 2}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	s.Revision = 3
	if err := e.Apply(s); err == nil {
		t.Fatal("接受被篡改摘要")
	}
	s.Revision = 1
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err == nil {
		t.Fatal("接受版本倒退")
	}
	s.Revision = 2
	s.Rules = []*controlpb.ForwardRule{{Id: "bad"}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err == nil {
		t.Fatal("接受同版本内容替换")
	}
	rev, _ := e.Applied()
	if rev != 2 {
		t.Fatal("旧快照覆盖实际版本")
	}
}
func TestForwardingPreservesTCPHalfClose(t *testing.T) {
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		c, err := target.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		c.Write(append([]byte("reply:"), b...))
	}()
	reserve, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	e := NewEngine(Dependencies{})
	defer e.Close()
	s := &controlpb.ConfigSnapshot{Revision: 1, Rules: []*controlpb.ForwardRule{{Id: "rule", EntryGroupId: "g", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: uint32(port), TargetAddress: "127.0.0.1", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), AllowPrivate: true}}}
	s.Digest = SnapshotDigest(s)
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTCP("tcp4", nil, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("request"))
	c.CloseWrite()
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "reply:request" {
		t.Fatalf("半关闭: %q %v port=%s", b, err, strconv.Itoa(port))
	}
}
