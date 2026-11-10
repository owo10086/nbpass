package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"nbpass/internal/dnswatch"
)

// 自动监控与绑定共用事务。已有手工监控转为入口组管理，保留变化历史。
func (p *Panel) bindEntryDomainWatch(ctx context.Context, tx *sql.Tx, group string) error {
	var domain, name string
	if err := tx.QueryRowContext(ctx, "SELECT domain,name FROM managed_records WHERE group_id=? AND owner_kind='group'", group).Scan(&domain, &name); err != nil {
		return err
	}
	if name != "@" {
		domain = name + "." + domain
	}
	domain, err := dnswatch.Domain(domain)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM dns_watches WHERE entry_group_id=? AND domain<>?", group, domain); err != nil {
		return err
	}
	now := p.deps.Clock.Now().UnixNano()
	// 优先接管同名手工监控；其他入口组的同名监控有各自独立的期望。
	_, err = tx.ExecContext(ctx, `UPDATE dns_watches SET entry_group_id=?,expected_ips='[]',expected_version=0,next_sample_at=?,last_status='' WHERE domain=? AND entry_group_id IS NULL AND NOT EXISTS(SELECT 1 FROM dns_watches WHERE entry_group_id=?)`, group, now, domain, group)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO dns_watches(id,domain,expected_ips,expected_version,next_sample_at,created_at,entry_group_id) SELECT ?,?,'[]',0,?,?,? WHERE NOT EXISTS(SELECT 1 FROM dns_watches WHERE entry_group_id=?)`, "entry:"+group, domain, now, now, group, group)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO entry_dns_audits(group_id,next_check_at) VALUES(?,?)", group, now)
	}
	if err == nil {
		err = resetEntryWatchPaths(ctx, tx, group)
	}
	return err
}

func resetEntryWatchPaths(ctx context.Context, tx *sql.Tx, group string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM dns_watch_tasks WHERE watch_id IN (SELECT id FROM dns_watches WHERE entry_group_id=?)", group)
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE dns_watch_paths SET active=0,consecutive_matches=0,consecutive_mismatches=0,sampled_at=NULL WHERE watch_id IN (SELECT id FROM dns_watches WHERE entry_group_id=?)", group)
	}
	return err
}

// 在提交期望的同一事务内撤销旧任务，连尚未写入服务商的版本也不能接受旧采样。
func (p *Panel) resetEntryDomainWatch(ctx context.Context, tx *sql.Tx, a EntryAction) error {
	_, err := tx.ExecContext(ctx, `UPDATE dns_watches SET expected_ips=?,expected_version=?,propagation_status=?,observation_deadline=NULL,next_sample_at=?,last_status='' WHERE entry_group_id=?`, watchJSON([]string{a.NewValue}), a.Version, EntryDesired, p.deps.Clock.Now().UnixNano(), a.GroupID)
	if err == nil {
		err = resetEntryWatchPaths(ctx, tx, a.GroupID)
	}
	return err
}

// DNSPod 成功即结束队列动作；传播状态只属于当前期望版本。
func (p *Panel) beginEntryPropagation(ctx context.Context, tx *sql.Tx, a EntryAction, now time.Time) error {
	window := min(time.Hour, max(10*time.Minute, time.Duration(a.OldTTL)*2*time.Second))
	_, err := tx.ExecContext(ctx, `INSERT INTO entry_dns_previous(watch_id,ip,expires_at) SELECT id,?,? FROM dns_watches WHERE entry_group_id=? ON CONFLICT(watch_id,ip) DO UPDATE SET expires_at=max(expires_at,excluded.expires_at)`, a.OldValue, now.Add(time.Duration(a.OldTTL)*time.Second).UnixNano(), a.GroupID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE dns_watches SET propagation_status='传播中',observation_deadline=?,next_sample_at=? WHERE entry_group_id=? AND expected_version=?`, now.Add(window).UnixNano(), now.UnixNano(), a.GroupID, a.Version)
	}
	// 写入前采到的新值不作为写入后的传播证据。
	if err == nil {
		var current int
		err = tx.QueryRowContext(ctx, "SELECT count(*) FROM dns_watches WHERE entry_group_id=? AND expected_version=?", a.GroupID, a.Version).Scan(&current)
		if err == nil && current > 0 {
			err = resetEntryWatchPaths(ctx, tx, a.GroupID)
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE entry_dns_audits SET next_check_at=? WHERE group_id=?", now.Add(30*time.Second).UnixNano(), a.GroupID)
	}
	return err
}

// 只有未过期的旧 A 值享受缓存宽限；混合的新旧答案也不能误报污染。
func entryWatchExpectation(ctx context.Context, tx *sql.Tx, id string, e dnswatch.Expectation, ips []string, now time.Time) (dnswatch.Expectation, error) {
	rows, err := tx.QueryContext(ctx, "SELECT ip FROM entry_dns_previous WHERE watch_id=? AND expires_at>?", id, now.UnixNano())
	if err != nil {
		return e, err
	}
	allowed := append([]string{}, e.IPs...)
	for rows.Next() {
		var ip string
		if err = rows.Scan(&ip); err != nil {
			break
		}
		allowed = append(allowed, ip)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return e, err
	}
	if len(ips) > 0 {
		cached := true
		for _, ip := range ips {
			cached = cached && slices.Contains(allowed, ip)
		}
		if cached {
			e.PreviousIPs = ips
			e.IgnorePreviousUntil = now.Add(time.Nanosecond)
		}
	}
	return e, nil
}

// 调度和收敛各自持久化；网络查询和入口队列不占用此锁。
func (p *Panel) reconcileEntryPropagation(ctx context.Context, checkers map[string]string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id,domain,expected_version,round,observation_deadline FROM dns_watches WHERE propagation_status='传播中'")
	if err != nil {
		return err
	}
	type observation struct {
		id, domain               string
		version, round, deadline int64
	}
	items := []observation{}
	for rows.Next() {
		var o observation
		if err = rows.Scan(&o.id, &o.domain, &o.version, &o.round, &o.deadline); err != nil {
			break
		}
		items = append(items, o)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	now := p.deps.Clock.Now()
	for _, o := range items {
		status := ""
		if now.UnixNano() >= o.deadline {
			status = "传播未确认"
		} else {
			var pending int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM dns_watch_tasks WHERE watch_id=?", o.id).Scan(&pending); err != nil {
				return err
			}
			if pending != 0 {
				continue
			}
			paths, e := tx.QueryContext(ctx, `SELECT node_id,consecutive_matches,sampled_at FROM dns_watch_paths WHERE watch_id=? AND expected_version=? AND round=? AND active=1`, o.id, o.version, o.round)
			if e != nil {
				return e
			}
			all, domestic := true, false
			for paths.Next() {
				var node string
				var matches int
				var at sql.NullInt64
				if err = paths.Scan(&node, &matches, &at); err != nil {
					break
				}
				if node != "" && checkers[node] == "" {
					continue
				}
				if !at.Valid {
					continue
				}
				fresh := now.Sub(time.Unix(0, at.Int64)) <= time.Minute
				all = all && fresh && matches >= 2
				domestic = domestic || (node != "" && fresh && matches >= 2)
			}
			if err == nil {
				err = paths.Err()
			}
			paths.Close()
			if err != nil {
				return err
			}
			if all && domestic {
				status = "已生效"
			}
		}
		if status == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE dns_watches SET propagation_status=?,next_sample_at=? WHERE id=? AND expected_version=?", status, now.Add(5*time.Minute).UnixNano(), o.id, o.version); err != nil {
			return err
		}
		if status == "传播未确认" {
			id := fmt.Sprintf("entry-propagation:%s:%d", o.id, o.version)
			summary := fmt.Sprintf("入口组域名「%s」传播未确认（期望版本 %d），请核对各解析路径。", o.domain, o.version)
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO dns_watch_notices(id,watch_id,type,summary) VALUES(?,?,'entry_propagation_unconfirmed',?)", id, o.id, summary); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM entry_dns_previous WHERE expires_at<=?", now.UnixNano())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Panel) getEntryDomainWatch(w http.ResponseWriter, r *http.Request) {
	var id string
	err := p.db.QueryRowContext(r.Context(), "SELECT id FROM dns_watches WHERE entry_group_id=?", r.PathValue("id")).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "入口组尚未绑定域名")
		return
	}
	if err != nil {
		problem(w, 503, "解析监控暂不可用")
		return
	}
	r.SetPathValue("id", id)
	p.getDNSWatch(w, r)
}
