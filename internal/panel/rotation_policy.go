package panel

import (
	"context"
	"math"
	"time"
)

// 迁移 21：额度独立于可清理事件，旧动作保守重建尝试历史。
const rotationPolicyMigration = `ALTER TABLE rotation_settings ADD COLUMN automatic_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rotation_actions ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual';
CREATE TABLE rotation_quota (
 action_id TEXT PRIMARY KEY REFERENCES rotation_actions(id),
 node_id TEXT NOT NULL REFERENCES nodes(id), attempted_at INTEGER NOT NULL
);
CREATE INDEX rotation_quota_node_time ON rotation_quota(node_id,attempted_at);
INSERT INTO rotation_quota SELECT id,node_id,created_at FROM rotation_actions WHERE state!='cancelled';
CREATE TABLE rotation_policy (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id),
 no_improvement INTEGER NOT NULL DEFAULT 0, paused INTEGER NOT NULL DEFAULT 0, last_normal_at INTEGER NOT NULL DEFAULT 0, last_reset_at INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE rotation_actions ADD COLUMN permit_sent INTEGER NOT NULL DEFAULT 0;
UPDATE rotation_actions SET permit_sent=1;
ALTER TABLE rotation_actions ADD COLUMN recheck_config BLOB NOT NULL DEFAULT X'';
ALTER TABLE rotation_actions ADD COLUMN recheck_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rotation_actions ADD COLUMN recheck_status TEXT NOT NULL DEFAULT '';
ALTER TABLE rotation_actions ADD COLUMN recheck_rounds BLOB NOT NULL DEFAULT '[]';
ALTER TABLE entry_groups ADD COLUMN blocked_policy TEXT NOT NULL DEFAULT 'disabled';`

type rotationQuota struct {
	UsedHour          int   `json:"used_hour"`
	UsedDay           int   `json:"used_day"`
	CooldownSeconds   int64 `json:"cooldown_seconds"`
	RetryAfterSeconds int64 `json:"retry_after_seconds"`
}

func (p *Panel) rotationQuota(ctx context.Context, id string) (rotationQuota, error) {
	var q rotationQuota
	now := p.deps.Clock.Now()
	rows, err := p.db.QueryContext(ctx, `SELECT MAX(attempted_at) AS at FROM (SELECT action_id,node_id,attempted_at FROM rotation_quota UNION ALL SELECT action_id,node_id,attempted_at FROM recovery_rotation_attempts WHERE state!='cancelled') WHERE node_id=? AND attempted_at>? GROUP BY action_id ORDER BY at`, id, now.Add(-24*time.Hour).UnixNano())
	if err != nil {
		return q, err
	}
	defer rows.Close()
	var times []time.Time
	for rows.Next() {
		var at int64
		if err = rows.Scan(&at); err != nil {
			return q, err
		}
		times = append(times, time.Unix(0, at))
	}
	if err = rows.Err(); err != nil {
		return q, err
	}
	seconds := func(at time.Time) int64 { return max(0, int64(math.Ceil(at.Sub(now).Seconds()))) }
	q.UsedDay = len(times)
	for _, at := range times {
		if at.After(now.Add(-time.Hour)) {
			q.UsedHour++
			q.RetryAfterSeconds = max(q.RetryAfterSeconds, seconds(at.Add(time.Hour)))
		}
		q.CooldownSeconds = max(q.CooldownSeconds, seconds(at.Add(30*time.Minute)))
	}
	q.RetryAfterSeconds = max(q.RetryAfterSeconds, q.CooldownSeconds)
	if len(times) >= 3 {
		q.RetryAfterSeconds = max(q.RetryAfterSeconds, seconds(times[len(times)-3].Add(24*time.Hour)))
	}
	return q, nil
}
