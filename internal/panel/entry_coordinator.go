package panel

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"nbpass/internal/dnsprovider"
)

type entryCoordinator struct {
	p       *Panel
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	workers map[string]chan struct{}
	wg      sync.WaitGroup
	closed  bool
}

func (p *Panel) startEntryCoordinator(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c := &entryCoordinator{p: p, ctx: ctx, cancel: cancel, workers: make(map[string]chan struct{})}
	p.coordinator = c
	rows, err := p.db.QueryContext(ctx, "SELECT DISTINCT group_id FROM entry_actions WHERE stage<>?", EntryWritten)
	if err != nil {
		cancel()
		return err
	}
	var groups []string
	for rows.Next() {
		var gid string
		if err = rows.Scan(&gid); err != nil {
			break
		}
		groups = append(groups, gid)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		cancel()
		return err
	}
	for _, gid := range groups {
		c.wake(gid)
	}
	return nil
}
func (c *entryCoordinator) wake(gid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	wake, ok := c.workers[gid]
	if !ok {
		wake = make(chan struct{}, 1)
		c.workers[gid] = wake
		c.wg.Add(1)
		go c.run(gid, wake)
		return // 新 worker 会立即查询，不需要初始唤醒跳过失败退避。
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}
func (c *entryCoordinator) close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
}
func (c *entryCoordinator) run(gid string, wake <-chan struct{}) {
	defer c.wg.Done()
	for c.ctx.Err() == nil {
		// 本轮查询已响应此前的唤醒；只允许查询期间的新唤醒打断退避。
		select {
		case <-wake:
		default:
		}
		a, err := scanAction(c.p.db.QueryRowContext(c.ctx, "SELECT "+actionColumns+" FROM entry_actions WHERE group_id=? AND stage<>? ORDER BY version LIMIT 1", gid, EntryWritten))
		if errors.Is(err, sql.ErrNoRows) {
			// 与 wake 共用锁：已有唤醒则重新查询，否则撤下空队列。
			// 提交若在此之后到达，wake 会重建 worker，不会丢失持久化动作。
			c.mu.Lock()
			select {
			case <-wake:
				c.mu.Unlock()
				continue
			default:
				delete(c.workers, gid)
				c.mu.Unlock()
				return
			}
		}
		if err == nil {
			err = c.execute(a)
		}
		if err == nil {
			continue
		}
		// 超时、读失败和保存失败均先等待，再从实际记录核对；绝不直接重发。
		timer := c.p.deps.Clock.NewTimer(5 * time.Second)
		select {
		case <-timer.C():
		case <-wake:
		case <-c.ctx.Done():
			timer.Stop()
			return
		}
		timer.Stop()
	}
}
func (c *entryCoordinator) execute(a EntryAction) error {
	p := c.p
	allowed, e := recoveryGroupAllowed(c.ctx, p.db, a.GroupID)
	if e != nil {
		return e
	}
	if !allowed {
		return c.note(a, "从旧备份恢复，入口组尚未核对，自动写暂停")
	}
	b, err := p.loadManaged(c.ctx, a.GroupID)
	if err != nil {
		return err
	}
	credentials, err := p.dnsCredential(c.ctx, b.AccountID)
	if err != nil {
		return c.note(a, "DNSPod 凭据不可用，等待核对")
	}
	if p.deps.DNSProvider == nil {
		return c.note(a, "DNS 服务商不可用，等待核对")
	}
	key := dnsprovider.Key{Domain: b.Domain, RecordID: b.RecordID}
	auth := dnsprovider.Credentials{TokenID: credentials.TokenID, Token: credentials.Token}
	ctx, cancel := p.dnsContext(c.ctx)
	actual, err := p.deps.DNSProvider.ReadRecord(ctx, auth, key)
	cancel()
	if err != nil {
		return c.note(a, "DNSPod 读回失败，等待核对")
	}
	if _, err = recordWarnings(actual, b.RecordID, b.LineID); err != nil || actual.Record.Name != b.Name {
		return c.note(a, "受管记录类型、名称或线路变化，等待人工核对")
	}
	if actual.Record.Value == a.NewValue && actual.Record.TTL == 60 {
		return c.finish(a)
	}
	// 不确定动作若看到第三个值，不擅自覆盖外部改动。
	if a.writeStarted && actual.Record.Value != a.OldValue && actual.Record.Value != a.NewValue {
		return c.note(a, "实际记录与动作旧值、新值均不同，等待人工核对")
	}
	if !a.writeStarted {
		a.OldValue, a.OldTTL = actual.Record.Value, actual.Record.TTL
	}
	// 动作和开始标记必须落盘成功，之后才允许产生外部副作用。
	if err = c.markWriting(a); err != nil {
		return err
	}
	desired := actual.Record
	desired.Value, desired.TTL = a.NewValue, 60
	ctx, cancel = p.dnsContext(c.ctx)
	err = p.deps.DNSProvider.WriteRecord(ctx, auth, key, desired)
	cancel()
	if c.ctx.Err() != nil {
		return c.ctx.Err()
	}
	if err != nil {
		a.writeStarted = true
		return c.note(a, "DNSPod 写入结果未确认，先读回核对")
	}
	return c.finish(a)
}
func (c *entryCoordinator) markWriting(a EntryAction) error {
	result, err := c.p.db.ExecContext(c.ctx, `UPDATE entry_actions SET old_value=?,old_ttl=?,write_started=1,last_error='' WHERE id=? AND version=? AND stage<>?`, a.OldValue, a.OldTTL, a.ID, a.Version, EntryWritten)
	return missingResult(result, err)
}
func (c *entryCoordinator) note(a EntryAction, message string) error {
	tx, err := c.p.db.BeginTx(c.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stage := a.Stage
	if a.writeStarted {
		stage = EntryUnconfirmed
	}
	_, err = tx.ExecContext(c.ctx, "UPDATE entry_actions SET stage=?,last_error=? WHERE id=? AND version=? AND stage<>?", stage, message, a.ID, a.Version, EntryWritten)
	if err == nil {
		_, err = tx.ExecContext(c.ctx, `UPDATE entry_groups SET publication_status=? WHERE id=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND current_action_id=? AND desired_version=?)`, stage, a.GroupID, a.GroupID, a.ID, a.Version)
	}
	if err == nil {
		err = c.p.actionEvent(c.ctx, tx, a, stage)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	return errors.New(message)
}
func (c *entryCoordinator) finish(a EntryAction) error {
	p := c.p
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(c.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = missingResult(tx.ExecContext(c.ctx, `UPDATE entry_actions SET stage=?,last_error='',written_at=? WHERE id=? AND version=? AND stage<>?`, EntryWritten, now.UnixNano(), a.ID, a.Version, EntryWritten)); err != nil {
		return err
	}
	_, err = tx.ExecContext(c.ctx, "UPDATE managed_records SET current_value=?,current_ttl=60 WHERE group_id=?", a.NewValue, a.GroupID)
	if err == nil {
		_, err = tx.ExecContext(c.ctx, `UPDATE entry_publications SET written_at=? WHERE group_id=? AND current_action_id=? AND desired_version=?`, now.UnixNano(), a.GroupID, a.ID, a.Version)
	}
	if err == nil {
		_, err = tx.ExecContext(c.ctx, `UPDATE entry_groups SET publication_status=? WHERE id=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND current_action_id=? AND desired_version=?)`, EntryWritten, a.GroupID, a.GroupID, a.ID, a.Version)
	}
	if err == nil {
		err = p.actionEvent(c.ctx, tx, a, EntryWritten)
	}
	if err == nil {
		err = p.beginEntryPropagation(c.ctx, tx, a, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		p.wakeDNSWatch()
	}
	return err
}
