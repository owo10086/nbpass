package compatibility

import "testing"

func TestCurrentStateWindowIsOneWayUpgrade(t *testing.T) {
	c := Current()
	if !Contains(c.StateMin, c.StateMax, 1) || !Contains(c.StateMin, c.StateMax, 2) || c.StateWrite != 2 {
		t.Fatalf("须读 1/2、写 2: %v", c)
	}
	if !Supports(c, IXAddressDialing) {
		t.Fatal("未宣告已经实现的地址拨号能力")
	}
	old := Current()
	old.StateMin, old.StateMax, old.StateWrite = 1, 1, 1
	if CompatibleState(old) {
		t.Fatal("允许了不能读取当前格式的在线切换")
	}
	if !Supports(c, IXMemberIdentity) {
		t.Fatal("未宣告成员地址隔离能力")
	}
	for _, cap := range []string{"outbound_ipv4", "forward_chains", "ix_addresses"} {
		if Supports(c, cap) {
			t.Fatalf("提前宣告未实现能力 %s", cap)
		}
	}
}
