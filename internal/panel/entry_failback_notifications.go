package panel

import (
	"context"
	"fmt"

	"nbpass/internal/eventlog"
)

// 迁移29：处理标记独立于可清理的事件/通知历史，避免清理后重新发送。
const entryFailbackMigration = `ALTER TABLE entry_actions ADD COLUMN failback_notified INTEGER NOT NULL DEFAULT 0 CHECK(failback_notified IN (0,1));`

// 从持久动作续投，不在 DNS 协调器内等待事件/通知队列写入。
func (p *Panel) flushEntryFailbackNotifications(ctx context.Context) error {
	// 恢复核对会把未完成的旧动作封存为 EntryWritten，但不填 written_at；
	// 封存表示已处理，不能据此宣称曾成功写入或发送回切通知。
	rows, err := p.db.QueryContext(ctx, "SELECT "+actionColumns+" FROM entry_actions WHERE kind='failback' AND stage=? AND written_at IS NOT NULL AND failback_notified=0 ORDER BY created_at", EntryWritten)
	if err != nil {
		return err
	}
	var actions []EntryAction
	for rows.Next() {
		a, e := scanAction(rows)
		if e != nil {
			err = e
			break
		}
		actions = append(actions, a)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, a := range actions {
		if a.WrittenAt == nil {
			continue
		}
		key := "entry-failback:" + a.ID
		message := fmt.Sprintf("入口组 %s 期望版本 %d：入口回切已写入 DNSPod，首选入口 %s，%s → %s（动作 %s）", a.GroupID, a.Version, a.NodeID, a.OldValue, a.NewValue, a.ID)
		now := p.deps.Clock.Now()
		if err = p.RecordEvent(ctx, a.NodeID, eventlog.Event{ID: key, Type: "entry_failback", OccurredAt: *a.WrittenAt, Message: message}); err != nil {
			return err
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = queueTelegram(ctx, tx, key, a.NodeID, []string{message}, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE entry_actions SET failback_notified=1 WHERE id=?", a.ID)
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
