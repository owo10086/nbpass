package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nbpass/internal/eventlog"
	"nbpass/internal/selfcheck"
)

// 每轮保留原始结果和最终结果。票 19 可在未知轮上附加补判证据，不能覆盖原始自测。
func (p *Panel) acceptSelfChecks(ctx context.Context, nodeID string, batch [][]byte) ([]string, error) {
	if len(batch) > 8 {
		return nil, errors.New("自测批次过大")
	}
	rounds := make([]selfcheck.Round, len(batch))
	size := 0
	now := p.deps.Clock.Now()
	for i, data := range batch {
		size += len(data)
		if size > 48<<10 {
			return nil, errors.New("自测批次过大")
		}
		if err := json.Unmarshal(data, &rounds[i]); err != nil {
			return nil, err
		}
		if err := rounds[i].Validate(now); err != nil {
			return nil, err
		}
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var enabled bool
	state := "unknown"
	consecutive := 0
	var last uint64
	var previousKey string
	err = tx.QueryRowContext(ctx, "SELECT enabled,state,consecutive,last_sequence,last_config_key FROM node_self_check WHERE node_id=?", nodeID).Scan(&enabled, &state, &consecutive, &last, &previousKey)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	settings, err := loadSelfCheckSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	var restoredAt int64
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(restored_at),0) FROM recovery_state").Scan(&restoredAt); err != nil {
		return nil, err
	}
	currentKey := selfcheck.ConfigKey(selfcheck.Config{Enabled: enabled, Settings: settings})
	ack := []string{}
	for _, round := range rounds {
		ack = append(ack, round.ID)
		var stored string
		err = tx.QueryRowContext(ctx, "SELECT round_id FROM self_check_rounds WHERE node_id=? AND (round_id=? OR sequence=?)", nodeID, round.ID, round.Sequence).Scan(&stored)
		if err == nil {
			if stored != round.ID {
				return nil, errors.New("自测序号冲突")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		eligible := 0
		if round.Sequence <= last || round.StartedAt.UnixNano() < restoredAt {
			eligible = -1
		} else if enabled && round.ConfigKey == currentKey {
			eligible = 1
		}
		data, _ := json.Marshal(round)
		_, err = tx.ExecContext(ctx, `INSERT INTO self_check_rounds(node_id,round_id,sequence,config_revision,started_at,finished_at,received_at,result,final_result,evidence_source,raw,decision_eligible) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, nodeID, round.ID, round.Sequence, round.ConfigRevision, round.StartedAt.UnixNano(), round.FinishedAt.UnixNano(), now.UnixNano(), round.Result, round.Result, "self_check", data, eligible)
		if err != nil {
			return nil, err
		}
		if round.Sequence <= last {
			continue
		}
		old := state
		if round.Sequence != last+1 || previousKey != round.ConfigKey {
			consecutive = 0
		}
		last = round.Sequence
		previousKey = round.ConfigKey
		// 旧配置结果仍可追查，但不能参与当前配置的判定或解除状态。
		if !enabled || round.ConfigKey != currentKey || round.StartedAt.UnixNano() < restoredAt {
			consecutive = 0
			continue
		}
		if round.Result == "unknown" {
			if err = p.scheduleCheckers(ctx, tx, nodeID, round); err != nil {
				return nil, err
			}
		}
		if round.Result == "normal" {
			if err = p.rotationNormalEvidence(ctx, tx, nodeID, round.ID, round.StartedAt); err != nil {
				return nil, err
			}
		}
		state, consecutive = advanceSelfCheckState(state, consecutive, round.Result)
		kind, message := "", ""
		if old != "blocked" && state == "blocked" {
			kind = "node_blocked"
			message = "节点连续三轮自测异常，认定被墙（自测推定）"
		}
		if (old == "blocked" || old == "suspected") && state == "normal" {
			kind = "node_unblocked"
			message = "节点自测正常，已解除疑似被墙或被墙"
		}
		if kind != "" {
			var name string
			if err := tx.QueryRowContext(ctx, "SELECT name FROM nodes WHERE id=?", nodeID).Scan(&name); err != nil {
				return nil, err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO self_check_notifications(id,node_id,kind,occurred_at,config_revision,message) VALUES(?,?,?,?,?,?)", "self-check:"+nodeID+":"+round.ID, nodeID, kind, round.FinishedAt.UnixNano(), round.ConfigRevision, fmt.Sprintf("%s：%s", name, message))
			if err != nil {
				return nil, err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_self_check(node_id,enabled,state,consecutive,last_sequence,last_config_key) VALUES(?,?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET state=excluded.state,consecutive=excluded.consecutive,last_sequence=excluded.last_sequence,last_config_key=excluded.last_config_key`, nodeID, enabled, state, consecutive, last, previousKey)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	p.wakeCheckerSessions()
	// 事务中的持久待办保证提交后崩溃也能重试，外部通知绝不阻塞状态事务。
	if err = p.flushSelfCheckNotifications(ctx); err != nil {
		return nil, err
	}
	return ack, nil
}
func (p *Panel) flushSelfCheckNotifications(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, "SELECT id,node_id,kind,occurred_at,config_revision,message,event_done,telegram_done FROM self_check_notifications WHERE event_done=0 OR telegram_done=0 ORDER BY occurred_at,id LIMIT 32")
	if err != nil {
		return err
	}
	type notification struct {
		id, nodeID, kind, message string
		at                        int64
		revision                  uint64
		eventDone, telegramDone   bool
	}
	pending := []notification{}
	for rows.Next() {
		var n notification
		if err = rows.Scan(&n.id, &n.nodeID, &n.kind, &n.at, &n.revision, &n.message, &n.eventDone, &n.telegramDone); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range pending {
		if !n.eventDone {
			if err = p.RecordEvent(ctx, n.nodeID, eventlog.Event{ID: n.id, Type: n.kind, OccurredAt: time.Unix(0, n.at).UTC(), ConfigRevision: n.revision, Message: n.message}); err != nil {
				return err
			}
			if _, err = p.db.ExecContext(ctx, "UPDATE self_check_notifications SET event_done=1 WHERE id=?", n.id); err != nil {
				return err
			}
		}
		if !n.telegramDone {
			if err = p.QueueTelegram(ctx, n.id, n.nodeID, n.message); err != nil {
				return err
			}
			if _, err = p.db.ExecContext(ctx, "UPDATE self_check_notifications SET telegram_done=1 WHERE id=?", n.id); err != nil {
				return err
			}
		}
	}
	return nil
}
func (p *Panel) startSelfCheckNotifications(ctx context.Context) {
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		for ctx.Err() == nil {
			_ = p.expireCheckerRounds(ctx)
			_ = p.flushSelfCheckNotifications(ctx)
			timer := p.deps.Clock.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C():
			}
		}
	}()
}
