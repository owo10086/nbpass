package panel

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

type recoveryQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// acceptRecovery 只在活性报告路径调用；控制锁与事务保护高水位和额度的共同提交。
func (p *Panel) acceptRecovery(ctx context.Context, id string, report *controlpb.RecoveryReport) error {
	if report == nil {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var water sql.NullInt64
	var desired uint64
	err = tx.QueryRowContext(ctx, `SELECT r.high_water,n.desired_revision FROM recovery_nodes r JOIN nodes n ON n.id=r.node_id WHERE r.node_id=?`, id).Scan(&water, &desired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if report.HighWater >= math.MaxInt64-1 || len(report.Actions) > 128 || report.HistorySince < 0 || report.HistorySince > report.ObservedAt || report.ObservedAt > p.deps.Clock.Now().Add(5*time.Minute).UnixNano() || report.ObservedAt < 0 {
		return errors.New("恢复报告无效")
	}
	// 首次核对强制换一个新版本；之后仅真实超前的水位才再次抬高。
	if !water.Valid || report.HighWater > desired {
		if err = p.raiseRecoveryRevision(ctx, tx, id, max(desired, report.HighWater)+1); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE recovery_nodes SET high_water=MAX(COALESCE(high_water,0),?),history_since=CASE WHEN ?=0 THEN 0 WHEN history_until<? THEN ? WHEN history_since=0 THEN ? ELSE MIN(history_since,?) END,history_until=? WHERE node_id=?`, report.HighWater, report.HistorySince, report.HistorySince, report.HistorySince, report.HistorySince, report.HistorySince, report.ObservedAt, id)
	if err != nil {
		return err
	}
	actions := append([]*controlpb.RecoveryRotation(nil), report.Actions...)
	if report.Current != nil {
		hasTimestamp := report.Current.AttemptedAt > 0
		if !hasTimestamp {
			err := tx.QueryRowContext(ctx, "SELECT attempted_at FROM recovery_rotation_attempts WHERE action_id=? AND node_id=?", report.Current.ActionId, id).Scan(&report.Current.AttemptedAt)
			if errors.Is(err, sql.ErrNoRows) {
				err = tx.QueryRowContext(ctx, "SELECT created_at FROM rotation_actions WHERE id=? AND node_id=?", report.Current.ActionId, id).Scan(&report.Current.AttemptedAt)
			}
			if errors.Is(err, sql.ErrNoRows) {
				report.Current.AttemptedAt = report.ObservedAt
			} else if err != nil {
				return err
			}
		}
		actions = append(actions, report.Current)
		// 节点只在前一动作终结后接受后续动作；已进入较新动作的旧待办只封存，不重放。
		if hasTimestamp {
			if _, err = tx.ExecContext(ctx, `UPDATE rotation_actions SET state='released',pending_operation='',observation_complete=1,reason='恢复核对：节点已进入后续动作，旧待办封存；原结果未知，额度保留' WHERE node_id=? AND id<>? AND created_at<? AND state NOT IN ('changed','cancelled','released')`, id, report.Current.ActionId, report.Current.AttemptedAt); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE recovery_rotation_attempts SET state='released' WHERE node_id=? AND action_id<>? AND attempted_at<? AND state NOT IN ('changed','cancelled','released')`, id, report.Current.ActionId, report.Current.AttemptedAt); err != nil {
				return err
			}
		}
	}
	sort.SliceStable(actions, func(i, j int) bool {
		return actions[i] != nil && actions[j] != nil && actions[i].AttemptedAt < actions[j].AttemptedAt
	})
	seen := map[string]bool{}
	for _, a := range actions {
		if a == nil || a.ActionId == "" || len(a.ActionId) > 128 || seen[a.ActionId] || a.AttemptedAt <= 0 || a.AttemptedAt > report.ObservedAt {
			return errors.New("恢复动作证据无效")
		}
		switch a.State {
		case "started", "observing", "changed", "unconfirmed", "released", "cancelled":
		default:
			return errors.New("恢复动作状态无效")
		}
		seen[a.ActionId] = true
		if err = p.importRecoveryRotation(ctx, tx, id, a); err != nil {
			return err
		}
		// 相同动作不能属于另一个节点；不允许补报改写原始尝试时间。
		var owner string
		var at int64
		e := tx.QueryRowContext(ctx, "SELECT node_id,attempted_at FROM recovery_rotation_attempts WHERE action_id=?", a.ActionId).Scan(&owner, &at)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil && (owner != id || at != a.AttemptedAt) {
			return errors.New("恢复动作身份或时间不匹配")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO recovery_rotation_attempts(action_id,node_id,attempted_at,state) VALUES(?,?,?,?) ON CONFLICT(action_id) DO UPDATE SET state=excluded.state`, a.ActionId, id, a.AttemptedAt, a.State); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (p *Panel) raiseRecoveryRevision(ctx context.Context, tx *sql.Tx, id string, revision uint64) error {
	var data []byte
	var encrypted bool
	snapshot := &controlpb.ConfigSnapshot{}
	err := tx.QueryRowContext(ctx, "SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", id).Scan(&data, &encrypted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(data) > 0 {
		if err = p.decodeSnapshot(id, data, encrypted, snapshot); err != nil {
			return err
		}
	}
	snapshot.Revision = revision
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	data, err = proto.MarshalOptions{Deterministic: true}.Marshal(snapshot)
	if err != nil {
		return err
	}
	data, err = p.secrets.(*sealedSecrets).seal("node-snapshot:"+id, data)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_snapshots(node_id,snapshot,digest,encrypted) VALUES(?,?,?,1) ON CONFLICT(node_id) DO UPDATE SET snapshot=excluded.snapshot,digest=excluded.digest,encrypted=1`, id, data, snapshot.Digest)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE nodes SET desired_revision=?,applied_revision=0,applied_digest='',attempted_revision=0,apply_error='' WHERE id=?`, revision, id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE recovery_nodes SET verified=0 WHERE node_id=?", id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE recovery_groups SET verified=0 WHERE group_id IN (SELECT group_id FROM entry_group_members WHERE node_id=?)", id)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE recovery_records SET verified=0 WHERE managed_record_id IN (SELECT id FROM managed_records WHERE node_id=? OR group_id IN (SELECT group_id FROM entry_group_members WHERE node_id=?))`, id, id)
	}
	return err
}

func recoveryGroupAllowed(ctx context.Context, q recoveryQuerier, gid string) (bool, error) {
	var active, verified, unknown int
	err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM recovery_state),COALESCE((SELECT verified FROM recovery_groups WHERE group_id=?),1),(SELECT count(*) FROM entry_group_members m JOIN recovery_nodes r ON r.node_id=m.node_id WHERE m.group_id=? AND (r.verified=0 OR r.high_water IS NULL))`, gid, gid).Scan(&active, &verified, &unknown)
	if err == nil && active > 0 {
		var records int
		err = q.QueryRowContext(ctx, `SELECT count(*) FROM managed_records m LEFT JOIN recovery_records r ON r.managed_record_id=m.id WHERE m.group_id=? AND (coalesce(r.verified,0)=0 OR r.binding_generation<>m.binding_generation)`, gid).Scan(&records)
		unknown += records
	}
	return active == 0 || verified == 1 && unknown == 0, err
}

func (p *Panel) recoveryNodeAllowed(ctx context.Context, q recoveryQuerier, id string, automatic bool) (bool, error) {
	var restored, created, since int64
	var verified bool
	err := q.QueryRowContext(ctx, `SELECT s.restored_at,n.created_at,r.history_since,r.verified FROM recovery_nodes r JOIN nodes n ON n.id=r.node_id CROSS JOIN recovery_state s WHERE r.node_id=?`, id).Scan(&restored, &created, &since, &verified)
	tracked := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if tracked && !verified {
		return false, nil
	}
	var groups, pending int
	err = q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM entry_group_members m LEFT JOIN recovery_groups g ON g.group_id=m.group_id WHERE m.node_id=? AND (COALESCE(g.verified,1)=0 OR EXISTS(SELECT 1 FROM entry_group_members m2 JOIN recovery_nodes r2 ON r2.node_id=m2.node_id WHERE m2.group_id=m.group_id AND (r2.verified=0 OR r2.high_water IS NULL)))),(SELECT count(*) FROM recovery_rotation_attempts WHERE node_id=? AND state NOT IN ('changed','released','cancelled'))`, id, id).Scan(&groups, &pending)
	if err != nil || groups > 0 || pending > 0 {
		return false, err
	}
	var records int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM managed_records m LEFT JOIN recovery_records r ON r.managed_record_id=m.id WHERE m.node_id=? AND EXISTS(SELECT 1 FROM recovery_state) AND (coalesce(r.verified,0)=0 OR r.binding_generation<>m.binding_generation)`, id).Scan(&records); err != nil || records > 0 {
		return false, err
	}
	if tracked && automatic && p.deps.Clock.Now().Before(time.Unix(0, restored).Add(24*time.Hour)) {
		needed := max(created, p.deps.Clock.Now().Add(-24*time.Hour).UnixNano())
		if since == 0 || since > needed {
			return false, nil
		}
	}
	return true, nil
}
