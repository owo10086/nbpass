package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nbpass/internal/checker"
	"nbpass/internal/eventlog"
	"nbpass/internal/publicip"
)

type offlineEntry struct {
	group, node, ip   string
	version, incident int64
	port              int
	revision          uint64
	// 全部发布冻结成员记录身份；主备仍按组期望版本核对。
	record  string
	binding int64
}

// 与手动操作沿用 entryMu → controlMu；持锁期间只读写本地状态，不调用外部服务。
func (p *Panel) checkOfflineEntries(ctx context.Context) error {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	now := p.deps.Clock.Now()
	rows, err := p.db.QueryContext(ctx, `SELECT s.group_id,s.active_node_id,s.expected_value,s.desired_version,n.check_port,n.desired_revision,n.last_seen_at,n.disconnected_at,n.maintenance_until,COALESCE(r.public_ipv4,'')
 FROM entry_publications s JOIN nodes n ON n.id=s.active_node_id LEFT JOIN node_regions r ON r.node_id=n.id`)
	if err != nil {
		return err
	}
	var entries []offlineEntry
	for rows.Next() {
		var e offlineEntry
		var last, disconnected, maintenance sql.NullInt64
		var currentIP string
		if err = rows.Scan(&e.group, &e.node, &e.ip, &e.version, &e.port, &e.revision, &last, &disconnected, &maintenance, &currentIP); err != nil {
			break
		}
		if p.presence(e.node, last, disconnected, maintenance, now).status == "offline" && currentIP == e.ip {
			entries = append(entries, e)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, err = publicip.Parse(e.ip); err != nil {
			continue
		}
		allowed, err := recoveryGroupAllowed(ctx, p.db, e.group)
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		if err = p.observeOffline(ctx, e.node, now); err != nil {
			return err
		}
		if err = p.db.QueryRowContext(ctx, "SELECT id FROM offline_incidents WHERE node_id=? AND recovered_at IS NULL", e.node).Scan(&e.incident); err != nil {
			return err
		}
		if err = p.advanceOfflineEntry(ctx, e, now); err != nil {
			return err
		}
	}
	return nil
}

func (p *Panel) advanceOfflineEntry(ctx context.Context, e offlineEntry, now time.Time) error {
	var id, outcome string
	var issued, deadline int64
	err := p.db.QueryRowContext(ctx, `SELECT id,outcome,issued_at,deadline FROM entry_offline_checks WHERE group_id=? AND desired_version=? AND incident_id=? ORDER BY issued_at DESC LIMIT 1`, e.group, e.version, e.incident).Scan(&id, &outcome, &issued, &deadline)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		switch outcome {
		case "pending":
			if now.UnixNano() >= deadline {
				tx, err := p.db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if err = p.finishOfflineCheck(ctx, tx, id, true); err != nil {
					return err
				}
				return tx.Commit()
			}
			return nil
		case "unreachable":
			// 确认窗口过期不能继续用于提交；重新请求本轮检测。
			if now.UnixNano() < deadline {
				return p.submitOfflineSwitch(ctx, e, id)
			}
		case "submitted":
			return nil
		}
		if now.UnixNano() < issued+int64(60*time.Second) {
			return nil
		}
	}
	return p.scheduleOfflineCheck(ctx, e, now)
}

func (p *Panel) scheduleOfflineCheck(ctx context.Context, e offlineEntry, now time.Time) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, _, err := p.randomCredential()
	if err != nil {
		return err
	}
	deadline := now.Add(60 * time.Second)
	if _, err = tx.ExecContext(ctx, `INSERT INTO entry_offline_checks(id,group_id,node_id,incident_id,desired_version,target_ip,target_port,config_revision,issued_at,deadline,managed_record_id,binding_generation) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, e.group, e.node, e.incident, e.version, e.ip, e.port, e.revision, now.UnixNano(), deadline.UnixNano(), e.record, e.binding); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT n.id FROM nodes n JOIN node_regions r ON r.node_id=n.id WHERE n.id<>? AND CASE WHEN r.override_name IS NULL THEN r.auto_mainland ELSE r.override_mainland END=1 AND r.checker_disabled=0 AND (n.maintenance_until IS NULL OR n.maintenance_until<=?) AND n.last_seen_at>? ORDER BY n.id`, e.node, now.UnixNano(), now.Add(-offlineWindow).UnixNano())
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var cid string
		if err = rows.Scan(&cid); err != nil {
			break
		}
		if s := p.controls[cid]; s != nil {
			select {
			case <-s.done:
			default:
				ids = append(ids, cid)
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	settings, err := loadSelfCheckSettings(ctx, tx)
	if err != nil {
		return err
	}
	for _, cid := range ids {
		sum := sha256.Sum256([]byte("offline:" + id + ":" + cid))
		task := checker.Task{ID: hex.EncodeToString(sum[:16]), RoundID: id, TargetID: e.node, CheckerID: cid, TargetIP: e.ip, TargetPort: e.port, ConfigRevision: e.revision, ControlHost: settings.Sites[0], IssuedAt: now, Deadline: deadline}
		data, _ := json.Marshal(task)
		if _, err = tx.ExecContext(ctx, "INSERT INTO entry_offline_tasks(id,check_id,checker_id,task) VALUES(?,?,?,?)", task.ID, id, cid, data); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE entry_offline_checks SET outcome='no_checkers' WHERE id=?", id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		p.wakeCheckerSessions()
	}
	return err
}

// 只更新离线确认，不写自测轮次、连续异常计数、心跳或配置确认。
func (p *Panel) finishOfflineCheck(ctx context.Context, tx *sql.Tx, id string, expired bool) error {
	var outcome string
	if err := tx.QueryRowContext(ctx, "SELECT outcome FROM entry_offline_checks WHERE id=?", id).Scan(&outcome); err != nil || outcome != "pending" {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT result FROM entry_offline_tasks WHERE check_id=?", id)
	if err != nil {
		return err
	}
	total, received, valid := 0, 0, 0
	success, executionError := false, false
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			break
		}
		total++
		if data == nil {
			continue
		}
		received++
		var r checker.Result
		if err = json.Unmarshal(data, &r); err != nil {
			break
		}
		if r.ExecutionError != "" {
			executionError = true
			continue
		}
		if r.Control.Connected && r.Target != nil {
			valid++
			success = success || r.Target.Connected
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if !expired && !success && received < total {
		return nil
	}
	outcome = "unknown"
	if success {
		outcome = "reachable"
	} else if total == received && valid > 0 && !executionError {
		outcome = "unreachable"
	}
	_, err = tx.ExecContext(ctx, "UPDATE entry_offline_checks SET outcome=? WHERE id=? AND outcome='pending'", outcome, id)
	return err
}

func (p *Panel) submitOfflineSwitch(ctx context.Context, e offlineEntry, id string) error {
	// 不使用页面缓存；控制锁保护这次候选检查到动作提交之间的报告和配置。
	states, err := p.checkEntryCandidates(ctx)
	if err != nil {
		return err
	}
	rows, err := p.db.QueryContext(ctx, "SELECT node_id FROM entry_group_members WHERE group_id=? ORDER BY position", e.group)
	if err != nil {
		return err
	}
	var members []string
	for rows.Next() {
		var member string
		if err = rows.Scan(&member); err != nil {
			break
		}
		members = append(members, member)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	var candidate, ip string
	for _, member := range members {
		state := states[[2]string{e.group, member}]
		if member != e.node && state.Candidate && state.DomesticEvidence != nil {
			candidate, ip = member, state.DomesticEvidence.PublicIPv4
			break
		}
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 维护API独立于控制锁；用提交事务再次核对，避免检查后维护/失效的竞态。
	var last, disconnected, maintenance, recovered sql.NullInt64
	var currentIP, active, outcome, expectedIP string
	var port int
	var revision uint64
	var version, deadline int64
	err = tx.QueryRowContext(ctx, `SELECT n.last_seen_at,n.disconnected_at,n.maintenance_until,r.public_ipv4,n.check_port,n.desired_revision,s.desired_version,s.active_node_id,s.expected_value,c.outcome,c.deadline,i.recovered_at
 FROM entry_offline_checks c JOIN nodes n ON n.id=c.node_id JOIN node_regions r ON r.node_id=n.id JOIN entry_publications s ON s.group_id=c.group_id JOIN offline_incidents i ON i.id=c.incident_id WHERE c.id=?`, id).Scan(&last, &disconnected, &maintenance, &currentIP, &port, &revision, &version, &active, &expectedIP, &outcome, &deadline, &recovered)
	if err != nil {
		return err
	}
	now := p.deps.Clock.Now()
	if outcome != "unreachable" || recovered.Valid || currentIP != e.ip || expectedIP != e.ip || revision != e.revision || port != e.port || active != e.node || version != e.version || now.UnixNano() >= deadline || p.presence(e.node, last, disconnected, maintenance, now).status != "offline" {
		_, err = tx.ExecContext(ctx, "UPDATE entry_offline_checks SET outcome='invalidated' WHERE id=? AND outcome='unreachable'", id)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if candidate == "" {
		_, err = tx.ExecContext(ctx, "UPDATE entry_offline_checks SET outcome='no_candidate' WHERE id=? AND outcome='unreachable'", id)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	a, err := p.submitEntryActionTx(ctx, tx, EntryActionRequest{GroupID: e.group, NodeID: candidate, IPv4: ip, Kind: "switch", ExpectedVersion: e.version})
	if err != nil {
		return err
	}
	if a.Stage == entryObserved {
		return tx.Commit()
	}
	// 分配动作ID或存储也可能跨过有效期。提交前最后检查时间条件，
	// 失败就回滚整个动作、期望版本和事件，绝不唤醒DNS协调器。
	fresh, err := p.offlineCandidateCurrent(ctx, tx, states[[2]string{e.group, candidate}])
	if err != nil {
		return err
	}
	if !fresh || p.deps.Clock.Now().UnixNano() >= deadline {
		if err = tx.Rollback(); err != nil {
			return err
		}
		outcome := "no_candidate"
		if p.deps.Clock.Now().UnixNano() >= deadline {
			outcome = "invalidated"
		}
		_, err = p.db.ExecContext(ctx, "UPDATE entry_offline_checks SET outcome=? WHERE id=? AND outcome='unreachable'", outcome, id)
		return err
	}
	// 发布版本触发器会先令本轮失效；成功动作与本轮归属在同一事务提交。
	if _, err = tx.ExecContext(ctx, "UPDATE entry_offline_checks SET outcome='submitted',action_id=? WHERE id=?", a.ID, id); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		p.coordinator.wake(e.group)
	}
	return err
}

// 调用者持有控制锁和提交事务；仅时间流逝也会让刚检查成功的候选失效。
func (p *Panel) offlineCandidateCurrent(ctx context.Context, tx *sql.Tx, state entryMemberState) (bool, error) {
	now := p.deps.Clock.Now()
	if !state.Candidate || state.CheckedAt == nil || now.Before(*state.CheckedAt) || now.Sub(*state.CheckedAt) > entryCandidateInterval || state.DomesticEvidence == nil || now.Before(state.DomesticEvidence.ObservedAt) || !now.Before(state.DomesticEvidence.ExpiresAt) {
		return false, nil
	}
	s := p.controls[state.NodeID]
	if s == nil {
		return false, nil
	}
	select {
	case <-s.done:
		return false, nil
	default:
	}
	var eligible bool
	err := tx.QueryRowContext(ctx, `SELECT (n.maintenance_until IS NULL OR n.maintenance_until<=?) AND COALESCE(n.last_seen_at>?,0) AND r.public_ipv4=? FROM nodes n JOIN node_regions r ON r.node_id=n.id WHERE n.id=?`, now.UnixNano(), now.Add(-offlineWindow).UnixNano(), state.DomesticEvidence.PublicIPv4, state.NodeID).Scan(&eligible)
	return eligible, err
}

// 从持久化决策/动作重建通知；队列故障不回滚切换，下一次扫描续投。
func (p *Panel) flushOfflineEntryNotifications(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT c.id,c.group_id,c.node_id,c.incident_id,c.desired_version,c.outcome,c.action_id,COALESCE(a.node_id,'') FROM entry_offline_checks c LEFT JOIN entry_actions a ON a.id=c.action_id WHERE c.notified=0 AND (c.outcome IN ('no_checkers','unknown','no_candidate') OR (c.outcome='submitted' AND a.stage=?)) ORDER BY c.issued_at`, EntryWritten)
	if err != nil {
		return err
	}
	type notice struct {
		id, group, node, outcome, action, target string
		incident, version                        int64
	}
	var notices []notice
	for rows.Next() {
		var n notice
		if err = rows.Scan(&n.id, &n.group, &n.node, &n.incident, &n.version, &n.outcome, &n.action, &n.target); err != nil {
			break
		}
		notices = append(notices, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notices {
		message, kind := "", "entry_failover_waiting"
		switch n.outcome {
		case "no_checkers", "unknown":
			message = "没有可用国内检测点或有效不可达确认，只通知、不切换，保留最后的解析"
		case "no_candidate":
			message = "活动入口离线且检测点确认不可达，但没有就绪候选，停止本次切换并保留最后的解析"
		case "submitted":
			kind = "entry_failover"
			message = fmt.Sprintf("活动入口离线且检测点确认不可达，入口切换已写入 DNSPod：%s → %s（动作 %s）", n.node, n.target, n.action)
		}
		message = fmt.Sprintf("入口组 %s 期望版本 %d：%s", n.group, n.version, message)
		key := fmt.Sprintf("entry-offline:%s:%d:%d", n.group, n.incident, n.version)
		now := p.deps.Clock.Now()
		// 先记事件。通知队列存储故障也不能遮蔽已完成的切换历史。
		if err = p.RecordEvent(ctx, n.node, eventlog.Event{ID: key + ":" + n.outcome, Type: kind, OccurredAt: now, Message: message}); err != nil {
			return err
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// 同一事故、版本和原因只记录/投递一次，后续轮次不刷屏。
		err = queueTelegram(ctx, tx, key, n.node, []string{message}, now)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE entry_offline_checks SET notified=1 WHERE id=?", n.id)
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
