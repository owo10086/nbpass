package forwarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
)

// Real WSS/yamux/TCP fixture. Only target access is forbidden on non-terminal
// nodes; successful forwarding must reach the actual terminal TCP listener.
type relayFixture struct {
	engines       []*Engine
	snapshots     []*controlpb.ConfigSnapshot
	deps          []Dependencies
	target        net.Listener
	accepted      atomic.Int32
	resolved      atomic.Int32
	sources       chan string
	targetHandler atomic.Pointer[func(net.Conn)]
}

func newRelayFixture(t *testing.T, hops int) *relayFixture {
	t.Helper()
	f := &relayFixture{sources: make(chan string, 100)}
	var err error
	f.target, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, e := range f.engines {
			e.Close()
		}
		f.target.Close()
	})
	go func() {
		for {
			c, err := f.target.Accept()
			if err != nil {
				return
			}
			f.accepted.Add(1)
			f.sources <- c.RemoteAddr().(*net.TCPAddr).IP.String()
			go func() {
				defer c.Close()
				if handler := f.targetHandler.Load(); handler != nil {
					(*handler)(c)
				} else {
					io.Copy(c, c)
				}
			}()
		}
	}()
	f.deps = make([]Dependencies, hops+1)
	f.snapshots = make([]*controlpb.ConfigSnapshot, hops+1)
	endpoints := make([]*controlpb.TunnelEndpoint, hops+1)
	for i := 0; i <= hops; i++ {
		f.deps[i] = Dependencies{NodeID: fmt.Sprint(i), DialTarget: func(context.Context, string) (net.Conn, error) {
			return nil, errors.New("nonterminal target forbidden")
		}, LookupIPv4: func(context.Context, string) ([]netip.Addr, error) {
			return nil, errors.New("nonterminal target resolution forbidden")
		}}
		f.snapshots[i] = &controlpb.ConfigSnapshot{Revision: 1}
		if i == 0 {
			continue
		}
		cert, key, _, _ := tunnelTestIdentity(t, fmt.Sprintf("node%d.test", i), false)
		ep := &controlpb.TunnelEndpoint{NodeId: fmt.Sprint(i), EntryId: fmt.Sprint(i - 1), AddressId: "public", Address: "127.0.0.1", Port: reserveTestPort(t, &f.deps[i]), ServerName: fmt.Sprintf("node%d.test", i), CertificatePem: cert, Credential: strings.Repeat("a", 43), Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS}
		ep.EndpointConfigDigest = EndpointConfigDigest(ep)
		endpoints[i] = ep
		f.snapshots[i].TunnelServer = &controlpb.TunnelServer{ListenPort: ep.Port, ServerName: ep.ServerName, CertificatePem: cert, PrivateKeyPem: key, Transport: ep.Transport, Peers: []*controlpb.TunnelPeer{{EntryId: ep.EntryId, Credential: ep.Credential}}}
	}
	edges := []*controlpb.ChainEdgeAuthorization{}
	for i := 1; i <= hops; i++ {
		ep := endpoints[i]
		edges = append(edges, &controlpb.ChainEdgeAuthorization{FromNodeId: ep.EntryId, ToNodeId: ep.NodeId, AddressId: ep.AddressId, EndpointConfigDigest: ep.EndpointConfigDigest, HopIndex: uint32(i)})
	}
	rule := &controlpb.ForwardRule{Id: "r", Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, ChainId: "chain", AuthorizationGeneration: 7, ListenPort: reserveTestPort(t, &f.deps[0]), ExitGroupId: "g1", AuthorizedExitIds: []string{"1"}, TargetAddress: "target.test", TargetPort: uint32(f.target.Addr().(*net.TCPAddr).Port), TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, AllowPrivate: true, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: strings.Repeat("f", 64), Edges: edges}}
	f.snapshots[0].Rules = []*controlpb.ForwardRule{rule}
	for i := 0; i <= hops; i++ {
		if i < hops {
			f.snapshots[i].ExitGroups = []*controlpb.ExitGroup{{Id: fmt.Sprintf("g%d", i+1), Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP, Exits: []*controlpb.TunnelEndpoint{endpoints[i+1]}}}
		}
		if i > 0 {
			r := proto.Clone(rule).(*controlpb.ForwardRule)
			r.HopIndex = uint32(i)
			r.ChainAuthorization.Edges = ChainEvidenceEdges(edges, fmt.Sprint(i), uint32(i), fmt.Sprint(i-1))
			grant := &controlpb.TunnelGrant{EntryId: fmt.Sprint(i - 1), Rule: r, HopIndex: uint32(i)}
			if i < hops {
				grant.NextExitGroupId = fmt.Sprintf("g%d", i+1)
				grant.AuthorizedNextNodeIds = []string{fmt.Sprint(i + 1)}
			}
			f.snapshots[i].TunnelServer.Grants = []*controlpb.TunnelGrant{grant}
		}
	}
	f.deps[hops].DialTarget = nil
	f.deps[hops].LookupIPv4 = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "target.test" {
			return nil, fmt.Errorf("unexpected resolution %s", host)
		}
		f.resolved.Add(1)
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	return f
}
func (f *relayFixture) start(t *testing.T) {
	t.Helper()
	f.engines = make([]*Engine, len(f.deps))
	for i := len(f.deps) - 1; i >= 0; i-- {
		e := NewEngine(f.deps[i])
		f.engines[i] = e
		s := f.snapshots[i]
		s.Digest = SnapshotDigest(s)
		if err := e.Apply(s); err != nil {
			t.Fatalf("node %d apply: %v", i, err)
		}
	}
}
func (f *relayFixture) connect(t *testing.T) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(f.snapshots[0].Rules[0].ListenPort))), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	return c
}
func relayExchange(t *testing.T, c net.Conn) {
	t.Helper()
	b := []byte("real-chain-bytes")
	if _, err := c.Write(b); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(b))
	if _, err := io.ReadFull(c, out); err != nil || string(out) != string(b) {
		t.Fatalf("chain exchange %q: %v", out, err)
	}
}
func TestChainRelayRealAndOfflineRestore(t *testing.T) {
	for _, hops := range []int{2, 3} {
		t.Run(fmt.Sprint(hops), func(t *testing.T) {
			f := newRelayFixture(t, hops)
			f.start(t)
			c := f.connect(t)
			relayExchange(t, c)
			c.Close()
			for i := 1; i < hops; i++ {
				var state State
				raw, err := json.Marshal(f.engines[i].State())
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				if len(state.Rules) != 0 || len(state.ExitGroups) != 1 {
					t.Fatalf("relay state lost next group: %+v", state)
				}
				f.engines[i].Close()
				f.deps[i].Listen = nil
				f.engines[i] = NewEngine(f.deps[i])
				if err = f.engines[i].Restore(state); err != nil {
					t.Fatal(err)
				}
			}
			c = f.connect(t)
			defer c.Close()
			relayExchange(t, c)
			if f.accepted.Load() != 2 || f.resolved.Load() != 2 {
				t.Fatalf("terminal observations %d/%d", f.accepted.Load(), f.resolved.Load())
			}
		})
	}
}
func TestChainRejectsFreeNextGroup(t *testing.T) {
	var wire strings.Builder
	if err := writeTunnelMessage(&wire, map[string]any{"rule_id": "r", "next_exit_group_id": "forged"}); err != nil {
		t.Fatal(err)
	}
	var req tunnelRequest
	if readTunnelMessage(strings.NewReader(wire.String()), &req) == nil {
		t.Fatal("untrusted next group silently ignored")
	}
}

func TestChainRelaySameGenerationNextRevocation(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	s := proto.Clone(f.snapshots[1]).(*controlpb.ConfigSnapshot)
	s.Revision++
	s.TunnelServer.Grants[0].AuthorizedNextNodeIds = nil
	s.Digest = SnapshotDigest(s)
	if err := f.engines[1].Apply(s); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(2200 * time.Millisecond))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("revoked next still carries stream")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("online relay failed to cancel offline successor")
	}
	before := f.accepted.Load()
	c2 := f.connect(t)
	defer c2.Close()
	c2.Write([]byte("forbidden"))
	if _, err := c2.Read(b[:]); err == nil {
		t.Fatal("revoked next accepted new stream")
	}
	if f.accepted.Load() != before {
		t.Fatal("revoked target connected")
	}
}

// Add a sibling relay whose outgoing WSS route reaches the same terminal.
func (f *relayFixture) addBackup(t *testing.T) int { return f.addRelaySibling(t, 1) }
func (f *relayFixture) addRelaySibling(t *testing.T, hop int) int {
	t.Helper()
	idx := len(f.snapshots)
	id := fmt.Sprintf("backup%d", idx)
	deps := f.deps[hop]
	deps.NodeID = id
	deps.Listen = nil
	s := proto.Clone(f.snapshots[hop]).(*controlpb.ConfigSnapshot)
	s.TunnelServer.ListenPort = reserveTestPort(t, &deps)
	incoming := proto.Clone(f.snapshots[hop-1].ExitGroups[0].Exits[0]).(*controlpb.TunnelEndpoint)
	incoming.NodeId = id
	incoming.Port = s.TunnelServer.ListenPort
	incoming.EndpointConfigDigest = EndpointConfigDigest(incoming)
	groupID := f.snapshots[hop-1].ExitGroups[0].Id
	for i, current := range f.snapshots {
		for _, group := range current.ExitGroups {
			if group.Id == groupID {
				ep := proto.Clone(incoming).(*controlpb.TunnelEndpoint)
				ep.EntryId = f.deps[i].NodeID
				group.Exits = append(group.Exits, ep)
			}
		}
		for _, r := range current.Rules {
			if r.ExitGroupId == groupID {
				r.AuthorizedExitIds = append(r.AuthorizedExitIds, id)
			}
		}
		for _, g := range current.GetTunnelServer().GetGrants() {
			if g.NextExitGroupId == groupID {
				g.AuthorizedNextNodeIds = append(g.AuthorizedNextNodeIds, id)
			}
		}
	}
	for _, group := range s.ExitGroups {
		for _, ep := range group.Exits {
			ep.EntryId = id
			for i, next := range f.snapshots {
				if f.deps[i].NodeID != ep.NodeId {
					continue
				}
				next.TunnelServer.Peers = append(next.TunnelServer.Peers, &controlpb.TunnelPeer{EntryId: id, Credential: ep.Credential})
				grant := proto.Clone(next.TunnelServer.Grants[0]).(*controlpb.TunnelGrant)
				grant.EntryId = id
				next.TunnelServer.Grants = append(next.TunnelServer.Grants, grant)
			}
		}
	}
	f.snapshots = append(f.snapshots, s)
	f.deps = append(f.deps, deps)
	edges := []*controlpb.ChainEdgeAuthorization{}
	for _, current := range f.snapshots {
		hop := uint32(1)
		if current.TunnelServer != nil {
			hop = current.TunnelServer.Grants[0].Rule.HopIndex + 1
		}
		for _, group := range current.ExitGroups {
			for _, ep := range group.Exits {
				edges = append(edges, &controlpb.ChainEdgeAuthorization{FromNodeId: ep.EntryId, ToNodeId: ep.NodeId, AddressId: ep.AddressId, HopIndex: hop, EndpointConfigDigest: ep.EndpointConfigDigest})
			}
		}
	}
	f.snapshots[0].Rules[0].ChainAuthorization.Edges = edges
	for i := 1; i < len(f.snapshots); i++ {
		for _, grant := range f.snapshots[i].TunnelServer.Grants {
			grant.Rule.ChainAuthorization.Edges = ChainEvidenceEdges(edges, f.deps[i].NodeID, grant.Rule.HopIndex, grant.EntryId)
		}
	}
	return idx
}
func TestChainRelayAsymmetricFailover(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.addBackup(t)
	f.deps[1].DialTunnel = func(context.Context, string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("B1 to C unreachable")}
	}
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	observations := f.engines[0].ChainFailures()
	if len(observations) != 1 || observations[0].Class != "downstream_next_hop" || observations[0].Edge.FromNodeID != "1" || observations[0].Edge.ToNodeID != "2" {
		t.Fatalf("failure misattributed: %+v", observations)
	}
	for _, health := range f.engines[0].ExitHealth() {
		if health.State == "unhealthy" || health.FailureClass == "entry_exit" {
			t.Fatalf("downstream poisoned A to B: %+v", health)
		}
	}
	if f.engines[1].ExitHealth()[0].State != "unhealthy" {
		t.Fatal("real local edge failure not counted")
	}
	// The cached unhealthy edge must still be retryable on subsequent streams.
	for _, recovering := range []bool{false, true} {
		if recovering {
			next := f.engines[1].tunnelClients[endpointKey(f.snapshots[1].ExitGroups[0].Exits[0])]
			next.recordProbe(time.Now(), "")
			if f.engines[1].ExitHealth()[0].State != "recovering" {
				t.Fatal("recovery window missing")
			}
		}
		another := f.connect(t)
		relayExchange(t, another)
		another.Close()
		observed := f.engines[0].ChainFailures()
		if len(observed) != 1 || observed[0].Class != "downstream_next_hop" || observed[0].Edge.FromNodeID != "1" || observed[0].Edge.ToNodeID != "2" {
			t.Fatalf("repeat failure misattributed: %+v", observed)
		}
		for _, health := range f.engines[0].ExitHealth() {
			if health.State == "unhealthy" {
				t.Fatal("repeat downstream failure poisoned entry edge")
			}
		}
	}
}

func TestChainRelayTargetFailureTriesSibling(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.addBackup(t)
	// Both relays reach C; C refuses the first actual target connection once.
	var attempts atomic.Int32
	f.deps[2].DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("target temporarily unavailable")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	if attempts.Load() != 2 {
		t.Fatalf("target attempts %d", attempts.Load())
	}
	failures := f.engines[0].ChainFailures()
	if len(failures) != 1 || failures[0].Class != "exit_target" || failures[0].Edge.FromNodeID != "2" {
		t.Fatalf("target ownership lost: %+v", failures)
	}
	for _, h := range f.engines[0].ExitHealth() {
		if h.State == "unhealthy" {
			t.Fatal("target poisoned local edge")
		}
	}
}

func TestChainRelayInvalidSourceOnlyBlocksTerminal(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.snapshots[0].OutboundIpv4 = "192.0.2.253"
	f.snapshots[1].OutboundIpv4 = "192.0.2.253"
	f.snapshots[2].OutboundIpv4 = "127.0.0.1"
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	s := proto.Clone(f.snapshots[2]).(*controlpb.ConfigSnapshot)
	s.OutboundIpv4 = "192.0.2.253"
	s.Revision++
	s.Digest = SnapshotDigest(s)
	if f.engines[2].Apply(s) == nil {
		t.Fatal("invalid final source accepted")
	}
	r := f.engines[2].TunnelGrantResults()
	if len(r) != 1 || r[0].ApplyError == "" || r[0].AppliedRevision != 0 {
		t.Fatal("missing final grant error", r)
	}
}

func TestChainRelaySharedBudgetCancellation(t *testing.T) {
	var faults atomic.Bool
	f := newRelayFixture(t, 3)
	var entered atomic.Int32
	f.deps[2].DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		if !faults.Load() {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		entered.Add(1)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.start(t)
	f.disconnectSessions()
	faults.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	begin := time.Now()
	_, err := f.engines[0].rules["r"].selection.Load().dial(ctx)
	if err == nil || time.Since(begin) > 450*time.Millisecond {
		t.Fatalf("budget reset: %v %s", err, time.Since(begin))
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		zero := true
		for _, e := range f.engines {
			b, _ := e.Connections()
			e.resources.mu.Lock()
			released := e.resources.connections == 0 && e.resources.pending == 0
			e.resources.mu.Unlock()
			zero = zero && b == 0 && released
		}
		if zero {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancelled multi-hop dial leaked business connections", entered.Load())
}

func TestChainEntrySameGenerationNextRevocation(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	s := proto.Clone(f.snapshots[0]).(*controlpb.ConfigSnapshot)
	s.Revision++
	s.Rules[0].AuthorizedExitIds = nil
	s.Digest = SnapshotDigest(s)
	if err := f.engines[0].Apply(s); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(2200 * time.Millisecond))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("entry retained revoked successor")
	} else if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("entry did not close revoked existing stream")
	}
}
func TestChainRelayChangedRouteCheckpoint(t *testing.T) {
	f := newRelayFixture(t, 2)
	var disk []byte
	writes := 0
	fail := false
	f.deps[1].Persist = func(s State) error {
		writes++
		if fail && writes == 2 {
			return errors.New("final persist failed")
		}
		var err error
		disk, err = json.Marshal(s)
		return err
	}
	f.start(t)
	s := proto.Clone(f.snapshots[1]).(*controlpb.ConfigSnapshot)
	s.Revision++
	s.ExitGroups[0].Exits[0].Port++
	s.ExitGroups[0].Exits[0].EndpointConfigDigest = EndpointConfigDigest(s.ExitGroups[0].Exits[0])
	s.TunnelServer.Grants[0].Rule.ChainAuthorization.Edges[1].EndpointConfigDigest = s.ExitGroups[0].Exits[0].EndpointConfigDigest
	s.Digest = SnapshotDigest(s)
	writes = 0
	fail = true
	if f.engines[1].Apply(s) == nil {
		t.Fatal("missing disk failure")
	}
	var state State
	if err := json.Unmarshal(disk, &state); err != nil {
		t.Fatal(err)
	}
	if state.TunnelServer != nil {
		t.Fatal("checkpoint can restore obsolete relay grant")
	}
	if state.HighestRevision != 2 {
		t.Fatal("high water lost")
	}
}

func TestChainRelayResourceFailureStopsRetry(t *testing.T) {
	f := newRelayFixture(t, 2)
	backup := f.addBackup(t)
	f.deps[1].Limits.MaxConnections = 1
	f.start(t)
	first := f.connect(t)
	defer first.Close()
	relayExchange(t, first)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := f.engines[0].rules["r"].selection.Load().dial(ctx)
	if classifyTunnel(err) != failureLocal {
		t.Fatalf("capacity lost classification: %v", err)
	}
	if b, _ := f.engines[backup].Connections(); b != 0 || f.accepted.Load() != 1 {
		t.Fatal("resource error retried sibling")
	}
	for _, h := range f.engines[0].ExitHealth() {
		if h.State == "unhealthy" {
			t.Fatal("resource poisoned edge")
		}
	}
}
func TestChainRelayHalfCloseSlowIOAndTraffic(t *testing.T) {
	f := newRelayFixture(t, 3)
	handler := func(c net.Conn) {
		var body []byte
		b := make([]byte, 2048)
		for {
			n, err := c.Read(b)
			body = append(body, b[:n]...)
			if err != nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		for len(body) > 0 {
			n := min(4096, len(body))
			if _, err := c.Write(body[:n]); err != nil {
				return
			}
			body = body[n:]
			time.Sleep(time.Millisecond)
		}
	}
	f.targetHandler.Store(&handler)
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	body := strings.Repeat("slow-relay", 30000)
	if _, err := io.WriteString(c, body); err != nil {
		t.Fatal(err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(c)
	if err != nil || string(out) != body {
		t.Fatalf("multi-hop FIN/slow IO %d %v", len(out), err)
	}
	traffic := f.engines[0].Traffic()
	var up, down uint64
	for _, sample := range traffic {
		up += sample.UploadBytes
		down += sample.DownloadBytes
	}
	if up != uint64(len(body)) || down != up {
		t.Fatalf("entry meter %d/%d", up, down)
	}
	for _, e := range f.engines[1:] {
		if len(e.Traffic()) != 0 {
			t.Fatal("relay repeated rule accounting")
		}
	}
}
func TestChainRelayRSTDoesNotReplay(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.addBackup(t)
	handler := func(c net.Conn) { b := make([]byte, 1); c.Read(b); c.(*net.TCPConn).SetLinger(0) }
	f.targetHandler.Store(&handler)
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	c.Write([]byte("business"))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("RST not propagated")
	}
	time.Sleep(30 * time.Millisecond)
	if f.accepted.Load() != 1 {
		t.Fatal("established flow was replayed")
	}
}
func TestChainRelayFinalSourceRestoreAndCheckpoint(t *testing.T) {
	for _, change := range []string{"restart", "revoke", "source"} {
		t.Run(change, func(t *testing.T) {
			f := newRelayFixture(t, 2)
			f.snapshots[2].OutboundIpv4 = "127.0.0.1"
			var disk []byte
			writes := 0
			fail := false
			f.deps[2].Persist = func(s State) error {
				writes++
				if fail && writes == 2 {
					return errors.New("final disk failure")
				}
				var err error
				disk, err = json.Marshal(s)
				return err
			}
			f.start(t)
			c := f.connect(t)
			relayExchange(t, c)
			c.Close()
			if ip := <-f.sources; ip != "127.0.0.1" {
				t.Fatal("wrong final source", ip)
			}
			if change != "restart" {
				s := proto.Clone(f.snapshots[2]).(*controlpb.ConfigSnapshot)
				s.Revision++
				if change == "revoke" {
					s.TunnelServer.Grants = nil
				} else {
					s.OutboundIpv4 = "192.0.2.253"
				}
				s.Digest = SnapshotDigest(s)
				writes = 0
				fail = true
				if f.engines[2].Apply(s) == nil {
					t.Fatal("disk failure absent")
				}
			}
			f.engines[2].Close()
			var state State
			if err := json.Unmarshal(disk, &state); err != nil {
				t.Fatal(err)
			}
			f.deps[2].Listen = nil
			f.deps[2].Persist = nil
			f.engines[2] = NewEngine(f.deps[2])
			if err := f.engines[2].Restore(state); err != nil {
				t.Fatal(err)
			}
			if change == "restart" {
				c = f.connect(t)
				defer c.Close()
				relayExchange(t, c)
				if ip := <-f.sources; ip != "127.0.0.1" {
					t.Fatal(ip)
				}
			} else {
				c = f.connect(t)
				defer c.Close()
				c.Write([]byte("forbidden"))
				var b [1]byte
				if _, err := c.Read(b[:]); err == nil {
					t.Fatal("checkpoint resurrected target")
				}
				if f.accepted.Load() != 1 {
					t.Fatal("checkpoint target connected")
				}
				if f.engines[2].Apply(f.snapshots[2]) == nil {
					t.Fatal("old generation restored")
				}
			}
		})
	}
}
func TestChainRelayWireAuthorizationRejectsForgery(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	client := f.engines[0].rules["r"].selection.Load().clients[0]
	rule := f.snapshots[0].Rules[0]
	for _, mutate := range []func(*tunnelRequest){func(r *tunnelRequest) { r.ChainID = "other" }, func(r *tunnelRequest) { r.HopIndex++ }, func(r *tunnelRequest) { r.EntryID = "original-entry-forged" }, func(r *tunnelRequest) { r.TargetPort++ }, func(r *tunnelRequest) { r.Generation-- }, func(r *tunnelRequest) { r.ChainFingerprint = "old" }} {
		request := streamAuthorization("0", rule)
		request.rule = rule
		mutate(&request)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := client.open(ctx, request)
		cancel()
		if conn != nil {
			conn.Close()
		}
		if err == nil || classifyTunnel(err) != failureConfig {
			t.Fatalf("forgery accepted: %+v %v", request, err)
		}
	}
	if f.accepted.Load() != 0 {
		t.Fatal("forgery reached target")
	}
}
func TestChainReadinessProjectsLargeTrustedTopology(t *testing.T) {
	f := newRelayFixture(t, 2)
	for i := 0; i < 60; i++ {
		f.snapshots[0].Rules[0].ChainAuthorization.Edges = append(f.snapshots[0].Rules[0].ChainAuthorization.Edges, &controlpb.ChainEdgeAuthorization{FromNodeId: fmt.Sprintf("unused%d", i), ToNodeId: "2", HopIndex: 2, AddressId: "public", EndpointConfigDigest: strings.Repeat("a", 64)})
	}
	raw, _ := json.Marshal(f.snapshots[0].Rules[0])
	if len(raw) <= 4096 {
		t.Fatal("fixture too small")
	}
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
}
func TestChainRelayAtMostThreeDifferentCandidates(t *testing.T) {
	var faults atomic.Bool
	f := newRelayFixture(t, 2)
	for i := 0; i < 3; i++ {
		f.addBackup(t)
	}
	counts := make([]atomic.Int32, len(f.deps))
	for i := range f.deps {
		if i == 0 || i == 2 {
			continue
		}
		index := i
		f.deps[i].DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
			if !faults.Load() {
				return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
			}
			counts[index].Add(1)
			return nil, errors.New("actual next edge unavailable")
		}
	}
	f.start(t)
	f.disconnectSessions()
	faults.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := f.engines[0].rules["r"].selection.Load().dial(ctx)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("all broken chain succeeded")
	}
	for _, i := range []int{1, 3, 4} {
		if counts[i].Load() != 1 {
			t.Fatalf("candidate %d attempts %d", i, counts[i].Load())
		}
	}
	if counts[5].Load() != 0 || f.accepted.Load() != 0 {
		t.Fatal("candidate cap/mandatory group bypassed")
	}
}
func TestChainGrantResultsConcurrentReadAndApply(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			default:
				f.engines[1].TunnelGrantResults()
				f.engines[1].ChainFailures()
			}
		}
	}()
	defer func() { close(done); <-finished }()
	for i := 0; i < 10; i++ {
		s := proto.Clone(f.snapshots[1]).(*controlpb.ConfigSnapshot)
		s.Revision += uint64(i + 1)
		s.Digest = SnapshotDigest(s)
		if err := f.engines[1].Apply(s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChainRelayBudgetDecreasesAcrossSlowRetries(t *testing.T) {
	var faults atomic.Bool
	f := newRelayFixture(t, 2)
	backup := f.addBackup(t)
	f.deps[0].Limits.DialTimeout = 320 * time.Millisecond
	budgets := make(chan time.Duration, 8)
	pause := func(ctx context.Context, d time.Duration) error {
		deadline, _ := ctx.Deadline()
		budgets <- time.Until(deadline)
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	f.deps[1].DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		if !faults.Load() {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		if err := pause(ctx, 110*time.Millisecond); err != nil {
			return nil, err
		}
		return nil, errors.New("first suffix unavailable")
	}
	f.deps[backup].DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		if !faults.Load() {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		if err := pause(ctx, 70*time.Millisecond); err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}
	f.deps[2].DialTarget = func(ctx context.Context, address string) (net.Conn, error) {
		if !faults.Load() {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		deadline, _ := ctx.Deadline()
		budgets <- time.Until(deadline)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.start(t)
	f.disconnectSessions()
	faults.Store(true)
	begin := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := f.engines[0].rules["r"].selection.Load().dial(ctx)
	if conn != nil {
		conn.Close()
	}
	elapsed := time.Since(begin)
	if err == nil || elapsed > 550*time.Millisecond || elapsed < 260*time.Millisecond {
		t.Fatalf("shared 320ms budget %s %v", elapsed, err)
	}
	a, b, c := <-budgets, <-budgets, <-budgets
	if !(a > b && b > c && a <= 320*time.Millisecond && b < 240*time.Millisecond && c < 170*time.Millisecond) {
		t.Fatalf("budgets not decreasing: %s %s %s", a, b, c)
	}
	t.Logf("entry limit=320ms elapsed=%s downstream budgets=%s,%s,%s", elapsed, a, b, c)
}
func TestChainRelayRejectsTruncatedRouteAndStaleGrantResult(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	state := f.engines[1].State()
	state.TunnelServer.Grants[0].NextExitGroupId = ""
	state.TunnelServer.Grants[0].AuthorizedNextNodeIds = nil
	e := NewEngine(Dependencies{})
	defer e.Close()
	if e.Restore(state) == nil {
		t.Fatal("truncated relay restored as terminal")
	}
	state = f.engines[1].State()
	state.TunnelGrantResults[0].Generation--
	if e.Restore(state) == nil {
		t.Fatal("mixed generation cached result accepted")
	}
}
func TestChainRelayFailureObservationBoundary(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.addBackup(t)
	f.deps[1].DialTunnel = func(context.Context, string) (net.Conn, error) { return nil, errors.New("edge refused") }
	f.start(t)
	c := f.connect(t)
	relayExchange(t, c)
	c.Close()
	if len(f.engines[0].ChainFailures()) == 0 {
		t.Fatal("missing real failure")
	}
	old := proto.Clone(f.snapshots[0].Rules[0]).(*controlpb.ForwardRule)
	s := proto.Clone(f.snapshots[0]).(*controlpb.ConfigSnapshot)
	s.Revision++
	s.Rules = nil
	s.Digest = SnapshotDigest(s)
	if err := f.engines[0].Apply(s); err != nil {
		t.Fatal(err)
	}
	f.engines[0].recordChainFailure(old, &tunnelFailure{class: failureDownstream, edge: chainEdgeFailure(old, f.snapshots[0].ExitGroups[0].Exits[0])})
	if len(f.engines[0].ChainFailures()) != 0 {
		t.Fatal("late removed rule failure revived")
	}
}

func TestChainGrantResultsNotConfirmedBeforeDurableWrite(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	e := f.engines[1]
	s := proto.Clone(f.snapshots[1]).(*controlpb.ConfigSnapshot)
	s.Revision++
	writes := 0
	e.deps.Persist = func(State) error {
		writes++
		if writes == 2 {
			for _, r := range e.TunnelGrantResults() {
				if r.AppliedRevision == s.Revision && r.ApplyError == "" {
					t.Error("grant exposed success before durable commit")
				}
			}
			return errors.New("disk failed")
		}
		return nil
	}
	s.Digest = SnapshotDigest(s)
	if e.Apply(s) == nil {
		t.Fatal("missing disk failure")
	}
	for _, r := range e.TunnelGrantResults() {
		if r.AppliedRevision != 0 || r.ApplyError == "" {
			t.Fatal("failed write confirmed grant")
		}
	}
}

func TestChainGrantBindFailureResultSurvivesRestore(t *testing.T) {
	f := newRelayFixture(t, 2)
	s := f.snapshots[1]
	s.Digest = SnapshotDigest(s)
	e := NewEngine(Dependencies{Listen: func(string, string) (net.Listener, error) { return nil, errors.New("port occupied") }})
	defer e.Close()
	if e.Apply(s) == nil {
		t.Fatal("bind unexpectedly succeeded")
	}
	state := e.State()
	if len(state.TunnelGrantResults) != 1 || state.TunnelGrantResults[0].ApplyError == "" {
		t.Fatal("missing failed result")
	}
	restored := NewEngine(Dependencies{})
	defer restored.Close()
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	if got := restored.TunnelGrantResults(); len(got) != 1 || !proto.Equal(got[0], state.TunnelGrantResults[0]) {
		t.Fatal("failed grant result lost on restore", got)
	}
}

func TestChainRelayNestedBackupBudget(t *testing.T) {
	var faults atomic.Bool
	f := newRelayFixture(t, 3)
	f.addRelaySibling(t, 1)
	cBackup := f.addRelaySibling(t, 2)
	f.deps[0].Limits.DialTimeout = 500 * time.Millisecond
	budgets := make(chan time.Duration, 8)
	firstAddresses := make(chan string, 4)
	delay := func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		budgets <- time.Until(deadline)
		timer := time.NewTimer(40 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	f.deps[1].DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		if !faults.Load() {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		firstAddresses <- address
		if err := delay(ctx); err != nil {
			return nil, err
		}
		return nil, errors.New("B1 suffix unavailable")
	}
	f.deps[2].DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		if !faults.Load() {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		if err := delay(ctx); err != nil {
			return nil, err
		}
		return nil, errors.New("C1 suffix unavailable")
	}
	f.deps[cBackup].DialTunnel = func(ctx context.Context, address string) (net.Conn, error) {
		if !faults.Load() {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
		if err := delay(ctx); err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	}
	f.start(t)
	f.disconnectSessions()
	faults.Store(true)
	start := time.Now()
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	if time.Since(start) > 650*time.Millisecond {
		t.Fatal("nested retry budget reset")
	}
	if len(firstAddresses) != 2 {
		t.Fatalf("B1 should attempt two distinct C members: %d", len(firstAddresses))
	}
	if <-firstAddresses == <-firstAddresses {
		t.Fatal("nested group repeated candidate")
	}
	if len(budgets) != 4 {
		t.Fatalf("unexpected nested attempts %d", len(budgets))
	}
	previous := 501 * time.Millisecond
	for range 4 {
		remaining := <-budgets
		if remaining >= previous {
			t.Fatalf("nested budget grew: %s >= %s", remaining, previous)
		}
		previous = remaining
	}
	for _, h := range f.engines[0].ExitHealth() {
		if h.State == "unhealthy" {
			t.Fatal("nested suffix poisoned entry edge")
		}
	}
	t.Logf("two nested groups failed over; elapsed=%s last remaining=%s", time.Since(start), previous)
}

func TestChainRestoreRejectsStaleFailedGrantResult(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	state := f.engines[1].State()
	state.TunnelGrantResults[0].Generation--
	state.TunnelGrantResults[0].AppliedRevision = 0
	state.TunnelGrantResults[0].ApplyError = "old failed generation"
	restored := NewEngine(Dependencies{})
	defer restored.Close()
	if err := restored.Restore(state); err == nil {
		t.Fatal("stale failed result accepted for actual current grant")
	}
}

func TestChainEstablishmentEvidenceIsRealAndKeepsFailureHistory(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	if len(f.engines[0].ChainEstablishments()) != 0 {
		t.Fatal("probe fabricated business establishment")
	}
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	successes := f.engines[0].ChainEstablishments()
	if len(successes) != 1 || successes[0].Class != "established" || successes[0].Edge.HopIndex != 1 {
		t.Fatal("missing real complete-path evidence", successes)
	}
}

func TestChainTerminalBusinessSuccessHasTargetEvidence(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	c := f.connect(t)
	defer c.Close()
	relayExchange(t, c)
	outcomes := f.engines[2].ChainEstablishments()
	found := false
	for _, outcome := range outcomes {
		if outcome.Class == "established" && outcome.Edge.ToNodeID == "" && outcome.Edge.HopIndex == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("terminal target success evidence missing", outcomes)
	}
}

func TestChainReadinessAndUpgradeCheckCompleteSuffixWithoutTarget(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.start(t)
	for _, e := range f.engines {
		if err := e.ListenCheckPort(0); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []int{0, 1} {
		if err := f.engines[i].UpgradeReady(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if f.accepted.Load() != 0 || f.resolved.Load() != 0 {
		t.Fatal("readiness dialed/resolved final target")
	}
	f.engines[2].Close()
	for _, i := range []int{0, 1} {
		if err := f.engines[i].UpgradeReady(context.Background()); err == nil {
			t.Fatal("local next-hop handshake hid broken complete suffix", i)
		}
	}
	if f.accepted.Load() != 0 {
		t.Fatal("failed readiness dialed target")
	}
}

// Faults begin after the real authorized preflight. Closing the established
// carriers forces the measured business attempt to exercise actual dial paths.
func (f *relayFixture) disconnectSessions() {
	for _, e := range f.engines {
		for _, c := range e.tunnelClients {
			c.mu.Lock()
			if c.session != nil {
				c.session.Close()
			}
			c.mu.Unlock()
		}
	}
}

func TestChainRelayLocalResourceRecoveryHasOwnBusinessEvidence(t *testing.T) {
	f := newRelayFixture(t, 2)
	f.deps[1].Limits.MaxConnectionsPerRule = 1
	f.start(t)
	if len(f.engines[1].ChainEstablishments()) != 0 {
		t.Fatal("preflight fabricated local recovery")
	}
	first := f.connect(t)
	relayExchange(t, first)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := f.engines[0].rules["r"].selection.Load().dial(ctx)
	var failure *tunnelFailure
	if !errors.As(err, &failure) || failure.class != failureLocal || failure.edge == nil || failure.edge.FromNodeID != "1" || failure.edge.ToNodeID != "" {
		t.Fatal("missing exact relay-local capacity failure", err)
	}
	first.Close()
	deadline := time.Now().Add(time.Second)
	for {
		f.engines[1].resources.mu.Lock()
		n := f.engines[1].resources.rules["r"]
		f.engines[1].resources.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relay did not return rule quota")
		}
		time.Sleep(time.Millisecond)
	}
	recovered := f.connect(t)
	defer recovered.Close()
	relayExchange(t, recovered)
	found := false
	for _, o := range f.engines[1].ChainEstablishments() {
		if o.Class == "local_established" && o.Edge.FromNodeID == "1" && o.Edge.ToNodeID == "" && o.Edge.HopIndex == 1 && o.Edge.RuleGeneration == 7 {
			found = true
		}
	}
	if !found {
		t.Fatal("real relay recovery lacks local resource identity", f.engines[1].ChainEstablishments())
	}
}

func TestChainResourceOriginOrderPreservedAcrossRelays(t *testing.T) {
	for _, limit := range []string{"node", "rule", "pending"} {
		t.Run(limit, func(t *testing.T) {
			f := newRelayFixture(t, 2)
			for i := range f.deps {
				f.deps[i].ProcessInstance = fmt.Sprint("process-", i)
			}
			switch limit {
			case "node":
				f.deps[2].Limits.MaxConnections = 1
			case "rule":
				f.deps[2].Limits.MaxConnectionsPerRule = 1
			case "pending":
				f.deps[2].Limits.MaxPendingDials = 1
			}
			f.start(t)
			first := f.connect(t)
			relayExchange(t, first)
			first.Close()
			waitTunnelResourcesReleased(t, f.engines[2])
			owner := f.engines[2]
			var before uint64
			for _, o := range owner.ChainEstablishments() {
				if o.Class == "local_established" {
					before = o.ResourceOrder.Sequence
				}
			}
			if before == 0 {
				t.Fatal("real local success has no origin sequence")
			}
			// Occupy the exact shared resource pool without admitting another target.
			lease := owner.resources.acquire("r")
			if lease == nil {
				t.Fatal("cannot occupy quota")
			}
			defer lease.release()
			if limit != "pending" {
				lease.dialDone()
			}
			calls := f.resolved.Load()
			// Deliberately make the relays' counters unrelated to the owner's sequence.
			for _, e := range f.engines[:2] {
				e.chainFailuresMu.Lock()
				e.chainObservationSequence += 100
				e.chainFailuresMu.Unlock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := f.engines[0].rules["r"].selection.Load().dial(ctx)
			var failure *tunnelFailure
			if !errors.As(err, &failure) || failure.class != failureLocal || failure.edge == nil || failure.edge.FromNodeID != "2" || failure.edge.ToNodeID != "" || failure.resourceOrder == nil {
				t.Fatal("missing actual resource owner", err)
			}
			order := *failure.resourceOrder
			if order.Instance != "process-2" || order.Sequence <= before || order.Sequence >= 100 {
				t.Fatal("relay replaced owner order", order)
			}
			for _, e := range f.engines[:2] {
				found := false
				for _, o := range e.ChainFailures() {
					if o.Class == "local_resource" && o.Edge.FromNodeID == "2" {
						found = true
						if o.ResourceOrder == nil || *o.ResourceOrder != order || o.Sequence <= order.Sequence {
							t.Fatal("origin lost at relay", o)
						}
					}
				}
				if !found {
					t.Fatal("relay did not preserve resource failure")
				}
			}
			if f.resolved.Load() != calls {
				t.Fatal("quota rejection resolved/dialed target")
			}
			lease.release()
			recovered := f.connect(t)
			defer recovered.Close()
			relayExchange(t, recovered)
			for _, o := range owner.ChainEstablishments() {
				if o.Class == "local_established" && (o.ResourceOrder.Instance != order.Instance || o.ResourceOrder.Sequence <= order.Sequence) {
					t.Fatal("recovery did not share owner sequence domain", o)
				}
			}
		})
	}
}

func TestChainResourceOrderRejectsInvalidScope(t *testing.T) {
	for _, scenario := range []string{"valid", "empty", "long", "zero", "target", "network", "no_edge"} {
		t.Run(scenario, func(t *testing.T) {
			order := &ChainResourceOrder{Instance: "owner", Sequence: 1}
			edge := &ChainFailure{FromNodeID: "b"}
			class := "local_resource"
			switch scenario {
			case "empty":
				order.Instance = ""
			case "long":
				order.Instance = strings.Repeat("x", 129)
			case "zero":
				order.Sequence = 0
			case "target":
				class = "exit_target"
			case "network":
				edge.ToNodeID = "c"
			case "no_edge":
				edge = nil
			}
			if ValidChainResourceOrder(order, class, edge) != (scenario == "valid") {
				t.Fatal("invalid resource provenance accepted")
			}
		})
	}
}

func TestChainResourceOriginCoalescingKeepsNewestOwnerRefusal(t *testing.T) {
	r := &controlpb.ForwardRule{Id: "r", ChainId: "chain", AuthorizationGeneration: 7, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: "fp"}}
	e := NewEngine(Dependencies{NodeID: "relay", ProcessInstance: "relay-process"})
	e.chainRules = map[string]*controlpb.ForwardRule{"r": r}
	edge := &ChainFailure{FromNodeID: "owner", RuleGeneration: 7, ChainFingerprint: "fp"}
	for _, sequence := range []uint64{5, 2} {
		e.recordChainFailure(r, &tunnelFailure{class: failureLocal, edge: edge, resourceOrder: &ChainResourceOrder{Instance: "owner-process", Sequence: sequence}})
	}
	got := e.ChainFailures()
	if len(got) != 1 || got[0].ResourceOrder.Sequence != 5 {
		t.Fatal("late older response erased newer owner refusal before heartbeat", got)
	}
}

func TestChainResourceOriginsSurviveOldInstanceArrivingLast(t *testing.T) {
	r := &controlpb.ForwardRule{Id: "r", ChainId: "chain", AuthorizationGeneration: 7, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: "fp"}}
	e := NewEngine(Dependencies{NodeID: "relay", ProcessInstance: "relay-process"})
	e.chainRules = map[string]*controlpb.ForwardRule{"r": r}
	edge := &ChainFailure{FromNodeID: "owner", RuleGeneration: 7, ChainFingerprint: "fp"}
	for _, instance := range []string{"current", "old-1", "old-2", "old-3"} {
		e.recordChainFailure(r, &tunnelFailure{class: failureLocal, edge: edge, resourceOrder: &ChainResourceOrder{Instance: instance, Sequence: 1}})
	}
	got := e.ChainFailures()
	if len(got) != 4 {
		t.Fatal("cross-instance arrival order lost unverified evidence", got)
	}
	found := false
	for _, o := range got {
		found = found || o.ResourceOrder.Instance == "current"
	}
	if !found {
		t.Fatal("current instance disappeared before node could queue it")
	}
}

func TestChainResourceCandidateBudgetDeliveryAndWriteFailure(t *testing.T) {
	now := time.Now()
	r := &controlpb.ForwardRule{Id: "r", ChainId: "chain", AuthorizationGeneration: 7, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: "fp"}}
	e := NewEngine(Dependencies{NodeID: "relay", Now: func() time.Time { return now }})
	e.chainRules = map[string]*controlpb.ForwardRule{"r": r}
	edge := &ChainFailure{FromNodeID: "owner", RuleGeneration: 7, ChainFingerprint: "fp"}
	for i := 0; i < chainResourceCandidateCount+2; i++ {
		e.recordChainFailure(r, &tunnelFailure{class: failureLocal, edge: edge, resourceOrder: &ChainResourceOrder{Instance: fmt.Sprint("instance-", i), Sequence: 1}})
	}
	if len(e.chainResources) != chainResourceCandidateCount || e.chainResourceBytes > chainResourceCandidateBytes || e.chainResourceGap.Count != 2 {
		t.Fatal("pending origin budget not enforced", len(e.chainResources), e.chainResourceBytes, e.chainResourceGap)
	}
	rejected := errors.New("durable write failed")
	if err := e.FlushChainResourceEvidence(func(outcomes []ChainFailureObservation, gap *ChainResourceGaps) error {
		if len(outcomes) != chainResourceCandidateCount || gap == nil || gap.Count != 2 || !gap.From.Equal(now) || !gap.To.Equal(now) {
			t.Fatal("wrong loss snapshot", len(outcomes), gap)
		}
		// An additional loss during the failing write must merge back exactly once.
		now = now.Add(time.Second)
		e.recordChainFailure(r, &tunnelFailure{class: failureLocal, edge: edge, resourceOrder: &ChainResourceOrder{Instance: "another", Sequence: 1}})
		return rejected
	}); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	if len(e.chainResources) != chainResourceCandidateCount || e.chainResourceGap.Count != 3 {
		t.Fatal("failed write acknowledged candidates or lost gap", len(e.chainResources), e.chainResourceGap)
	}
	if err := e.FlushChainResourceEvidence(func(outcomes []ChainFailureObservation, gap *ChainResourceGaps) error {
		if gap == nil || gap.Count != 3 || !gap.To.Equal(now) {
			t.Fatal("retry lost exact gap", gap)
		}
		// A newer same-instance observation must survive acknowledgement of this snapshot.
		e.recordChainFailure(r, &tunnelFailure{class: failureLocal, edge: edge, resourceOrder: &ChainResourceOrder{Instance: "instance-0", Sequence: 2}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(e.chainResources) != 1 || e.chainResourceGap.Count != 0 {
		t.Fatal("delivery did not reclaim or erased newer result", len(e.chainResources), e.chainResourceGap)
	}
	if err := e.FlushChainResourceEvidence(func([]ChainFailureObservation, *ChainResourceGaps) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(e.chainResources) != 0 || e.chainResourceBytes != 0 {
		t.Fatal("delivered histories accumulated")
	}
}

func TestChainResourceCandidateByteBudget(t *testing.T) {
	e := NewEngine(Dependencies{})
	for i := 0; i < chainResourceCandidateCount; i++ {
		o := ChainFailureObservation{Sequence: uint64(i + 1), RuleID: "r", ChainID: strings.Repeat("x", 1400), Class: "local_resource", ObservedAt: time.Now(), Edge: ChainFailure{FromNodeID: "owner"}, ResourceOrder: &ChainResourceOrder{Instance: fmt.Sprint(i), Sequence: 1}}
		if resourceObservationBytes(o) > 2048 {
			t.Fatal("fixture exceeded one event before total byte budget")
		}
		e.recordResourceCandidate(o)
	}
	if e.chainResourceBytes > chainResourceCandidateBytes || e.chainResourceGap.Count == 0 || len(e.chainResources) >= chainResourceCandidateCount {
		t.Fatal("byte budget not independent of count", e.chainResourceBytes, len(e.chainResources), e.chainResourceGap)
	}
}

func TestChainResourceGapScopesRetainLossConfigurationAndBoundMemory(t *testing.T) {
	now := time.Now()
	e := NewEngine(Dependencies{})
	// Force loss at the existing byte limit; changing the rule cannot rewrite it.
	e.chainResourceBytes = chainResourceCandidateBytes
	for i := 1; i <= 2; i++ {
		o := ChainFailureObservation{RuleID: "r", ChainID: "chain", Class: "local_resource", ObservedAt: now, ResourceOrder: &ChainResourceOrder{Instance: "owner", Sequence: uint64(i)}, Edge: ChainFailure{FromNodeID: "b", RuleGeneration: uint64(i), ChainFingerprint: fmt.Sprint("fp", i)}}
		e.recordResourceCandidate(o)
	}
	fail := errors.New("disk")
	if err := e.FlushChainResourceEvidence(func(_ []ChainFailureObservation, g *ChainResourceGaps) error {
		if g == nil || len(g.Scopes) != 2 || g.Count != 2 {
			t.Fatal("configuration boundary lost before delivery", g)
		}
		for scope, loss := range g.Scopes {
			if scope.RuleID != "r" || scope.Fingerprint != fmt.Sprint("fp", scope.Generation) || loss.Count != 1 {
				t.Fatal(scope, loss)
			}
		}
		return fail
	}); !errors.Is(err, fail) {
		t.Fatal(err)
	}
	if len(e.chainResourceGap.Scopes) != 2 || e.chainResourceGap.Count != 2 {
		t.Fatal("failed delivery discarded scopes", e.chainResourceGap)
	}
	g := ChainResourceGaps{}
	for i := 0; i < ChainResourceScopeCount+2; i++ {
		g.Add(ChainResourceScope{RuleID: fmt.Sprint("r", i), Generation: 1, Fingerprint: "fp"}, eventlog.Gap{Count: 1, From: now, To: now, Reason: "overflow"})
	}
	if g.Count != ChainResourceScopeCount+2 || len(g.Scopes) != ChainResourceScopeCount || g.Unscoped.Count != 2 || g.bytes > ChainResourceScopeBytes {
		t.Fatal("scope capacity lost count or exceeded budget", g.Count, len(g.Scopes), g.Unscoped, g.bytes)
	}
	// An already represented scope remains precise even when the budget is full.
	g.Add(ChainResourceScope{RuleID: "r0", Generation: 1, Fingerprint: "fp"}, eventlog.Gap{Count: 1, From: now, To: now, Reason: "overflow"})
	if g.Scopes[ChainResourceScope{RuleID: "r0", Generation: 1, Fingerprint: "fp"}].Count != 2 || g.Unscoped.Count != 2 {
		t.Fatal("overflow erased known scope")
	}
}

func TestChainResourceGapScopeByteBudget(t *testing.T) {
	now := time.Now()
	g := ChainResourceGaps{}
	for i := 0; i < ChainResourceScopeCount; i++ {
		g.Add(ChainResourceScope{RuleID: fmt.Sprintf("%04d", i) + strings.Repeat("\x01", 124), Generation: 1, Fingerprint: strings.Repeat("\x02", 64)}, eventlog.Gap{Count: 1, From: now, To: now, Reason: "overflow"})
	}
	if g.bytes > ChainResourceScopeBytes || g.Unscoped.Count == 0 || len(g.Scopes) >= ChainResourceScopeCount || g.Count != ChainResourceScopeCount {
		t.Fatal("scope byte budget did not bound memory", g.bytes, len(g.Scopes), g.Count, g.Unscoped)
	}
}
