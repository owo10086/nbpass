package panel

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"nbpass/internal/eventlog"
)

const offlineWindow = 90 * time.Second

const offlineMigration = `CREATE TABLE offline_incidents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id TEXT NOT NULL REFERENCES nodes(id),
	offline_at INTEGER NOT NULL,
	recovered_at INTEGER,
	notification_stage INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX offline_incidents_active ON offline_incidents(node_id) WHERE recovered_at IS NULL;`

type presence struct {
	status, label string
	connected     bool
	offlineAt     time.Time
}

func (p *Panel) presence(id string, last, disconnected, maintenance sql.NullInt64, now time.Time) presence {
	result := presence{status: "online", label: "在线", connected: p.controls[id] != nil && last.Valid && now.Sub(time.Unix(0, last.Int64)) < offlineWindow}
	base := last
	if disconnected.Valid {
		base = disconnected
	}
	if !base.Valid {
		result.status, result.label = "pending", "待接入"
	} else {
		if maintenance.Valid && maintenance.Int64 > base.Int64 {
			base = maintenance
		}
		if !result.connected && now.After(time.Unix(0, base.Int64).Add(offlineWindow)) {
			if now.Sub(p.startedAt) < offlineWindow {
				result.status, result.label = "pending", "待确认"
			} else {
				result.status, result.label = "offline", "离线"
				result.offlineAt = time.Unix(0, base.Int64).Add(offlineWindow)
				if grace := p.startedAt.Add(offlineWindow); grace.After(result.offlineAt) {
					result.offlineAt = grace
				}
			}
		}
	}
	if maintenance.Valid && now.Before(time.Unix(0, maintenance.Int64)) {
		result.status, result.label = "maintenance", "维护中"
	}
	return result
}

// 调用方持 controlMu；事件与事故状态在同一事务落盘，重复扫描不会重复记账。
func (p *Panel) observeOffline(ctx context.Context, id string, now time.Time) error {
	var last, disconnected, maintenance sql.NullInt64
	if err := p.db.QueryRowContext(ctx, "SELECT last_seen_at, disconnected_at, maintenance_until FROM nodes WHERE id=?", id).Scan(&last, &disconnected, &maintenance); err != nil {
		return err
	}
	state := p.presence(id, last, disconnected, maintenance, now)
	if state.status != "offline" {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO offline_incidents(node_id,offline_at) VALUES(?,?)", id, state.offlineAt.UnixNano())
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count > 0 {
		incident, err := result.LastInsertId()
		if err != nil {
			return err
		}
		err = insertEvent(ctx, tx, id, "panel", eventlog.Event{ID: offlineEventID(incident, "offline"), Type: "node_offline", OccurredAt: state.offlineAt, Message: "节点控制连接中断超过 90 秒，判定离线"}, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (p *Panel) observeAllOffline(ctx context.Context) error {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	rows, err := p.db.QueryContext(ctx, "SELECT id FROM nodes WHERE last_seen_at IS NOT NULL")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := p.observeOffline(ctx, id, p.deps.Clock.Now()); err != nil {
			return err
		}
	}
	return nil
}

func offlineEventID(incident int64, transition string) string {
	return "offline-incident:" + strconv.FormatInt(incident, 10) + ":" + transition
}

func offlineTelegramID(incident int64) string { return "offline:" + strconv.FormatInt(incident, 10) }

func offlineSummary(name string, at int64) string {
	return fmt.Sprintf("节点「%s」离线\n离线时间：%s", name, time.Unix(0, at).UTC().Format(time.RFC3339))
}

// 只有活性报告能恢复事故；探针、事件及配置确认都不能调用这里。
func (p *Panel) recordHeartbeat(ctx context.Context, id, version string, digest []byte) error {
	now := p.deps.Clock.Now()
	if err := p.observeOffline(ctx, id, now); err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE nodes SET last_seen_at=?,disconnected_at=NULL,software_version=? WHERE id=? AND token_hash=?", now.UnixNano(), version, id, digest); err != nil {
		return err
	}
	var incident int64
	err = tx.QueryRowContext(ctx, "UPDATE offline_incidents SET recovered_at=? WHERE node_id=? AND recovered_at IS NULL RETURNING id", now.UnixNano(), id).Scan(&incident)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if err := insertEvent(ctx, tx, id, "panel", eventlog.Event{ID: offlineEventID(incident, "recovered"), Type: "node_recovered", OccurredAt: now, Message: "节点已恢复上线"}, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.wakeOffline()
	return nil
}

func (p *Panel) startOffline(ctx context.Context) {
	p.offlineWake = make(chan struct{}, 1)
	first := p.deps.Clock.NewTimer(time.Second)
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		timer := first
		defer func() { timer.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C():
				timer = p.deps.Clock.NewTimer(time.Second)
			case <-p.offlineWake:
			}
			_ = p.observeAllOffline(ctx)
			_ = p.checkOfflineEntries(ctx)
			_ = p.flushOfflineEntryNotifications(ctx)
			_ = p.queueOfflineNotifications(ctx)
		}
	}()
}

func (p *Panel) wakeOffline() {
	select {
	case p.offlineWake <- struct{}{}:
	default:
	}
}

// 通知写入独立于活性和事件事务；队列故障后仍可从持久化事故重新构造摘要。
func (p *Panel) queueOfflineNotifications(ctx context.Context) error {
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT i.id,i.node_id,n.name,i.offline_at,i.recovered_at FROM offline_incidents i JOIN nodes n ON n.id=i.node_id
		WHERE (i.recovered_at IS NOT NULL AND i.notification_stage<2)
		OR (i.recovered_at IS NULL AND i.notification_stage<1 AND i.offline_at<=? AND ?>=? AND (n.maintenance_until IS NULL OR n.maintenance_until<=?))`,
		now.Add(-2*time.Minute).UnixNano(), now.UnixNano(), p.startedAt.Add(offlineWindow).UnixNano(), now.Add(-offlineWindow-2*time.Minute).UnixNano())
	if err != nil {
		return err
	}
	type notice struct {
		id, offlineAt int64
		nodeID, name  string
		recovered     sql.NullInt64
	}
	var notices []notice
	for rows.Next() {
		var n notice
		if err := rows.Scan(&n.id, &n.nodeID, &n.name, &n.offlineAt, &n.recovered); err != nil {
			rows.Close()
			return err
		}
		notices = append(notices, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notices {
		parts := []string{offlineSummary(n.name, n.offlineAt)}
		stage := 1
		if n.recovered.Valid {
			stage = 2
			parts = append(parts, fmt.Sprintf("节点「%s」恢复上线\n恢复时间：%s", n.name, time.Unix(0, n.recovered.Int64).UTC().Format(time.RFC3339)))
		}
		if err := queueTelegram(ctx, tx, offlineTelegramID(n.id), n.nodeID, parts, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE offline_incidents SET notification_stage=? WHERE id=?", stage, n.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(notices) > 0 {
		p.wakeTelegram()
	}
	return nil
}
