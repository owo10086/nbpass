package panel

import (
	"context"
	"fmt"
	"testing"

	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
)

func TestCompatibilityMigrationPreservesVersionThirtyAndAllowsNodeDeletion(t *testing.T) {
	dir := t.TempDir()
	deps := diskDependencies(dir)
	db, err := deps.Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:30] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO nodes(id,name,created_at,maintenance_until) VALUES('n','原有节点',0,123)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	var until, version int
	if err = p.db.QueryRow("SELECT maintenance_until FROM nodes WHERE id='n'").Scan(&until); err != nil || until != 123 {
		t.Fatal("兼容迁移破坏维护证据", err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal("迁移未升级至当前版本", version, err)
	}
	if _, err = p.db.Exec("INSERT INTO node_compatibility(node_id,report) VALUES('n','{}')"); err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec("DELETE FROM nodes WHERE id='n'"); err != nil {
		t.Fatal("能力记录阻止无引用节点删除", err)
	}
}

func TestLegacyCompatibilityOnlyGrantsExplicitFlags(t *testing.T) {
	c, err := resolveCompatibility(&controlpb.NodeReport{ProtocolVersion: 1, TunnelAuthorizationVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !c.supports("tunnel_authorization") || c.supports("upgrade") || c.supports("exit_failover") || c.supports("ix_address_dialing") || c.supports("ix_member_identity") || c.supports("outbound_ipv4") || c.supports("forward_chains") {
		t.Fatalf("无声明节点继承了未明确支持的能力: %v", c.Capabilities)
	}
}

func TestCompatibilityUsesDeclarationsAndRangesInsteadOfSoftwareVersion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		caps      []string
		tunnelMin uint32
		want      bool
	}{
		{"explicit", []string{"tunnel_authorization"}, 1, true},
		{"missing", nil, 1, false},
		{"unknown", []string{"future", "ix_addresses"}, 1, false},
		{"incompatible_tunnel", []string{"tunnel_authorization"}, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &controlpb.NodeReport{ProtocolVersion: 2, SoftwareVersion: "same-version", TunnelAuthorizationVersion: 1, Compatibility: compatibility.Current()}
			r.Compatibility.Capabilities = tc.caps
			r.Compatibility.TunnelMin, r.Compatibility.TunnelMax = tc.tunnelMin, tc.tunnelMin
			c, err := resolveCompatibility(r)
			if err != nil {
				t.Fatal(err)
			}
			if c.supports("tunnel_authorization") != tc.want {
				t.Fatalf("范围或声明未限制能力: %v", c.Capabilities)
			}
		})
	}
}
