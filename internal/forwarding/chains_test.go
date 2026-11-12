package forwarding

import (
	"net/netip"
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

func TestChainTargetDialerLoops(t *testing.T) {
	nodes := []ValidationNode{{ID: "a", ObservedIPv4: "192.0.2.1"}, {ID: "b", ObservedIPv4: "192.0.2.2"}, {ID: "c", ObservedIPv4: "192.0.2.3"}}
	for i := range nodes {
		nodes[i].NodeListenerPorts = NodeListenerPorts{TunnelListenPort: 18443, CheckPort: 18080}
	}
	for _, host := range []string{"127.0.0.1", "localhost", "target.test", "192.0.2.3"} {
		t.Run(host, func(t *testing.T) {
			rules := []Rule{{ID: "chain", Name: "链入口", EntryNodeID: "a", Kind: Tunnel, ListenPort: 20001, TargetAddress: host, TargetPort: 20002, AllowPrivate: true, Enabled: true}, {ID: "back", Name: "末跳本机监听", EntryNodeID: "c", Kind: Direct, ListenPort: 20002, TargetAddress: "192.0.2.1", TargetPort: 20001, AllowPrivate: true, Enabled: true}}
			targets := map[string][]netip.Addr{"target.test": {netip.MustParseAddr("127.0.0.1")}}
			failures := ConfigurationErrors(nodes, rules, nil, targets, map[string][]string{"chain": {"b", "c"}})
			if len(failures) != 1 || failures[0].Code != "forwarding_loop" {
				t.Fatalf("备用末跳环路未发现: %+v", failures)
			}
			rules = rules[:1]
			rules[0].TargetPort = 20001
			if got := ConfigurationErrors(nodes, rules, nil, targets, map[string][]string{"chain": {"b", "c"}}); len(got) > 0 {
				t.Fatalf("末跳无业务监听却报自环: %+v", got)
			}
		})
	}
}
