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

// 配置确认只更新配置结果，不能刷新探针/IP/10 秒活性。
func TestConfigAcknowledgementDoesNotAdvanceOtherReportState(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	digest := sha256.Sum256([]byte("config-token"))
	if _, err := p.db.Exec("INSERT INTO nodes(id,name,token_hash,created_at,last_seen_at,desired_revision,process_instance) VALUES('config','入口',?,0,123,7,'process')", digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES('config',X'','expected')"); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), instance: "process"}
	p.controls["config"] = session
	report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "process", AttemptedRevision: 7, AppliedRevision: 7, AppliedDigest: "expected", PublicIpv4: "223.5.5.5", Probe: &controlpb.ProbeSample{Sequence: 1, Values: map[string]float64{"cpu_percent": 25}}}
	if _, err := p.acceptReport(context.Background(), "config", digest[:], session, report); err != nil {
		t.Fatal(err)
	}
	var lastSeen, applied, probes, regions int
	if err := p.db.QueryRow("SELECT last_seen_at,applied_revision FROM nodes WHERE id='config'").Scan(&lastSeen, &applied); err != nil {
		t.Fatal(err)
	}
	if lastSeen != 123 || applied != 7 {
		t.Fatalf("配置确认串改活性或未推进配置: last_seen=%d applied=%d", lastSeen, applied)
	}
	if err := p.db.QueryRow("SELECT count(*) FROM probe_realtime WHERE node_id='config'").Scan(&probes); err != nil || probes != 0 {
		t.Fatalf("配置确认串改探针: %d %v", probes, err)
	}
	if err := p.db.QueryRow("SELECT count(*) FROM node_regions WHERE node_id='config'").Scan(&regions); err != nil || regions != 0 {
		t.Fatalf("配置确认串改地区: %d %v", regions, err)
	}
}

func TestDirectMigrationPreservesVersionSixEventsAndCredentials(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, migration := range migrations[:6] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	digest := sha256.Sum256([]byte("preserved-token"))
	if _, err := db.Exec("INSERT INTO nodes(id,name,token_hash,created_at) VALUES('existing','已有节点',?,0)", digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO events(node_id,source,event_id,type,occurred_at,received_at,config_revision,message) VALUES('existing','node','kept','sync_failed',1,2,3,'历史事件')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var message string
	var revision, count int
	if err := p.db.QueryRow("SELECT message,config_revision FROM events WHERE event_id='kept'").Scan(&message, &revision); err != nil || message != "历史事件" || revision != 3 {
		t.Fatalf("事件数据丢失: %q %d %v", message, revision, err)
	}
	if err := p.db.QueryRow("SELECT count(*) FROM nodes WHERE token_hash=?", digest[:]).Scan(&count); err != nil || count != 1 {
		t.Fatalf("节点凭据丢失: %d %v", count, err)
	}
	for _, query := range []string{
		"INSERT INTO entry_groups(id,name) VALUES('g','入口组')",
		"INSERT INTO entry_group_members VALUES('g','existing',0)",
		"INSERT INTO forward_rules VALUES('r','规则','g','direct',12345,'8.8.8.8','ipv4',443,0,1)",
		"INSERT INTO node_snapshots VALUES('existing',X'','digest')",
		"UPDATE nodes SET applied_digest='digest',attempted_revision=1,apply_error='' WHERE id='existing'",
	} {
		if _, err := p.db.Exec(query); err != nil {
			t.Fatalf("直连迁移不可用: %v", err)
		}
	}
}
