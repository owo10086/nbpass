package panel

import (
	"context"
	"encoding/json"
	"time"

	"nbpass/internal/eventlog"
	"nbpass/internal/nodeupdate"
)

func (p *Panel) startUpgradeConfirmation(ctx context.Context) {
	timer := p.deps.Clock.NewTimer(time.Second)
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		defer func() { timer.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C():
				timer = p.deps.Clock.NewTimer(time.Second)
				p.controlMu.Lock()
				_ = p.expireUpgradeConfirmations(ctx)
				p.controlMu.Unlock()
			}
		}
	}()
}

// An absent control report cannot tell us whether installation failed or succeeded.
// Keep the task active and maintenance bounded by its original deadline.
func (p *Panel) expireUpgradeConfirmations(ctx context.Context) error {
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT node_id,progress FROM node_upgrades WHERE active=1 AND created_at<=?", now.Add(-3*time.Minute).UnixNano())
	if err != nil {
		return err
	}
	type pending struct {
		id       string
		progress nodeupdate.Progress
	}
	var updates []pending
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		var v nodeupdate.Progress
		if err = json.Unmarshal(raw, &v); err != nil {
			break
		}
		if v.State != "unconfirmed" {
			v.State = "unconfirmed"
			v.Steps = append(v.Steps, "unconfirmed")
			updates = append(updates, pending{id, v})
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		raw, _ := json.Marshal(u.progress)
		if _, err = tx.ExecContext(ctx, "UPDATE node_upgrades SET progress=? WHERE id=? AND active=1", raw, u.progress.Manifest.TaskID); err != nil {
			return err
		}
		if err = insertEvent(ctx, tx, u.id, "panel", eventlog.Event{ID: "upgrade:" + u.progress.Manifest.TaskID + ":unconfirmed", Type: "node_upgrade", OccurredAt: now, Message: "升级控制确认超时，结果未确认；保留当前程序，等待回连核对"}, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
