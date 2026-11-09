package panel

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"nbpass/internal/controlpb"
	"nbpass/internal/nodeupdate"
)

func TestUpgradeReportsCannotAdvanceOtherControlState(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	ctx := context.Background()
	now := p.deps.Clock.Now()
	digest := sha256.Sum256([]byte("token"))
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,token_hash,created_at,last_seen_at,desired_revision,applied_revision,process_instance) VALUES('n','节点',?,?,?,5,4,'current')`, digest[:], now.UnixNano(), int64(123))
	if err != nil {
		t.Fatal(err)
	}
	m := nodeupdate.Manifest{TaskID: "task", Version: "0.1.0", Platform: nodeupdate.Platform, Size: 3, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("new")))}
	initial, _ := json.Marshal(nodeupdate.Progress{Manifest: m, State: "queued"})
	raw, _ := json.Marshal(m)
	if _, err = p.db.Exec("INSERT INTO node_upgrades(id,node_id,manifest,progress,initial_instance,created_at) VALUES('task','n',?,?,'current',?)", raw, initial, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{instance: "current", done: make(chan struct{})}
	p.controlMu.Lock()
	p.controls["n"] = session
	p.controlMu.Unlock()
	progress, _ := json.Marshal(nodeupdate.Progress{Manifest: m, Sequence: 1, State: "verifying"})
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current", UpgradeOnly: true, UpgradeProgress: progress, SoftwareVersion: "poison", UpgradeCapable: true, AppliedRevision: 5, PublicIpv4: "8.8.8.8", Probe: &controlpb.ProbeSample{Sequence: 1}, Events: []*controlpb.Event{{EventId: "poison"}}, Recovery: &controlpb.RecoveryReport{HighWater: 999}}
	ack, err := p.acceptReport(ctx, "n", digest[:], session, report)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.UpgradeOnly || ack.Snapshot != nil || len(ack.AcknowledgedEventIds) != 0 {
		t.Fatal("升级确认消费其他状态")
	}
	var last, applied int64
	var version string
	if err = p.db.QueryRow("SELECT last_seen_at,applied_revision,software_version FROM nodes WHERE id='n'").Scan(&last, &applied, &version); err != nil {
		t.Fatal(err)
	}
	if last != 123 || applied != 4 || version != "" || session.upgradeCapable {
		t.Fatal("升级报告推进了其他控制状态")
	}
	var count int
	for _, query := range []string{"SELECT count(*) FROM node_regions WHERE node_id='n'", "SELECT count(*) FROM events WHERE event_id='poison'", "SELECT count(*) FROM probe_realtime WHERE node_id='n'"} {
		if err = p.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("跨报告消费: %s count=%d err=%v", query, count, err)
		}
	}
	// Conversely, heartbeat payloads must not consume an upgrade-progress field.
	report.UpgradeOnly = false
	report.UpgradeProgress, _ = json.Marshal(nodeupdate.Progress{Manifest: m, Sequence: 99, State: "failed"})
	report.Probe = nil
	report.Events = nil
	report.Recovery = nil
	report.PublicIpv4 = ""
	report.AppliedRevision = 0
	if _, err = p.acceptReport(ctx, "n", digest[:], session, report); err != nil {
		t.Fatal(err)
	}
	saved, err := p.latestUpgrade(ctx, "n")
	if err != nil || saved.State != "verifying" {
		t.Fatal("心跳消费了混入的升级进度")
	}
}
func TestUpgradeMigrationPreservesVersionTwentyEight(t *testing.T) {
	dir := t.TempDir()
	deps := diskDependencies(dir)
	db, err := deps.Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, migration := range migrations[:28] {
		if _, err = db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec("INSERT INTO nodes(id,name,created_at,maintenance_until) VALUES('n','保留节点',0,123)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	p := openDiskPanel(t, dir)
	var until int64
	if err = p.db.QueryRow("SELECT maintenance_until FROM nodes WHERE id='n'").Scan(&until); err != nil || until != 123 {
		t.Fatal("升级迁移破坏原维护状态")
	}
	if len(migrations) != 29 {
		t.Fatalf("迁移号变化: %d", len(migrations))
	}
}
