package panel

import (
	"context"
	"crypto/sha256"
	"fmt"

	"nbpass/internal/eventlog"
)

// 明确拒绝先落逐条终态，通知存储故障不能将其重新变成可重发的 DNS 动作。
func (p *Panel) notifyRecordRejections(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT r.action_id,r.managed_record_id,a.node_id,r.record_id,r.last_error FROM entry_action_records r JOIN entry_actions a ON a.id=r.action_id WHERE r.stage=? AND r.rejection_notified=0`, EntryFailed)
	if err != nil {
		return err
	}
	type rejection struct{ action, managed, node, record, message string }
	var items []rejection
	for rows.Next() {
		var r rejection
		if err = rows.Scan(&r.action, &r.managed, &r.node, &r.record, &r.message); err != nil {
			break
		}
		items = append(items, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range items {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("dns-rejected:%x", sha256.Sum256([]byte(r.action+":"+r.managed)))
		now := p.deps.Clock.Now()
		message := fmt.Sprintf("受管记录 %s 动作 %s 明确失败：%s；已成功的其他记录保持原状，请管理员核对", r.record, r.action, r.message)
		err = insertEvent(ctx, tx, r.node, "panel", eventlog.Event{ID: key, Type: "entry_action", OccurredAt: now, Message: message}, now)
		if err == nil {
			err = queueTelegram(ctx, tx, key, r.node, []string{message}, now)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE entry_action_records SET rejection_notified=1 WHERE action_id=? AND managed_record_id=? AND stage=?`, r.action, r.managed, EntryFailed)
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
