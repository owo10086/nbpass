package panel

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"nbpass/internal/eventlog"
)

// 事件与通知从持久决策重建。队列写入失败不能回滚切换，下一轮继续投递。
func (p *Panel) flushBlockedEntryNotifications(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT d.id,d.group_id,d.node_id,d.desired_version,d.outcome,d.action_id,d.created_at,COALESCE(a.node_id,''),a.written_at
 FROM entry_blocked_decisions d LEFT JOIN entry_actions a ON a.id=d.action_id
 WHERE (d.outcome='no_candidate' AND d.waiting_notified=0)
 OR (d.outcome='submitted' AND d.switch_notified=0 AND a.stage=? AND a.written_at IS NOT NULL) ORDER BY d.created_at`, EntryWritten)
	if err != nil {
		return err
	}
	type notice struct {
		id, group, node, outcome, action, target string
		version, created                         int64
		written                                  sql.NullInt64
	}
	var notices []notice
	for rows.Next() {
		var n notice
		if err = rows.Scan(&n.id, &n.group, &n.node, &n.version, &n.outcome, &n.action, &n.created, &n.target, &n.written); err != nil {
			break
		}
		notices = append(notices, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notices {
		kind, column, at := "entry_blocked_waiting", "waiting_notified", n.created
		message := "活动入口被墙，但没有就绪候选；保留最后的解析，不发起换 IP"
		if n.outcome == "submitted" {
			kind, column, at = "entry_blocked_switch", "switch_notified", n.written.Int64
			message = fmt.Sprintf("活动入口被墙，入口切换已写入 DNSPod：%s → %s（动作 %s）", n.node, n.target, n.action)
		}
		message = fmt.Sprintf("入口组 %s 期望版本 %d（策略②仅入口切换）：%s", n.group, n.version, message)
		key := n.id + ":" + n.outcome
		now := p.deps.Clock.Now()
		if err = p.RecordEvent(ctx, n.node, eventlog.Event{ID: key, Type: kind, OccurredAt: time.Unix(0, at).UTC(), Message: message}); err != nil {
			return err
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = queueTelegram(ctx, tx, key, n.node, []string{message}, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE entry_blocked_decisions SET "+column+"=1 WHERE id=?", n.id)
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
		p.wakeTelegram()
	}
	return nil
}
