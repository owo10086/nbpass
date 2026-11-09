package panel

import (
	"context"
	"crypto/sha256"
	"fmt"
	"nbpass/internal/controlpb"
	"testing"
)

func TestExitHealthMigrationPreservesExistingMainProbe(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for version, migration := range migrations[:len(migrations)-1] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"INSERT INTO nodes(id,name,created_at) VALUES('entry','已有入口',0)",
		`INSERT INTO probe_realtime(node_id,process_instance,sequence,received_at,values_json) VALUES('entry','process',42,123,'{"cpu_percent":25}')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	var sequence, version int
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("迁移版本错误: %d %v", version, err)
	}
	var health, values string
	if err := p.db.QueryRow("SELECT sequence,values_json,exit_health_json FROM probe_realtime WHERE node_id='entry'").Scan(&sequence, &values, &health); err != nil {
		t.Fatal(err)
	}
	if sequence != 42 || values != `{"cpu_percent":25}` || health != "[]" {
		t.Fatalf("迁移破坏既有采样: %d %s %s", sequence, values, health)
	}
	if err := p.db.QueryRow("SELECT exit_failover_version FROM nodes WHERE id='entry'").Scan(&version); err != nil || version != 0 {
		t.Fatalf("旧节点错误声明新能力: %d %v", version, err)
	}
}

func TestExitCapabilityChangeInvalidatesRemoteConfirmations(t *testing.T) {
	p, entry, exit, _, s := tunnelAuthorizationProtocolFixture(t)
	rule := s.TunnelServer.Grants[0].Rule
	for _, version := range []uint32{0, 1} {
		if _, err := p.db.Exec("UPDATE nodes SET exit_failover_version=? WHERE id=?", 1-version, entry); err != nil {
			t.Fatal(err)
		}
		if _, err := p.db.Exec("INSERT OR REPLACE INTO tunnel_authorization_confirmations(exit_id,entry_id,rule_id,generation) VALUES(?,?,?,?)", exit, entry, rule.Id, rule.AuthorizationGeneration); err != nil {
			t.Fatal(err)
		}
		tx, err := p.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := p.admitExitFailover(context.Background(), tx, entry, version)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := p.db.QueryRow("SELECT count(*) FROM tunnel_authorization_confirmations WHERE entry_id=?", entry).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if !changed || count != 0 {
			t.Fatalf("能力切换复用了撤权前确认: changed=%v count=%d", changed, count)
		}
	}
}

// 独立上报混入健康与配置字段，也只能处理本类载荷。
func TestExitHealthIsNotConsumedByOtherReportCategories(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	defer p.Close()
	digest := sha256.Sum256([]byte("exit-health-token"))
	if _, err := p.db.Exec("INSERT INTO nodes(id,name,token_hash,created_at,last_seen_at,process_instance) VALUES('entry','入口',?,0,123,'process')", digest[:]); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), instance: "process"}
	p.controls["entry"] = session
	for _, kind := range []string{"events", "rotation", "self_check", "dns_watch", "traffic", "checker"} {
		t.Run(kind, func(t *testing.T) {
			report := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "process", AppliedRevision: 7, AttemptedRevision: 7, Probe: &controlpb.ProbeSample{Sequence: 1, ExitHealth: []*controlpb.ExitHealth{{ExitId: "unrelated", State: "healthy"}}}}
			switch kind {
			case "events":
				report.EventsOnly = true
			case "rotation":
				report.RotationOnly = true
				report.Rotation = &controlpb.RotationProgress{}
			case "self_check":
				report.SelfCheckOnly = true
			case "dns_watch":
				report.DnsWatchOnly = true
			case "traffic":
				report.TrafficOnly = true
			case "checker":
				report.CheckerOnly = true
			}
			if _, err := p.acceptReport(context.Background(), "entry", digest[:], session, report); err != nil {
				t.Fatal(err)
			}
			var last, applied, probes int
			if err := p.db.QueryRow("SELECT last_seen_at,applied_revision FROM nodes WHERE id='entry'").Scan(&last, &applied); err != nil {
				t.Fatal(err)
			}
			if err := p.db.QueryRow("SELECT count(*) FROM probe_realtime WHERE node_id='entry'").Scan(&probes); err != nil {
				t.Fatal(err)
			}
			if last != 123 || applied != 0 || probes != 0 {
				t.Fatalf("%s串写健康/配置/活性: %d %d %d", kind, probes, applied, last)
			}
		})
	}
}
