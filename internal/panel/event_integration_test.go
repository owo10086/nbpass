package panel

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
)

// 事件或探针独立报告可确认事件，但不能顺带推进应用版本、活性或公网 IP。
func TestIndependentReportsAcknowledgeEventsWithoutAdvancingOtherState(t *testing.T) {
	for _, kind := range []string{"events", "probe"} {
		t.Run(kind, func(t *testing.T) {
			p := openDiskPanel(t, t.TempDir())
			digest := sha256.Sum256([]byte("test-token"))
			_, err := p.db.Exec("INSERT INTO nodes(id, name, token_hash, created_at, last_seen_at, desired_revision, process_instance) VALUES('existing', '已有节点', ?, 0, 123, 7, 'process')", digest[:])
			if err != nil {
				t.Fatal(err)
			}
			session := &controlSession{done: make(chan struct{}), instance: "process"}
			p.controls["existing"] = session
			report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "process", AppliedRevision: 7, PublicIpv4: "223.5.5.5", EventsOnly: kind == "events", ProbeOnly: kind == "probe", Events: []*controlpb.Event{(eventlog.Event{ID: "independent-event", Type: "sync_failed", OccurredAt: time.Now(), Message: "安全摘要"}).Proto()}}
			response, err := p.acceptReport(context.Background(), "existing", digest[:], session, report)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.AcknowledgedEventIds) != 1 || response.AcknowledgedEventIds[0] != "independent-event" {
				t.Fatalf("独立报告未确认事件: %v", response)
			}
			var lastSeen, applied, regions, events int
			if err := p.db.QueryRow("SELECT last_seen_at, applied_revision FROM nodes WHERE id = 'existing'").Scan(&lastSeen, &applied); err != nil {
				t.Fatal(err)
			}
			if lastSeen != 123 || applied != 0 {
				t.Fatalf("独立报告推进了其他状态: last_seen=%d applied=%d", lastSeen, applied)
			}
			if err := p.db.QueryRow("SELECT count(*) FROM node_regions WHERE node_id = 'existing'").Scan(&regions); err != nil || regions != 0 {
				t.Fatalf("独立报告改写了地区: %d %v", regions, err)
			}
			if err := p.db.QueryRow("SELECT count(*) FROM events WHERE event_id = 'independent-event'").Scan(&events); err != nil || events != 1 {
				t.Fatalf("事件未持久化: %d %v", events, err)
			}
		})
	}
}

// 从 main 的版本 5 升级，既有探针样本和节点保留，事件表可正常写入。
func TestEventMigrationPreservesVersionFiveProbeData(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, migration := range migrations[:5] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"INSERT INTO nodes(id, name, created_at) VALUES('existing', '已有节点', 0)",
		"INSERT INTO probe_realtime VALUES('existing', 'process', 42, 123, '{\"cpu_percent\":25}')",
		"INSERT INTO probe_minutes VALUES('existing', 60, 2, '{}')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	if err := p.RecordEvent(context.Background(), "existing", eventlog.Event{ID: "upgrade", Type: "sync_failed", Message: "升级后事件"}); err != nil {
		t.Fatalf("升级后事件写入失败: %v", err)
	}
	var sequence, samples int
	if err := p.db.QueryRow("SELECT sequence FROM probe_realtime WHERE node_id = 'existing'").Scan(&sequence); err != nil || sequence != 42 {
		t.Fatalf("实时样本丢失: %d %v", sequence, err)
	}
	if err := p.db.QueryRow("SELECT samples FROM probe_minutes WHERE node_id = 'existing'").Scan(&samples); err != nil || samples != 2 {
		t.Fatalf("分钟样本丢失: %d %v", samples, err)
	}
}
