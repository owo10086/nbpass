package forwarding

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

func TestUpgradeBusinessCheckDoesNotTrustCachedTunnelSuccess(t *testing.T) {
	deps := Dependencies{}
	snapshot := cachedTunnelFixture(t, &deps)
	exitConfig := proto.Clone(snapshot).(*controlpb.ConfigSnapshot)
	exitConfig.Rules = nil
	exitConfig.ExitGroups = nil
	exitConfig.Digest = SnapshotDigest(exitConfig)
	exit := NewEngine(deps)
	if err := exit.Apply(exitConfig); err != nil {
		t.Fatal(err)
	}
	snapshot.TunnelServer = nil
	snapshot.Digest = SnapshotDigest(snapshot)
	entry := NewEngine(deps)
	if err := entry.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	state := entry.State()
	entry.Close()
	exit.Close()
	restored := NewEngine(deps)
	defer restored.Close()
	if err := restored.Restore(state); err != nil {
		t.Fatal(err)
	}
	if restored.ApplyError() != "" {
		t.Fatalf("未保留缓存成功用于离线转发: %s", restored.ApplyError())
	}
	if err := restored.UpgradeReady(context.Background()); err == nil {
		t.Fatal("未验证本进程隧道握手就报告升级业务就绪")
	}
}
