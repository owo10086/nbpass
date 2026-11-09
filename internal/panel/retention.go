package panel

import (
	"context"
	"time"
)

const retentionBatchSize = 256

// 只允许清理展示历史；实时序号、动作、额度、配置版本和流量高水位不在此处维护。
// 每条 DELETE 独立提交，不持有控制锁，也不把整个积压放进一个事务。
func (p *Panel) cleanHistoryBatch(ctx context.Context) (bool, error) {
	now := p.deps.Clock.Now()
	statements := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM events WHERE sequence IN (
 SELECT sequence FROM events
 WHERE occurred_at < ? - (SELECT event_days FROM retention_settings WHERE id=1) * ?
 ORDER BY occurred_at LIMIT ?)`, []any{now.UnixNano(), int64(24 * time.Hour), retentionBatchSize}},
		{`DELETE FROM probe_minutes WHERE rowid IN (
 SELECT rowid FROM probe_minutes
 WHERE minute < ? - (SELECT probe_hours FROM retention_settings WHERE id=1) * 3600
 ORDER BY minute LIMIT ?)`, []any{now.Truncate(time.Minute).Unix(), retentionBatchSize}},
	}
	more := false
	for _, statement := range statements {
		// 设置读取与删除在同一语句内完成，避免保存较长保留期后仍按旧设置删除。
		result, err := p.db.ExecContext(ctx, statement.query, statement.args...)
		if err != nil {
			return more, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return more, err
		}
		more = more || deleted == retentionBatchSize
	}
	return more, nil
}

func (p *Panel) startRetention(ctx context.Context) {
	p.retentionWake = make(chan struct{}, 1)
	first := p.deps.Clock.NewTimer(0)
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
			case <-p.retentionWake:
			}
			timer.Stop()
			more, err := p.cleanHistoryBatch(ctx)
			delay := time.Minute
			if err == nil && more {
				// 积压继续分批排空，批次之间释放数据库并给其他写入机会。
				delay = 10 * time.Millisecond
			}
			timer = p.deps.Clock.NewTimer(delay)
		}
	}()
}
