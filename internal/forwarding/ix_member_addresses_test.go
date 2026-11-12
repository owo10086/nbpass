package forwarding

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// Use the wire contract so this test also catches a missing/generated-wrong field 280.
func memberEndpoint(t *testing.T, ep *controlpb.TunnelEndpoint, address string) *controlpb.TunnelEndpoint {
	t.Helper()
	out := proto.Clone(ep).(*controlpb.TunnelEndpoint)
	if err := protojson.Unmarshal([]byte(`{"address_id":"`+address+`"}`), out); err != nil {
		t.Fatal(err)
	}
	// Unmarshal replaces the message; merge only the identity into the full endpoint.
	full := proto.Clone(ep).(*controlpb.TunnelEndpoint)
	proto.Merge(full, out)
	return full
}

func TestIXMemberIdentityWireDigestAndCrossGroupValidation(t *testing.T) {
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	public := memberEndpoint(t, s.ExitGroups[0].Exits[0], "public")
	ix := memberEndpoint(t, public, "ix")
	if !sameTunnelRoute(s.ExitGroups[0].Exits[0], public) {
		t.Fatal("空地址身份未按同一公网路由解释")
	}
	if EndpointConfigDigest(public) == EndpointConfigDigest(ix) {
		t.Fatal("地址身份未进入摘要")
	}
	if EndpointConfigDigest(s.ExitGroups[0].Exits[0]) != EndpointConfigDigest(public) {
		t.Fatal("空身份未按旧公网摘要解释")
	}
	s.ExitGroups[0].Exits[0] = public
	s.ExitGroups = append(s.ExitGroups, &controlpb.ExitGroup{Id: "ix", Strategy: s.ExitGroups[0].Strategy, Exits: []*controlpb.TunnelEndpoint{ix}})
	s.Digest = SnapshotDigest(s)
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal("同服务器不同地址跨组被拒绝", err)
	}
	endpoints, _ := tunnelEndpoints(s.ExitGroups)
	if len(endpoints) != 2 {
		t.Fatal("两地址被合并", len(endpoints))
	}
	conflict := proto.Clone(s).(*controlpb.ConfigSnapshot)
	conflict.ExitGroups[1].Exits[0] = proto.Clone(public).(*controlpb.TunnelEndpoint)
	conflict.ExitGroups[1].Exits[0].Port++
	conflict.Digest = SnapshotDigest(conflict)
	if ValidateSnapshot(conflict) == nil {
		t.Fatal("同地址冲突未拒绝")
	}
	s.ExitGroups[0].Exits = append(s.ExitGroups[0].Exits, ix)
	s.Digest = SnapshotDigest(s)
	if ValidateSnapshot(s) == nil {
		t.Fatal("组内同服务器两地址未拒绝")
	}
}

func TestIXMemberClientsHealthRotationAndRestoreRemainIndependent(t *testing.T) {
	deps := Dependencies{}
	s := cachedTunnelFixture(t, &deps)
	public := memberEndpoint(t, s.ExitGroups[0].Exits[0], "public")
	ix := memberEndpoint(t, public, "ix")
	public.EndpointConfigDigest, ix.EndpointConfigDigest = EndpointConfigDigest(public), EndpointConfigDigest(ix)
	s.ExitGroups[0].Exits[0] = public
	g := &controlpb.ExitGroup{Id: "ix", Strategy: s.ExitGroups[0].Strategy, Exits: []*controlpb.TunnelEndpoint{ix}}
	s.ExitGroups = append(s.ExitGroups, g)
	r := proto.Clone(s.Rules[0]).(*controlpb.ForwardRule)
	r.Id, r.ExitGroupId, r.ListenPort = "ix-rule", "ix", reserveTestPort(t, &deps)
	s.Rules = append(s.Rules, r)
	s.TunnelServer.Grants = append(s.TunnelServer.Grants, &controlpb.TunnelGrant{EntryId: ix.EntryId, Rule: r})
	s.Digest = SnapshotDigest(s)
	e := NewEngine(deps)
	defer e.Close()
	if err := e.Apply(s); err != nil {
		t.Fatal(err)
	}
	_, groups := tunnelEndpoints(s.ExitGroups)
	pc := e.selectTunnel(s.Rules[0], groups["exit"]).clients[0]
	ic := e.selectTunnel(r, groups["ix"]).clients[0]
	if pc == ic {
		t.Fatal("两个地址复用客户端/会话")
	}
	pc.recordProbe(time.Now(), "")
	ic.linkFailed()
	if !pc.available() || ic.available() || len(e.ExitHealth()) != 2 {
		t.Fatal("两地址健康串用", e.ExitHealth())
	}
	rotated := proto.Clone(s).(*controlpb.ConfigSnapshot)
	for _, group := range rotated.ExitGroups {
		for _, ep := range group.Exits {
			ep.RotationId = "rotation"
			ep.Credential = public.Credential
		}
	}
	endpoints, _ := tunnelEndpoints(rotated.ExitGroups)
	e.applyTunnelClients(endpoints)
	if pc.currentEndpoint().RotationId != "rotation" || ic.currentEndpoint().RotationId != "rotation" || !pc.available() || ic.available() {
		t.Fatal("轮换未更新全部地址或丢失恢复观察")
	}
	// Both addresses are cacheable independently; restore opens fresh clients, not shared health/session objects.
	raw, err := json.Marshal(e.State())
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	var state State
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	restored := NewEngine(Dependencies{})
	defer restored.Close()
	if err = restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	_, groups = tunnelEndpoints(state.ExitGroups)
	pc = restored.selectTunnel(state.Rules[0], groups[state.Rules[0].ExitGroupId]).clients[0]
	ic = restored.selectTunnel(state.Rules[1], groups[state.Rules[1].ExitGroupId]).clients[0]
	if pc == ic || len(restored.ExitHealth()) != 2 {
		t.Fatal("恢复合并两地址")
	}
	pc.probeOnce(context.Background())
	if pc.lastProbe != "success" || ic.lastProbe != "" {
		t.Fatal("恢复后的健康串用")
	}
}
