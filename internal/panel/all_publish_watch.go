package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"nbpass/internal/dnswatch"
	"slices"
	"time"
)

// 只把逐条确认仍启用的地址纳入集合；失败和未知启用不算已发布。
func (p *Panel) refreshAllWatchTx(ctx context.Context, tx *sql.Tx, a EntryAction, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT current_value FROM managed_records WHERE owner_kind='member' AND group_id=? AND current_enabled=1 AND EXISTS(SELECT 1 FROM entry_groups g WHERE g.id=group_id AND g.first_published_at IS NOT NULL) ORDER BY current_value`, a.GroupID)
	if err != nil {
		return err
	}
	ips := []string{}
	for rows.Next() {
		var ip string
		if err = rows.Scan(&ip); err != nil {
			break
		}
		ips = append(ips, ip)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	ips, err = dnswatch.Addresses(ips)
	if err != nil {
		return err
	}
	// 每条旧值保留各自 TTL 宽限，重复值合并取最晚到期。
	_, err = tx.ExecContext(ctx, `INSERT INTO entry_dns_previous(watch_id,ip,expires_at) SELECT w.id,r.old_value,r.written_at+r.old_ttl*1000000000 FROM entry_action_records r JOIN dns_watches w ON w.entry_group_id=? WHERE r.action_id=? AND r.old_enabled=1 AND r.stage=? ON CONFLICT(watch_id,ip) DO UPDATE SET expires_at=max(expires_at,excluded.expires_at)`, a.GroupID, a.ID, EntryWritten)
	if err != nil {
		return err
	}
	var old string
	if err = tx.QueryRowContext(ctx, "SELECT expected_ips FROM dns_watches WHERE entry_group_id=?", a.GroupID).Scan(&old); err != nil {
		return err
	}
	if old != watchJSON(ips) {
		if err = resetEntryWatchPaths(ctx, tx, a.GroupID); err != nil {
			return err
		}
	}
	// 未确认时继续采样已确认集合，记录级显示与地址传播独立。
	_, err = tx.ExecContext(ctx, `UPDATE dns_watches SET expected_ips=?,expected_version=?,propagation_status='传播中',observation_deadline=?,next_sample_at=? WHERE entry_group_id=? AND expected_version=?`, watchJSON(ips), a.Version, now.Add(min(time.Hour, max(10*time.Minute, time.Duration(a.OldTTL)*2*time.Second))).UnixNano(), now.UnixNano(), a.GroupID, a.Version)
	return err
}

// 启用以任一路径出现为证据；暂停必须所有当前路径确认不再返回。
// 同地址还有其他启用记录时，暂停的服务商确认即完成，明确展示原因。
func (p *Panel) reconcileAllPropagationTx(ctx context.Context, tx *sql.Tx, checkers map[string]string) error {
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.entry_group_id,w.expected_version,w.observation_deadline,s.current_action_id FROM dns_watches w JOIN entry_groups g ON g.id=w.entry_group_id JOIN entry_publications s ON s.group_id=g.id WHERE g.publication_mode='all' AND w.propagation_status='传播中'`)
	if err != nil {
		return err
	}
	type watch struct {
		id, g, a string
		v        int64
		deadline sql.NullInt64
	}
	var watches []watch
	for rows.Next() {
		var w watch
		if err = rows.Scan(&w.id, &w.g, &w.v, &w.deadline, &w.a); err != nil {
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
	now := p.deps.Clock.Now()
	for _, w := range watches {
		paths, e := tx.QueryContext(ctx, `SELECT node_id,ips,status,sampled_at FROM dns_watch_paths WHERE watch_id=? AND expected_version=? AND active=1`, w.id, w.v)
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		allFresh := true
		count := 0
		for paths.Next() {
			var n, encoded, status string
			var at sql.NullInt64
			if err = paths.Scan(&n, &encoded, &status, &at); err != nil {
				break
			}
			if n != "" && checkers[n] == "" {
				continue
			}
			count++
			fresh := at.Valid && now.UnixNano() >= at.Int64 && now.Sub(time.Unix(0, at.Int64)) <= time.Minute && status != "采样失败"
			allFresh = allFresh && fresh
			if fresh {
				var ips []string
				if err = json.Unmarshal([]byte(encoded), &ips); err != nil {
					break
				}
				for _, ip := range ips {
					seen[ip] = true
				}
			}
		}
		if err == nil {
			err = paths.Err()
		}
		paths.Close()
		if err != nil {
			return err
		}
		allFresh = allFresh && count > 0
		records, e := loadActionRecords(ctx, tx, w.a)
		if e != nil {
			return e
		}
		var encoded string
		if err = tx.QueryRowContext(ctx, "SELECT expected_ips FROM dns_watches WHERE id=?", w.id).Scan(&encoded); err != nil {
			return err
		}
		var enabled []string
		if err = json.Unmarshal([]byte(encoded), &enabled); err != nil {
			return err
		}
		complete := len(enabled) > 0
		for _, ip := range enabled {
			complete = complete && seen[ip]
		}
		for _, r := range records {
			status, note := "传播中", ""
			done := false
			confirmed := r.Stage == EntryWritten
			if r.Stage == EntryFailed {
				if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_records WHERE id=? AND binding_generation=? AND current_enabled=? AND current_value=?)`, r.ManagedRecordID, r.BindingGeneration, r.NewEnabled, r.NewValue).Scan(&confirmed); err != nil {
					return err
				}
				if confirmed {
					note = "服务商人工修复已核对"
				}
			}
			if confirmed {
				if r.NewEnabled {
					done = seen[r.NewValue]
				} else if slices.Contains(enabled, r.NewValue) {
					done = true
					note = "该地址仍由其他成员发布"
				} else {
					done = allFresh && !seen[r.NewValue]
				}
			}
			if done {
				status = "已生效"
			} else if w.deadline.Valid && now.UnixNano() >= w.deadline.Int64 {
				status = "传播未确认"
			}
			complete = complete && done
			if _, err = tx.ExecContext(ctx, "UPDATE entry_action_records SET propagation_status=?,propagation_note=? WHERE action_id=? AND managed_record_id=?", status, note, w.a, r.ManagedRecordID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET propagation_status=? WHERE group_id=? AND node_id=(SELECT node_id FROM managed_records WHERE id=?)`, status, w.g, r.ManagedRecordID); err != nil {
				return err
			}
		}
		status := ""
		if complete {
			status = "已生效"
		} else if w.deadline.Valid && now.UnixNano() >= w.deadline.Int64 {
			status = "传播未确认"
		}
		if status != "" {
			if _, err = tx.ExecContext(ctx, "UPDATE dns_watches SET propagation_status=? WHERE id=? AND expected_version=?", status, w.id, w.v); err != nil {
				return err
			}
		}
	}
	return nil
}
