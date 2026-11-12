package forwarding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

func TestIXAddressDomainRestoresCacheAndResolvesEachSessionAndProbe(t *testing.T) {
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	ep := s.ExitGroups[0].Exits[0]
	ep.Address = "ix.example.test"
	ep.EndpointConfigDigest = "configuration-one"
	s.Digest = SnapshotDigest(s)
	var lookups atomic.Int32
	deps.LookupIPv4 = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host != "ix.example.test" {
			return nil, errors.New("unexpected DNS name")
		}
		lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("224.0.0.1"), netip.MustParseAddr("255.255.255.255"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	deps.DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		if address != net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ep.Port))) {
			return nil, errors.New("tunnel dial was not a validated concrete IPv4")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}
	var disk []byte
	deps.Persist = func(state State) error { var err error; disk, err = json.Marshal(state); return err }
	e := NewEngine(deps)
	defer e.Close()
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(s.Rules[0].ListenPort))), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(2 * time.Second))
	defer c.Close()
	if _, err = c.Write([]byte("IX")); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 2)
	if _, err = io.ReadFull(c, out); err != nil || string(out) != "IX" {
		t.Fatal("域名业务转发失败", err)
	}
	client := e.tunnelClients[ep.NodeId]
	client.probeOnce(context.Background())
	health := e.ExitHealth()
	if lookups.Load() != 2 || len(health) != 1 || health[0].LastProbe != "success" || health[0].EndpointConfigDigest != "configuration-one" {
		t.Fatal("探测未独立解析或未回显配置摘要", lookups.Load(), health)
	}
	e.Close()
	var state State
	if err = json.Unmarshal(disk, &state); err != nil {
		t.Fatal(err)
	}
	if state.ExitGroups[0].Exits[0].EndpointConfigDigest != "configuration-one" {
		t.Fatal("缓存丢失摘要")
	}
	restored := NewEngine(Dependencies{LookupIPv4: deps.LookupIPv4, DialTunnel: deps.DialTunnel})
	defer restored.Close()
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	restored.tunnelClients[ep.NodeId].probeOnce(context.Background())
	if lookups.Load() != 3 || restored.ExitHealth()[0].LastProbe != "success" {
		t.Fatal("恢复后探测未重新解析", restored.ExitHealth())
	}
}

func TestIXAddressDNSFailureAndCancellationNeverDialInvalidCandidates(t *testing.T) {
	for _, mode := range []string{"all_invalid", "lookup_error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			cert, _, _, _ := tunnelTestIdentity(t, "exit.test", false)
			var dials atomic.Int32
			e := NewEngine(Dependencies{LookupIPv4: func(ctx context.Context, _ string) ([]netip.Addr, error) {
				switch mode {
				case "lookup_error":
					return nil, errors.New("DNS failed")
				case "cancel":
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("224.0.0.1"), netip.MustParseAddr("255.255.255.255")}, nil
			}, DialTunnel: func(context.Context, string) (net.Conn, error) { dials.Add(1); return nil, errors.New("must not dial") }})
			client := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{Address: "ix.example.test", Port: 443, ServerName: "exit.test", CertificatePem: cert}}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			session, err := client.openSession(ctx, true)
			if session != nil {
				session.Close()
			}
			if err == nil || dials.Load() != 0 || time.Since(start) > time.Second {
				t.Fatal("解析失败未及时停止或拨非法候选", err, dials.Load())
			}
		})
	}
}
