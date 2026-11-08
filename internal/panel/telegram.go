package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type TelegramMessage struct {
	BotToken, ChatID, Text string
}

// QueueTelegram 为面板动作提供通知接缝。相同事故的安全摘要合并，重复摘要不重发。
// 只持久化，不等待外部服务；调用方不得把秘密、请求头或原始响应传入摘要。
func (p *Panel) QueueTelegram(ctx context.Context, incidentID, nodeID, message string) error {
	if len(incidentID) == 0 || len(incidentID) > 128 || !utf8.ValidString(incidentID) || len(nodeID) > 128 || len(message) == 0 || len(message) > 2048 || !utf8.ValidString(message) {
		return errors.New("通知事故 ID 或摘要无效")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := queueTelegram(ctx, tx, incidentID, nodeID, []string{message}, p.deps.Clock.Now()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.wakeTelegram()
	return nil
}

func queueTelegram(ctx context.Context, tx *sql.Tx, incidentID, nodeID string, additions []string, now time.Time) error {
	var encoded string
	var parts []string
	err := tx.QueryRowContext(ctx, "SELECT parts FROM telegram_deliveries WHERE incident_id=?", incidentID).Scan(&encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal([]byte(encoded), &parts); err != nil {
			return err
		}
	}
	changed := false
	for _, addition := range additions {
		found := false
		for _, part := range parts {
			if part == addition {
				found = true
				break
			}
		}
		if !found {
			parts = append(parts, addition)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	// 保留事故最新摘要；单条 Telegram 文本和持久化合并区均有界。
	for len(parts) > 1 && len(strings.Join(parts, "\n\n")) > 3500 {
		parts = parts[1:]
	}
	data, err := json.Marshal(parts)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO telegram_deliveries(incident_id,node_id,message,parts,created_at,updated_at,next_attempt_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(incident_id) DO UPDATE SET
		message=excluded.message,parts=excluded.parts,version=telegram_deliveries.version+1,state='pending',updated_at=excluded.updated_at,
		next_attempt_at=CASE WHEN telegram_deliveries.state='pending' THEN telegram_deliveries.next_attempt_at ELSE excluded.next_attempt_at END`,
		incidentID, nodeID, strings.Join(parts, "\n\n"), string(data), now.UnixNano(), now.UnixNano(), now.UnixNano())
	return err
}

func (p *Panel) wakeTelegram() {
	select {
	case p.telegramWake <- struct{}{}:
	default:
	}
}

func (p *Panel) startTelegram(ctx context.Context) {
	p.telegramWake = make(chan struct{}, 1)
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
			case <-p.telegramWake:
			}
			if err := p.expireTelegram(ctx); err != nil {
				continue
			}
			for ctx.Err() == nil {
				more, err := p.deliverTelegram(ctx)
				if err != nil || !more {
					break
				}
			}
		}
	}()
}

func (p *Panel) expireTelegram(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := p.deps.Clock.Now()
	_, err = tx.ExecContext(ctx, `INSERT INTO telegram_failures(incident_id,attempted_at,attempt,message)
		SELECT incident_id,?,attempts,'通知超过 24 小时，已过期' FROM telegram_deliveries WHERE state='pending' AND updated_at<=?`, now.UnixNano(), now.Add(-24*time.Hour).UnixNano())
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE telegram_deliveries SET state='expired' WHERE state='pending' AND updated_at<=?", now.Add(-24*time.Hour).UnixNano())
	}
	if err == nil {
		err = trimTelegramFailures(ctx, tx)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func trimTelegramFailures(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM telegram_failures WHERE id NOT IN (SELECT id FROM telegram_failures ORDER BY id DESC LIMIT 100)")
	return err
}

type telegramDelivery struct {
	id, text          string
	version, attempts int64
}

func (p *Panel) deliverTelegram(ctx context.Context) (bool, error) {
	p.telegramMu.Lock()
	config, err := p.telegramConfig(ctx)
	p.telegramMu.Unlock()
	if err != nil || !config.Enabled {
		return false, err
	}
	var delivery telegramDelivery
	now := p.deps.Clock.Now()
	// 未恢复的离线通知在启动宽限内暂停；测试及有新鲜恢复证据的通知可继续。
	err = p.db.QueryRowContext(ctx, `SELECT d.incident_id,d.message,d.version,d.attempts FROM telegram_deliveries d
		LEFT JOIN offline_incidents i ON d.incident_id='offline:' || i.id
		LEFT JOIN nodes n ON n.id=i.node_id
		WHERE d.state='pending' AND d.next_attempt_at<=? AND (i.id IS NULL OR i.recovered_at IS NOT NULL
		OR (? >= ? AND (n.maintenance_until IS NULL OR n.maintenance_until<=?)))
		ORDER BY d.next_attempt_at,d.created_at,d.incident_id LIMIT 1`, now.UnixNano(), now.UnixNano(), p.startedAt.Add(offlineWindow).UnixNano(), now.Add(-offlineWindow-2*time.Minute).UnixNano()).Scan(&delivery.id, &delivery.text, &delivery.version, &delivery.attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sendErr := p.sendTelegram(ctx, TelegramMessage{BotToken: config.Token, ChatID: config.ChatID, Text: delivery.text})
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return true, p.completeTelegram(ctx, config, delivery, sendErr)
}

func (p *Panel) sendTelegram(ctx context.Context, message TelegramMessage) error {
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := p.deps.Clock.NewTimer(10 * time.Second)
	defer timer.Stop()
	result := make(chan error, 1)
	go func() { result <- p.deps.Telegram(request, message) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return telegramSendError{message: "Telegram 发送超时"}
	case err := <-result:
		return err
	}
}

type telegramSendError struct {
	message    string
	retryAfter time.Duration
}

func (e telegramSendError) Error() string { return e.message }

func (p *Panel) completeTelegram(ctx context.Context, config telegramConfig, delivery telegramDelivery, sendErr error) error {
	p.telegramMu.Lock()
	defer p.telegramMu.Unlock()
	current, err := p.telegramConfig(ctx)
	if err != nil {
		return err
	}
	// 配置改变期间迟到的结果不能宣称新目标可达，也不能消费新目标的待发内容。
	if current.ID != config.ID {
		return nil
	}
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var succeeded any
	if sendErr == nil {
		succeeded = now.UnixNano()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO telegram_status(id,config_id,reachable,attempted_at,succeeded_at)
		VALUES(1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET config_id=excluded.config_id,reachable=excluded.reachable,attempted_at=excluded.attempted_at,
		succeeded_at=CASE WHEN excluded.reachable THEN excluded.succeeded_at WHEN telegram_status.config_id=excluded.config_id THEN telegram_status.succeeded_at ELSE NULL END`,
		config.ID, sendErr == nil, now.UnixNano(), succeeded)
	if err != nil {
		return err
	}
	if sendErr == nil {
		_, err = tx.ExecContext(ctx, `UPDATE telegram_deliveries SET attempts=attempts+1,sent_version=?,state=CASE WHEN version=? THEN 'sent' ELSE 'pending' END,next_attempt_at=? WHERE incident_id=?`, delivery.version, delivery.version, now.UnixNano(), delivery.id)
	} else {
		message := "Telegram 发送失败，请检查凭据、聊天目标与网络"
		delay := 5 * time.Second
		for i := int64(0); i < delivery.attempts && delay < 5*time.Minute; i++ {
			delay *= 2
		}
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
		var safe telegramSendError
		if errors.As(sendErr, &safe) {
			message = safe.message
			if safe.retryAfter > delay {
				delay = safe.retryAfter
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE telegram_deliveries SET attempts=attempts+1,next_attempt_at=? WHERE incident_id=?", now.Add(delay).UnixNano(), delivery.id)
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO telegram_failures(incident_id,attempted_at,attempt,message) VALUES(?,?,?,?)", delivery.id, now.UnixNano(), delivery.attempts+1, message)
		}
		if err == nil {
			err = trimTelegramFailures(ctx, tx)
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
