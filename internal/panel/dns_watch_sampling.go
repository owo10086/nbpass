package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/dnswatch"
	"nbpass/internal/eventlog"
)

type dnsWatchTask struct {
	ID, WatchID, PathID, NodeID, Instance, Domain, Resolver, Kind string
	Version, Round, Expires                                       int64
}

func scanWatchTask(row interface{ Scan(...any) error }) (dnsWatchTask, error) {
	var t dnsWatchTask
	err := row.Scan(&t.ID, &t.WatchID, &t.PathID, &t.NodeID, &t.Instance, &t.Domain, &t.Resolver, &t.Kind, &t.Version, &t.Round, &t.Expires)
	return t, err
}

const watchTaskColumns = "id,watch_id,path_id,node_id,process_instance,domain,resolver,kind,expected_version,round,expires_at"

func (p *Panel) startDNSWatch(ctx context.Context) {
	p.dnsWatchWake = make(chan struct{}, 1)
	jobs := make(chan dnsWatchTask, 4)
	done := make(chan string, 4)
	for range 4 {
		p.background.Add(1)
		go func() {
			defer p.background.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case task := <-jobs:
					query, cancel := p.dnsContext(ctx)
					ips, err := p.deps.DNSWatchLookup(query, task.Domain, dnswatch.Resolver{Kind: task.Kind, Address: task.Resolver})
					failed := err != nil || query.Err() != nil
					cancel()
					if ctx.Err() == nil {
						_ = p.completeDNSWatchTask(ctx, task, ips, failed)
					}
					select {
					case done <- task.ID:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		busy := map[string]bool{}
		for {
			if ctx.Err() != nil {
				return
			}
			// 传播轮次与任务期限同为 30 秒，先结算漏报，避免被新轮次清除。
			p.expireDNSWatchTasks(ctx)
			_ = p.scheduleDNSWatch(ctx)
			p.flushDNSWatchNotices(ctx)
			rows, err := p.db.QueryContext(ctx, "SELECT "+watchTaskColumns+" FROM dns_watch_tasks WHERE node_id='' AND expires_at>? ORDER BY expires_at LIMIT 32", p.deps.Clock.Now().UnixNano())
			tasks := []dnsWatchTask{}
			if err == nil {
				for rows.Next() {
					task, e := scanWatchTask(rows)
					if e != nil {
						break
					}
					tasks = append(tasks, task)
				}
				rows.Close()
			}
			for _, task := range tasks {
				if !busy[task.ID] {
					select {
					case jobs <- task:
						busy[task.ID] = true
					default:
					}
				}
			}
			timer := p.deps.Clock.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case id := <-done:
				delete(busy, id)
			case <-p.dnsWatchWake:
			case <-timer.C():
			}
			timer.Stop()
		}
	}()
}

func (p *Panel) scheduleDNSWatch(ctx context.Context) error {
	checkers, err := p.dnsWatchCheckers(ctx)
	if err != nil {
		return err
	}
	p.dnsWatchMu.Lock()
	err = p.reconcileEntryPropagation(ctx, checkers)
	if err == nil {
		err = p.createDNSWatchTasks(ctx, checkers)
	}
	if err == nil {
		err = p.claimDNSWatchTasks(ctx)
	}
	p.dnsWatchMu.Unlock()
	if err != nil {
		return err
	}
	// 只在到期且存在待派发任务时唤醒，快照版本不因此改变。
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	rows, err := p.db.QueryContext(ctx, "SELECT DISTINCT node_id FROM dns_watch_tasks WHERE node_id<>'' AND expires_at>?", p.deps.Clock.Now().UnixNano())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if s := p.controls[id]; s != nil {
			select {
			case s.changed <- struct{}{}:
			default:
			}
		}
	}
	return rows.Err()
}
func (p *Panel) createDNSWatchTasks(ctx context.Context, checkers map[string]string) error {
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var encoded string
	if err = tx.QueryRowContext(ctx, "SELECT resolvers FROM dns_watch_settings WHERE id=1").Scan(&encoded); err != nil {
		return err
	}
	var resolvers []dnswatch.Resolver
	if err = json.Unmarshal([]byte(encoded), &resolvers); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,domain,expected_version,round,propagation_status FROM dns_watches WHERE next_sample_at<=? AND propagation_status<>? ORDER BY next_sample_at LIMIT 32", now.UnixNano(), EntryDesired)
	if err != nil {
		return err
	}
	type watch struct {
		id, domain     string
		version, round int64
		propagation    string
	}
	watches := []watch{}
	for rows.Next() {
		var w watch
		if err = rows.Scan(&w.id, &w.domain, &w.version, &w.round, &w.propagation); err != nil {
			break
		}
		watches = append(watches, w)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, w := range watches {
		interval := 5 * time.Minute
		if w.propagation == "传播中" {
			interval = 30 * time.Second
		}
		if _, err = tx.ExecContext(ctx, "UPDATE dns_watches SET round=round+1,next_sample_at=? WHERE id=?", now.Add(interval).UnixNano(), w.id); err != nil {
			return err
		}
		// 未采到的轮次、角色中断均不能与后来的异常拼成连续证据。
		if _, err = tx.ExecContext(ctx, `UPDATE dns_watch_paths SET consecutive_mismatches=0,consecutive_matches=0 WHERE watch_id=? AND (active=0 OR status='等待采样' OR sampled_at IS NULL OR round<>? OR sampled_at<?)`, w.id, w.round, now.Add(-interval-30*time.Second).UnixNano()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE dns_watch_paths SET active=0 WHERE watch_id=?", w.id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM dns_watch_tasks WHERE watch_id=?", w.id); err != nil {
			return err
		}
		for _, r := range resolvers {
			nodes := map[string]string{"": ""}
			if r.Kind == "dns" {
				nodes = checkers
			}
			for node, instance := range nodes {
				path := node + "|" + r.Key()
				id, _, e := p.randomCredential()
				if e != nil {
					return e
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO dns_watch_tasks(id,watch_id,path_id,node_id,process_instance,domain,resolver,kind,expected_version,round,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, w.id, path, node, instance, w.domain, r.Address, r.Kind, w.version, w.round+1, int64(0))
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO dns_watch_paths(watch_id,path_id,node_id,kind,resolver,expected_version,round) VALUES(?,?,?,?,?,?,?) ON CONFLICT(watch_id,path_id) DO UPDATE SET active=1,status='等待采样',ips='[]',round=excluded.round,expected_version=excluded.expected_version`, w.id, path, node, r.Kind, r.Address, w.version, w.round+1)
				if err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}
func (p *Panel) dnsWatchCommands(ctx context.Context, node, instance string) ([]*controlpb.DNSWatchTask, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT "+watchTaskColumns+" FROM dns_watch_tasks WHERE node_id=? AND process_instance=? AND expires_at>? ORDER BY expires_at,id LIMIT 4", node, instance, p.deps.Clock.Now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*controlpb.DNSWatchTask{}
	for rows.Next() {
		task, e := scanWatchTask(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, &controlpb.DNSWatchTask{TaskId: task.ID, Domain: task.Domain, Resolver: task.Resolver, ExpiresAt: task.Expires, ExpectedVersion: task.Version, Round: task.Round})
	}
	return result, rows.Err()
}
func (p *Panel) acceptDNSWatchResults(ctx context.Context, node, instance string, results []*controlpb.DNSWatchResult) error {
	if len(results) > 16 {
		return errors.New("解析采样批次过大")
	}
	for _, r := range results {
		if r == nil || len(r.TaskId) != 43 || len(r.Ips) > 64 {
			return errors.New("解析采样格式无效")
		}
		task, err := scanWatchTask(p.db.QueryRowContext(ctx, "SELECT "+watchTaskColumns+" FROM dns_watch_tasks WHERE id=? AND node_id=? AND process_instance=? AND expires_at>?", r.TaskId, node, instance, p.deps.Clock.Now().UnixNano()))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		// 角色在任务进行中被撤销、维护或失去活性时，不接受旧资格的证据。
		var qualified int
		err = p.db.QueryRowContext(ctx, `SELECT count(*) FROM nodes n JOIN node_regions r ON r.node_id=n.id WHERE n.id=?
   AND CASE WHEN r.override_name IS NULL THEN r.auto_mainland ELSE r.override_mainland END=1 AND r.checker_disabled=0
   AND n.disconnected_at IS NULL AND n.last_seen_at>? AND (n.maintenance_until IS NULL OR n.maintenance_until<=?)`, node, p.deps.Clock.Now().Add(-offlineWindow).UnixNano(), p.deps.Clock.Now().UnixNano()).Scan(&qualified)
		if err != nil {
			return err
		}
		if qualified == 0 {
			continue
		}
		if err = p.completeDNSWatchTask(ctx, task, r.Ips, r.Failed); err != nil {
			return err
		}
	}
	return nil
}
func (p *Panel) expireDNSWatchTasks(ctx context.Context) {
	rows, err := p.db.QueryContext(ctx, "SELECT "+watchTaskColumns+" FROM dns_watch_tasks WHERE expires_at>0 AND expires_at<=?", p.deps.Clock.Now().UnixNano())
	if err != nil {
		return
	}
	tasks := []dnsWatchTask{}
	for rows.Next() {
		task, e := scanWatchTask(rows)
		if e != nil {
			break
		}
		tasks = append(tasks, task)
	}
	rows.Close()
	for _, task := range tasks {
		_ = p.completeDNSWatchTask(ctx, task, nil, true)
	}
}
func (p *Panel) completeDNSWatchTask(ctx context.Context, task dnsWatchTask, values []string, failed bool) error {
	p.dnsWatchMu.Lock()
	defer p.dnsWatchMu.Unlock()
	ips, err := dnswatch.Addresses(values)
	failed = failed || err != nil || len(ips) == 0 || p.deps.Clock.Now().UnixNano() >= task.Expires
	if failed {
		ips = []string{}
	}
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var encoded, domain, previousStatus string
	var version, round int64
	err = tx.QueryRowContext(ctx, `SELECT w.expected_ips,w.expected_version,w.round,w.domain,w.last_status FROM dns_watches w JOIN dns_watch_tasks t ON t.watch_id=w.id WHERE t.id=? AND t.expected_version=w.expected_version AND t.round=w.round`, task.ID).Scan(&encoded, &version, &round, &domain, &previousStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var expected []string
	if err = json.Unmarshal([]byte(encoded), &expected); err != nil {
		return err
	}
	var progress dnswatch.Progress
	var lastEncoded string
	err = tx.QueryRowContext(ctx, "SELECT expected_version,consecutive_mismatches,consecutive_matches,last_success FROM dns_watch_paths WHERE watch_id=? AND path_id=?", task.WatchID, task.PathID).Scan(&progress.ExpectedVersion, &progress.ConsecutiveMismatches, &progress.ConsecutiveMatches, &lastEncoded)
	if err != nil {
		return err
	}
	var last []string
	if err = json.Unmarshal([]byte(lastEncoded), &last); err != nil {
		return err
	}
	expectation, err := entryWatchExpectation(ctx, tx, task.WatchID, dnswatch.Expectation{Version: version, IPs: expected}, ips, now)
	if err != nil {
		return err
	}
	state := progress.Observe(expectation, ips, failed, now)
	changed := !failed && len(last) > 0 && !slices.Equal(last, ips)
	if !failed {
		for _, ip := range ips {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO dns_watch_ips(watch_id,ip) VALUES(?,?)", task.WatchID, ip); err != nil {
				return err
			}
		}
	}
	if changed || failed {
		_, err = tx.ExecContext(ctx, "INSERT INTO dns_watch_history(watch_id,path_id,sampled_at,old_ips,ips,status,changed) VALUES(?,?,?,?,?,?,?)", task.WatchID, task.PathID, now.UnixNano(), lastEncoded, watchJSON(ips), state, changed)
		if err != nil {
			return err
		}
	}
	if !failed {
		lastEncoded = watchJSON(ips)
	}
	_, err = tx.ExecContext(ctx, `UPDATE dns_watch_paths SET ips=?,last_success=?,status=?,consecutive_mismatches=?,consecutive_matches=?,sampled_at=?,expected_version=? WHERE watch_id=? AND path_id=?`, watchJSON(ips), lastEncoded, state, progress.ConsecutiveMismatches, progress.ConsecutiveMatches, now.UnixNano(), progress.ExpectedVersion, task.WatchID, task.PathID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM dns_watch_tasks WHERE id=?", task.ID); err != nil {
		return err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM dns_watch_tasks WHERE watch_id=?", task.WatchID).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		var matched, bad, failures int
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(status='符合期望'),0),COALESCE(sum(status='期望不符'),0),COALESCE(sum(status='采样失败'),0) FROM dns_watch_paths WHERE watch_id=? AND active=1 AND round=? AND expected_version=?`, task.WatchID, round, version).Scan(&matched, &bad, &failures); err != nil {
			return err
		}
		aggregate := "观察中"
		eventType := ""
		if matched > 0 {
			aggregate = "正常"
		}
		if failures > 0 {
			aggregate = "采样失败"
			eventType = "dns_watch_failed"
		}
		if bad > 0 {
			aggregate = "期望不符"
			eventType = "dns_watch_mismatch"
		}
		if bad > 0 && matched > 0 {
			aggregate = "疑似污染"
			eventType = "dns_watch_poisoned"
		}
		if aggregate != previousStatus {
			if _, err = tx.ExecContext(ctx, "UPDATE dns_watches SET last_status=? WHERE id=?", aggregate, task.WatchID); err != nil {
				return err
			}
			if eventType != "" {
				sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s", task.WatchID, version, round, eventType)))
				id := "dns-watch:" + hex.EncodeToString(sum[:])
				summary := fmt.Sprintf("域名「%s」解析监控：%s（期望版本 %d）。", domain, aggregate, version)
				_, err = tx.ExecContext(ctx, "INSERT INTO dns_watch_notices(id,watch_id,type,summary) VALUES(?,?,?,?)", id, task.WatchID, eventType, summary)
				if err != nil {
					return err
				}
			}
		}
	}
	if err = tx.Commit(); err == nil {
		p.wakeDNSWatch()
	}
	return err
}

// 两条投递进度分别扫描，通知积压不能占用事件的处理窗口。
func (p *Panel) flushDNSWatchNotices(ctx context.Context) {
	for _, column := range []string{"event_recorded", "notification_queued"} {
		rows, err := p.db.QueryContext(ctx, "SELECT id,type,summary FROM dns_watch_notices WHERE "+column+"=0 ORDER BY rowid LIMIT 32")
		if err != nil {
			continue
		}
		type notice struct{ id, kind, summary string }
		notices := []notice{}
		for rows.Next() {
			var n notice
			if rows.Scan(&n.id, &n.kind, &n.summary) != nil {
				break
			}
			notices = append(notices, n)
		}
		rows.Close()
		for _, n := range notices {
			if column == "event_recorded" {
				err = p.RecordEvent(ctx, "", eventlog.Event{ID: n.id, Type: n.kind, Message: n.summary})
			} else {
				err = p.QueueTelegram(ctx, n.id, "", n.summary)
			}
			if err == nil {
				_, _ = p.db.ExecContext(ctx, "UPDATE dns_watch_notices SET "+column+"=1 WHERE id=?", n.id)
			}
		}
	}
}

// 排队不消耗采样期限：每个执行位置最多认领四项，认领后才开始 30 秒期限。
func (p *Panel) claimDNSWatchTasks(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT node_id FROM dns_watch_tasks WHERE expires_at=0")
	if err != nil {
		return err
	}
	nodes := []string{}
	for rows.Next() {
		var node string
		if err = rows.Scan(&node); err != nil {
			break
		}
		nodes = append(nodes, node)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	now := p.deps.Clock.Now().UnixNano()
	deadline := p.deps.Clock.Now().Add(30 * time.Second).UnixNano()
	for _, node := range nodes {
		var running int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM dns_watch_tasks WHERE node_id=? AND expires_at>?", node, now).Scan(&running); err != nil {
			return err
		}
		if running >= 4 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE dns_watch_tasks SET expires_at=? WHERE id IN (SELECT id FROM dns_watch_tasks WHERE node_id=? AND expires_at=0 ORDER BY rowid LIMIT ?)`, deadline, node, 4-running); err != nil {
			return err
		}
	}
	return tx.Commit()
}
