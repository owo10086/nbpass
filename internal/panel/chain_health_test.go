package panel

import (
	"nbpass/internal/controlpb"
	"testing"
)

// The entry's own handshake must not cover a broken downstream edge.
func TestChainHealthRequiresEveryCurrentAuthorizedEdge(t *testing.T) {
	a := &controlpb.ForwardRule{Id: "r", ChainId: "chain", ExitGroupId: "b", AuthorizationGeneration: 7, AuthorizedExitIds: []string{"b"}}
	b := &controlpb.TunnelGrant{EntryId: "a", Rule: &controlpb.ForwardRule{Id: "r", ChainId: "chain", AuthorizationGeneration: 7}, NextExitGroupId: "c", AuthorizedNextNodeIds: []string{"c"}}
	c := &controlpb.TunnelGrant{EntryId: "b", Rule: &controlpb.ForwardRule{Id: "r", ChainId: "chain", AuthorizationGeneration: 7}}
	inputs := map[string]entryReadinessInputs{
		"a": {sessionLive: true, snapshot: &controlpb.ConfigSnapshot{Revision: 1, Rules: []*controlpb.ForwardRule{a}, ExitGroups: []*controlpb.ExitGroup{{Id: "b", Exits: []*controlpb.TunnelEndpoint{{NodeId: "b", AddressId: "ix", EndpointConfigDigest: "ab"}}}}}, health: map[[3]string]bool{{"b", "ix", "ab"}: true}},
		"b": {sessionLive: true, snapshot: &controlpb.ConfigSnapshot{Revision: 2, TunnelServer: &controlpb.TunnelServer{Grants: []*controlpb.TunnelGrant{b}}, ExitGroups: []*controlpb.ExitGroup{{Id: "c", Exits: []*controlpb.TunnelEndpoint{{NodeId: "c", AddressId: "ix", EndpointConfigDigest: "bc"}}}}}, health: map[[3]string]bool{}},
		"c": {sessionLive: true, snapshot: &controlpb.ConfigSnapshot{Revision: 3, TunnelServer: &controlpb.TunnelServer{Grants: []*controlpb.TunnelGrant{c}}}},
	}
	// Existing single-edge readiness is deliberately insufficient.
	if !inputs["a"].tunnelReady("r") {
		t.Fatal("fixture lacks healthy first edge")
	}
	grants := map[[3]string]*controlpb.TunnelGrantApplyResult{{"b", "a", "r"}: {Generation: 7, AppliedRevision: 2}, {"c", "b", "r"}: {Generation: 7, AppliedRevision: 3}}
	if chainPathReady("a", "r", inputs, grants) {
		t.Fatal("first handshake promoted incomplete chain")
	}
	v := inputs["b"]
	v.health[[3]string{"c", "ix", "bc"}] = true
	if !chainPathReady("a", "r", inputs, grants) {
		t.Fatal("complete path not ready")
	}
	grants[[3]string{"c", "b", "r"}].Generation = 6
	if chainPathReady("a", "r", inputs, grants) {
		t.Fatal("old grant became current")
	}
	grants[[3]string{"c", "b", "r"}].Generation = 7
	v = inputs["c"]
	v.sessionLive = false
	inputs["c"] = v
	if chainPathReady("a", "r", inputs, grants) {
		t.Fatal("offline terminal asserted current readiness")
	}
}
