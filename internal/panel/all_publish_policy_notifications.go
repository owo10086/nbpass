package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	"nbpass/internal/eventlog"
)

func (p *Panel) memberDecisionTx(ctx context.Context, tx *sql.Tx, g string, m allMemberPublication, kind, identity, message, action string) error {
	key := fmt.Sprintf("entry-member:%x", sha256.Sum256([]byte(g+":"+m.NodeID+":"+m.Record.ID+":"+fmt.Sprint(m.Record.BindingGeneration)+":"+kind+":"+identity)))
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entry_member_decisions(id,group_id,node_id,managed_record_id,binding_generation,kind,message,action_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, key, g, m.NodeID, m.Record.ID, m.Record.BindingGeneration, kind, "入口组 "+g+" 成员 "+m.NodeID+"："+message, action, p.deps.Clock.Now().UnixNano())
	return err
}

func (p *Panel) memberActionDecisionTx(ctx context.Context, tx *sql.Tx, g string, m allMemberPublication, a EntryAction, reason string) error {
	kind := "entry_member_withdrawn"
	if a.Kind == "manual_restore" || a.Kind == "auto_restore" {
		kind = "entry_member_restored"
	}
	return p.memberDecisionTx(ctx, tx, g, m, kind, a.ID, reason+"，服务商已确认（动作 "+a.ID+"）", a.ID)
}

// 从逐记录确认重建通知。事件与队列分开，通知故障不阻塞 DNS 或丢失历史。
func (p *Panel) flushAllMemberNotifications(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT d.id,d.node_id,d.kind,d.message,d.created_at FROM entry_member_decisions d WHERE d.notified=0 AND (d.action_id='' OR EXISTS(SELECT 1 FROM entry_action_records r JOIN entry_actions a ON a.id=r.action_id WHERE r.action_id=d.action_id AND r.managed_record_id=d.managed_record_id AND r.binding_generation=d.binding_generation AND r.stage=? AND a.stage NOT IN (?,?))) ORDER BY d.created_at`, EntryWritten, EntryArchived, EntrySuperseded)
	if err != nil {
		return err
	}
	type notice struct {
		id, node, kind, message string
		at                      int64
	}
	var items []notice
	for rows.Next() {
		var n notice
		if err = rows.Scan(&n.id, &n.node, &n.kind, &n.message, &n.at); err != nil {
			break
		}
		items = append(items, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range items {
		if err = p.RecordEvent(ctx, n.node, eventlog.Event{ID: n.id, Type: n.kind, OccurredAt: time.Unix(0, n.at).UTC(), Message: n.message}); err != nil {
			return err
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = queueTelegram(ctx, tx, n.id, n.node, []string{n.message}, p.deps.Clock.Now())
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE entry_member_decisions SET notified=1 WHERE id=?", n.id)
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
