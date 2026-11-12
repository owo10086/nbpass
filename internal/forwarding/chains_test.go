package forwarding

import (
	"reflect"
	"testing"
)

func TestExpandForwardChainAllMembersAndOrder(t *testing.T) {
	groups := map[string]ChainGroup{
		"g1":    {ID: "g1", Name: "一", Members: []ChainMember{{NodeID: "a", Address: "10.0.0.1"}, {NodeID: "b", Address: "10.0.0.1"}}},
		"g2":    {ID: "g2", Name: "二", Members: []ChainMember{{NodeID: "c"}}},
		"g3":    {ID: "g3", Members: []ChainMember{{NodeID: "b"}}},
		"empty": {ID: "empty"},
	}
	hops, err := ExpandForwardChain(ForwardChain{ExitGroupIDs: []string{"g2", "g1"}}, groups)
	if err != nil || len(hops) != 2 || hops[0].Position != 1 || hops[0].ExitGroupID != "g2" || !reflect.DeepEqual(hops[1].Members, groups["g1"].Members) {
		t.Fatal(hops, err)
	}
	for _, ids := range [][]string{{}, {"g1"}, {"missing", "g2"}, {"empty", "g2"}, {"g1", "g1"}, {"g1", "g3"}} {
		if _, err := ExpandForwardChain(ForwardChain{ExitGroupIDs: ids}, groups); err == nil {
			t.Fatal("路径未拒绝", ids)
		}
	}
	if err := ValidatePathNodes([]string{"b"}, hops); err == nil {
		t.Fatal("入口与备用重复未拒绝")
	}
	if err := ValidatePathNodes([]string{"d", "e"}, hops); err != nil {
		t.Fatal(err)
	}
}
