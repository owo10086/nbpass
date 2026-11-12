package forwarding

import (
	"bytes"
	"context"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"nbpass/internal/controlpb"
	"testing"
	"time"
)

func TestChainProtocolAuthorizationFailureAndBudget(t *testing.T) {
	rule := &controlpb.ForwardRule{Id: "r", ChainId: "c", HopIndex: 1, AuthorizationGeneration: 7, TargetAddress: "8.8.8.8", TargetPort: 443, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: "fp", Edges: []*controlpb.ChainEdgeAuthorization{{FromNodeId: "a", ToNodeId: "b", AddressId: "ix", HopIndex: 1, EndpointConfigDigest: "ab"}, {FromNodeId: "b", ToNodeId: "c", AddressId: "public", HopIndex: 2, EndpointConfigDigest: "bc"}}}}
	req := tunnelRequest{RuleID: "r", EntryID: "a", Generation: 7, TargetAddress: rule.TargetAddress, TargetPort: 443, TargetFamily: rule.TargetFamily, ChainID: "c", HopIndex: 1, ChainFingerprint: "fp", RemainingBudgetMS: 9999}
	if matchStreamAuthorization(rule, tunnelAuth{EntryID: "a"}, req) == nil {
		t.Fatal("valid rejected")
	}
	mutations := []func(*tunnelRequest){func(r *tunnelRequest) { r.ChainID = "wrong" }, func(r *tunnelRequest) { r.HopIndex = 2 }, func(r *tunnelRequest) { r.ChainFingerprint = "old" }, func(r *tunnelRequest) { r.Generation = 6 }, func(r *tunnelRequest) { r.EntryID = "forged" }, func(r *tunnelRequest) { r.TargetPort = 80 }, func(r *tunnelRequest) { r.RemainingBudgetMS = 0 }, func(r *tunnelRequest) { r.RemainingBudgetMS = 10001 }}
	for _, mutate := range mutations {
		bad := req
		mutate(&bad)
		if matchStreamAuthorization(rule, tunnelAuth{EntryID: "a"}, bad) != nil {
			t.Fatal("bad accepted", bad)
		}
	}
	zero := proto.Clone(rule).(*controlpb.ForwardRule)
	zero.AuthorizationGeneration = 0
	if matchStreamAuthorization(zero, tunnelAuth{EntryID: "a"}, tunnelRequest{}) != nil {
		t.Fatal("chain accepted legacy zero")
	}
	failure := &ChainFailure{FromNodeID: "b", ToNodeID: "c", AddressID: "public", HopIndex: 2, RuleGeneration: 7, GrantGeneration: 7, ChainFingerprint: "fp", EndpointConfigDigest: "bc"}
	if !ValidateChainFailure(rule, "downstream_next_hop", failure) {
		t.Fatal("valid edge rejected")
	}
	for _, mutate := range []func(*ChainFailure){func(f *ChainFailure) { f.FromNodeID = "a" }, func(f *ChainFailure) { f.ToNodeID = "outside" }, func(f *ChainFailure) { f.AddressID = "other" }, func(f *ChainFailure) { f.HopIndex = 3 }, func(f *ChainFailure) { f.ChainFingerprint = "old" }, func(f *ChainFailure) { f.GrantGeneration = 6 }, func(f *ChainFailure) { f.EndpointConfigDigest = "old" }} {
		bad := *failure
		mutate(&bad)
		if ValidateChainFailure(rule, "downstream_next_hop", &bad) {
			t.Fatal("forged edge accepted", bad)
		}
	}
	target := *failure
	target.FromNodeID = "c"
	target.ToNodeID = ""
	target.AddressID = ""
	target.EndpointConfigDigest = ""
	if !ValidateChainFailure(rule, "exit_target", &target) {
		t.Fatal("terminal target rejected")
	}
	result := upstreamChainResult(tunnelResult{Failure: failureLink, Edge: failure})
	if result.Failure != failureDownstream || result.Edge != failure || chainRetryable(failureConfig) || chainRetryable(failureLocal) {
		t.Fatal("attribution or retry changed")
	}
	var wire bytes.Buffer
	if err := writeTunnelMessage(&wire, result); err != nil {
		t.Fatal(err)
	}
	var decoded tunnelResult
	if err := readTunnelMessage(&wire, &decoded); err != nil || decoded.Edge.FromNodeID != "b" {
		t.Fatal(decoded, err)
	}
	parent, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	ctx, stop := ChainBudget(parent, time.Second, 10*time.Second)
	defer stop()
	before := ChainRemainingBudgetMS(ctx)
	if before == 0 || before > 25 {
		t.Fatal(before)
	}
	<-ctx.Done()
	if ChainRemainingBudgetMS(ctx) != 0 {
		t.Fatal("expired budget reset")
	}
	for _, limit := range []time.Duration{5 * time.Millisecond, time.Second, 20 * time.Second} {
		ctx, stop := ChainBudget(context.Background(), limit, 12*time.Millisecond)
		remaining := ChainRemainingBudgetMS(ctx)
		stop()
		if remaining > 12 || time.Duration(remaining)*time.Millisecond > limit {
			t.Fatal(remaining)
		}
	}
}
func TestChainGrantResultWireNumbers(t *testing.T) {
	fields := (&controlpb.NodeReport{}).ProtoReflect().Descriptor().Fields()
	if fields.ByName("tunnel_grant_results").Number() != 285 {
		t.Fatal("report field")
	}
	grant := &controlpb.TunnelGrantApplyResult{EntryId: "a", RuleId: "r", Generation: 9, AppliedRevision: 4, ApplyError: "bounded"}
	b, err := proto.Marshal(&controlpb.NodeReport{AttemptedRevision: 4, AttemptedDigest: "d", RevocationsApplied: true, TunnelGrantResults: []*controlpb.TunnelGrantApplyResult{grant}})
	if err != nil {
		t.Fatal(err)
	}
	decoded := new(controlpb.NodeReport)
	if err = proto.Unmarshal(b, decoded); err != nil || !proto.Equal(grant, decoded.TunnelGrantResults[0]) {
		t.Fatal(err)
	}
	for i, name := range []string{"entry_id", "rule_id", "generation", "applied_revision", "apply_error"} {
		if grant.ProtoReflect().Descriptor().Fields().Get(i).Number() != protoreflect.FieldNumber(i+1) || string(grant.ProtoReflect().Descriptor().Fields().Get(i).Name()) != name {
			t.Fatal(name)
		}
	}
}

func TestChainFailureRejectsSiblingAndAcceptsIntermediateLocal(t *testing.T) {
	edges := []*controlpb.ChainEdgeAuthorization{
		{FromNodeId: "a", ToNodeId: "b1", AddressId: "public", HopIndex: 1, EndpointConfigDigest: "ab1"},
		{FromNodeId: "a", ToNodeId: "b2", AddressId: "public", HopIndex: 1, EndpointConfigDigest: "ab2"},
		{FromNodeId: "b1", ToNodeId: "c", AddressId: "public", HopIndex: 2, EndpointConfigDigest: "bc"},
		{FromNodeId: "b2", ToNodeId: "c", AddressId: "public", HopIndex: 2, EndpointConfigDigest: "bc"},
	}
	rule := &controlpb.ForwardRule{Id: "r", ChainId: "chain", AuthorizationGeneration: 7, ChainAuthorization: &controlpb.ChainAuthorization{Fingerprint: "fp", Edges: edges}}
	selected := &controlpb.TunnelEndpoint{EntryId: "a", NodeId: "b1", AddressId: "public", EndpointConfigDigest: "ab1"}
	f := &ChainFailure{FromNodeID: "b1", ToNodeID: "c", AddressID: "public", HopIndex: 2, RuleGeneration: 7, GrantGeneration: 7, ChainFingerprint: "fp", EndpointConfigDigest: "bc"}
	if !ValidateChainFailureFromNext(rule, selected, "downstream_next_hop", f) {
		t.Fatal("real suffix rejected")
	}
	f.FromNodeID = "b2"
	if ValidateChainFailureFromNext(rule, selected, "downstream_next_hop", f) {
		t.Fatal("sibling forged failure accepted")
	}
	scoped := proto.Clone(rule).(*controlpb.ForwardRule)
	scoped.HopIndex = 1
	scoped.ChainAuthorization.Edges = ChainEvidenceEdges(edges, "b1", 1, "a")
	if len(scoped.ChainAuthorization.Edges) != 2 || ValidateChainFailure(scoped, "downstream_next_hop", f) {
		t.Fatal("relay acquired sibling authority")
	}
	f.FromNodeID = "b1"
	f.ToNodeID = ""
	f.AddressID = ""
	f.EndpointConfigDigest = ""
	f.HopIndex = 1
	for _, class := range []string{"local_resource", "configuration_authorization"} {
		if !ValidateChainFailureFromNext(rule, selected, class, f) {
			t.Fatal("intermediate local error rejected", class)
		}
	}
	if ValidateChainFailureFromNext(rule, selected, "exit_target", f) {
		t.Fatal("intermediate pretended terminal")
	}
	f.FromNodeID = "a"
	f.HopIndex = 0
	if ValidateChainFailureFromNext(rule, selected, "local_resource", f) {
		t.Fatal("earlier-hop error accepted")
	}
}
