package node

import (
	"strings"
	"testing"

	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

func TestIXMemberPublicVersionCannotBorrowDedicatedCacheIdentity(t *testing.T) {
	// A restored dedicated-only state is not evidence that the public address was applied.
	e := forwarding.NewEngine(forwarding.Dependencies{})
	defer e.Close()
	s := &controlpb.ConfigSnapshot{ExitGroups: []*controlpb.ExitGroup{{Exits: []*controlpb.TunnelEndpoint{{NodeId: "b", AddressId: "public", AddressVersion: "new-public"}, {NodeId: "b", AddressId: "ix"}}}}}
	if exitAddressVersionsApplied(e, s) {
		t.Fatal("没有缓存却确认公网版本")
	}
	// Dedicated-only configurations never wait for a public-IP transition.
	s.ExitGroups[0].Exits = s.ExitGroups[0].Exits[1:]
	s.ExitGroups[0].Exits[0].AddressVersion = strings.Repeat("x", 43)
	if !exitAddressVersionsApplied(e, s) {
		t.Fatal("专线被误当成公网换址参与者")
	}
}
