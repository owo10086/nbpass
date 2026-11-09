package panel

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nbpass/internal/platform"
)

type retentionClock struct {
	platform.RealClock
	now time.Time
}

func (c retentionClock) Now() time.Time { return c.now }

// 包内只验证物理属性：每条 DELETE 有界、边界分钟保留、实时去重不被清除。
func TestRetentionStorageBatchBoundariesAndRealtimeCursor(t *testing.T) {
	ctx := context.Background()
	deps := diskDependencies(t.TempDir())
	now := time.Date(2030, 6, 1, 12, 0, 30, 0, time.UTC)
	deps.Clock = retentionClock{now: now}
	db, err := deps.Persistence.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &Panel{db: db, deps: deps}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','节点',0);
 INSERT INTO probe_realtime(node_id,process_instance,sequence,received_at,values_json) VALUES('n','instance',99,0,'{}');`); err != nil {
		t.Fatal(err)
	}
	for i := range 600 {
		if _, err := tx.Exec(`INSERT INTO events(node_id,source,event_id,type,occurred_at,received_at,config_revision,message) VALUES('n','panel',?,'fixture',?,0,0,'历史')`, fmt.Sprint(i), now.Add(-91*24*time.Hour).UnixNano()); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO probe_minutes VALUES('n',?,1,'{}')`, now.Add(-25*time.Hour).Truncate(time.Minute).Add(-time.Duration(i)*time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	more, err := p.cleanHistoryBatch(ctx)
	if err != nil || !more {
		t.Fatalf("未报告剩余批次: more=%v err=%v", more, err)
	}
	for _, table := range []string{"events", "probe_minutes"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 344 {
			t.Fatalf("%s 一批应只删除 256 条，剩余=%d err=%v", table, count, err)
		}
	}
	// 积压中延长保留期，后续批次必须立即使用已提交的新值。
	if _, err := db.Exec("UPDATE retention_settings SET event_days=3650,probe_hours=48 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if more, err := p.cleanHistoryBatch(ctx); err != nil || more {
		t.Fatalf("延长设置后仍继续删除: more=%v err=%v", more, err)
	}
	for _, table := range []string{"events", "probe_minutes"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 344 {
			t.Fatalf("延长保留期后 %s 被继续清理: %d %v", table, count, err)
		}
	}
	if _, err := db.Exec("UPDATE retention_settings SET event_days=90,probe_hours=24 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	for more {
		more, err = p.cleanHistoryBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []int64{now.Add(-90 * 24 * time.Hour).UnixNano(), now.Add(-90*24*time.Hour).UnixNano() - 1} {
		if _, err := db.Exec(`INSERT INTO events(node_id,source,event_id,type,occurred_at,received_at,config_revision,message) VALUES('n','panel',?,'boundary',?,0,0,'边界')`, fmt.Sprint(at), at); err != nil {
			t.Fatal(err)
		}
	}
	boundaryMinute := now.Add(-24 * time.Hour).Truncate(time.Minute).Unix()
	for _, at := range []int64{boundaryMinute, boundaryMinute - 60} {
		if _, err := db.Exec(`INSERT INTO probe_minutes VALUES('n',?,1,'{}')`, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.cleanHistoryBatch(ctx); err != nil {
		t.Fatal(err)
	}
	var eventAt, minute, sequence int64
	if err := db.QueryRow("SELECT occurred_at FROM events").Scan(&eventAt); err != nil || eventAt != now.Add(-90*24*time.Hour).UnixNano() {
		t.Fatalf("恰好保留期边界的事件被删除: %d %v", eventAt, err)
	}
	if err := db.QueryRow("SELECT minute FROM probe_minutes").Scan(&minute); err != nil || minute != boundaryMinute {
		t.Fatalf("跨越边界的分钟桶被删除: %d %v", minute, err)
	}
	if err := db.QueryRow("SELECT sequence FROM probe_realtime WHERE node_id='n'").Scan(&sequence); err != nil || sequence != 99 {
		t.Fatalf("实时去重高水位被清理: %d %v", sequence, err)
	}
}
