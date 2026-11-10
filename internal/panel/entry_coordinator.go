package panel

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
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
	rows, err := p.db.QueryContext(ctx, "SELECT DISTINCT CASE WHEN owner_node_id IS NULL THEN group_id ELSE 'node:'||owner_node_id END FROM entry_actions WHERE stage NOT IN "+terminalActionStages)
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
		a, err := scanAction(c.p.db.QueryRowContext(c.ctx, "SELECT "+actionColumns+" FROM entry_actions WHERE (CASE WHEN owner_node_id IS NULL THEN group_id ELSE 'node:'||owner_node_id END)=? AND stage NOT IN "+terminalActionStages+" ORDER BY version LIMIT 1", gid))
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
