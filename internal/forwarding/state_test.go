package forwarding

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"

	"nbpass/internal/controlpb"
)

// 缓存高水位不能随着整份已应用版本回退，否则旧完整快照可复活撤销规则。
func TestRestoredHighWaterRejectsOldSnapshotAfterPartialApply(t *testing.T) {
	deps := Dependencies{}
	port := reserveTestPort(t, &deps)
	occupied, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var disk []byte
	deps.Persist = func(s State) error { var err error; disk, err = json.Marshal(s); return err }
	e := NewEngine(deps)
	defer e.Close()
	old := &controlpb.ConfigSnapshot{Revision: 1, Rules: []*controlpb.ForwardRule{{Id: "old", EntryGroupId: "g", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: port, TargetAddress: "8.8.8.8", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: 443}}}
	old.Digest = SnapshotDigest(old)
	if err := e.Apply(old); err != nil {
		t.Fatal(err)
	}
	next := &controlpb.ConfigSnapshot{Revision: 2, Rules: []*controlpb.ForwardRule{{Id: "failed", EntryGroupId: "g", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: uint32(occupied.Addr().(*net.TCPAddr).Port), TargetAddress: "8.8.8.8", TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: 443}}}
	next.Digest = SnapshotDigest(next)
	if err := e.Apply(next); err == nil {
		t.Fatal("监听冲突未失败")
	}
	e.Close()
	var state State
	if err := json.Unmarshal(disk, &state); err != nil {
		t.Fatal(err)
	}
	rebuilt := NewEngine(Dependencies{})
	defer rebuilt.Close()
	if err := rebuilt.Restore(state); err != nil {
		t.Fatal(err)
	}
	if revision, _ := rebuilt.Applied(); revision != 1 {
		t.Fatal("重建错误推进整份版本")
	}
	if err := rebuilt.Apply(old); err == nil {
		t.Fatal("高水位丢失，接受了撤销之前的快照")
	}
	different := &controlpb.ConfigSnapshot{Revision: 2}
	different.Digest = SnapshotDigest(different)
	if err := rebuilt.Apply(different); err == nil {
		t.Fatal("高水位摘要丢失，接受同版本不同内容")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		t.Fatalf("已撤销的旧规则恢复监听: %v", err)
	}
	listener.Close()
}
