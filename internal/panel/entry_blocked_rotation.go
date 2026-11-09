package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

// 迁移34：策略③只关联既有切换、节点换 IP 和期望版本，不另建执行器。
const entryBlockedRotationMigration = `ALTER TABLE entry_blocked_decisions ADD COLUMN policy TEXT NOT NULL DEFAULT 'switch_entry';
CREATE TABLE entry_blocked_rotations (
 decision_id TEXT PRIMARY KEY REFERENCES entry_blocked_decisions(id) ON DELETE CASCADE,
 expected_version INTEGER NOT NULL, rotation_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE entry_blocked_rotation_notices (
 decision_id TEXT NOT NULL REFERENCES entry_blocked_rotations(decision_id) ON DELETE CASCADE,
 rotation_id TEXT NOT NULL, phase TEXT NOT NULL,
 PRIMARY KEY(decision_id,rotation_id,phase)
);`

const strategyThreeFlowFrom = ` FROM entry_blocked_rotations f
 JOIN entry_blocked_decisions d ON d.id=f.decision_id
 JOIN entry_groups g ON g.id=d.group_id
 JOIN entry_publications s ON s.group_id=g.id
 LEFT JOIN rotation_actions a ON a.id=f.rotation_id
 WHERE g.blocked_policy='switch_then_rotate' AND s.desired_version=f.expected_version
 AND EXISTS(SELECT 1 FROM entry_group_members m WHERE m.group_id=g.id AND m.node_id=d.node_id)
 AND d.created_at>=COALESCE((SELECT MAX(restored_at) FROM recovery_state),0)`

// DNS 写入确认后才签发许可；三轮无改善可按共享额度重试，未知和冻结不盲重试。
const strategyThreeSwitchConfirmed = ` AND s.written_at IS NOT NULL AND (d.action_id='' OR EXISTS(SELECT 1 FROM entry_actions e WHERE e.id=d.action_id AND e.stage='服务商已写入' AND e.written_at IS NOT NULL))`
const strategyThreeCanRotate = ` AND (f.rotation_id='' OR (a.state='changed' AND a.recheck_status='no_improvement') OR a.state='released')`
const strategyThreeRotationPurpose = `EXISTS(SELECT 1` + strategyThreeFlowFrom + strategyThreeSwitchConfirmed + strategyThreeCanRotate + ` AND d.node_id=c.node_id)`

// 调用方已持 controlMu，授权事务同时关联动作，崩溃后不会丢失关联或重复申请。
func linkStrategyThreeRotation(ctx context.Context, tx *sql.Tx, nodeID, actionID string, manual bool) error {
	suffix := ""
	if !manual {
		suffix = strategyThreeSwitchConfirmed + strategyThreeCanRotate
	}
	_, err := tx.ExecContext(ctx, `UPDATE entry_blocked_rotations SET rotation_id=? WHERE decision_id IN
 (SELECT f.decision_id`+strategyThreeFlowFrom+suffix+` AND d.node_id=?)`, actionID, nodeID)
	return err
}

// 同活动入口的地址更新和偏差写回延续恢复门槛；手动切换或真实切换另起期望。
func carryStrategyThreeVersion(ctx context.Context, tx *sql.Tx, req EntryActionRequest, version int64) error {
	if req.Kind != "writeback" && req.Kind != "address_update" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE entry_blocked_rotations SET expected_version=?
 WHERE expected_version=? AND decision_id IN (SELECT id FROM entry_blocked_decisions WHERE group_id=?)
 AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND desired_version=? AND active_node_id=?)`, version, req.ExpectedVersion, req.GroupID, req.GroupID, req.ExpectedVersion, req.NodeID)
	return err
}

// 回切资格与候选窗口分开：周期自测成功不能替代本次新 IP 复检恢复。
// 手动动作提高期望版本后，旧工作流自然失效；显式恢复仍走普通回切规则。
func strategyThreeFailbackAllowed(ctx context.Context, q recoveryQuerier, group string, version int64) (bool, error) {
	var blocked bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+strategyThreeFlowFrom+`
 AND d.group_id=? AND f.expected_version=? AND d.outcome='submitted'
 AND NOT COALESCE(a.state='changed' AND a.recheck_status='recovered'
 AND a.new_ip=(SELECT public_ipv4 FROM node_regions WHERE node_id=d.node_id),0))`, group, version).Scan(&blocked)
	return !blocked, err
}

type strategyThreeFlow struct{ id, group, node, rotation string }

func (p *Panel) strategyThreeFlows(ctx context.Context, suffix string, args ...any) ([]strategyThreeFlow, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT f.decision_id,d.group_id,d.node_id,f.rotation_id`+strategyThreeFlowFrom+strategyThreeSwitchConfirmed+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var flows []strategyThreeFlow
	for rows.Next() {
		var f strategyThreeFlow
		if err = rows.Scan(&f.id, &f.group, &f.node, &f.rotation); err != nil {
			return nil, err
		}
		flows = append(flows, f)
	}
	return flows, rows.Err()
}

func (p *Panel) strategyThreeNotice(ctx context.Context, f strategyThreeFlow, phase, message string) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entry_blocked_rotation_notices(decision_id,rotation_id,phase) VALUES(?,?,?)`, f.id, f.rotation, phase)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	message = fmt.Sprintf("入口组 %s（策略③）：%s；已切离时留在备用入口，无备用时保留当前解析", f.group, message)
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(f.id+":"+f.rotation+":"+phase)))
	if err = p.rotationNotice(ctx, tx, f.node, key, "entry_waiting", message); err != nil {
		return err
	}
	return tx.Commit()
}

// 授权失败只记录本组影响；通知待办沿用票30，不在授权中访问外部服务。
func (p *Panel) notifyStrategyThreeRestriction(ctx context.Context, nodeID string, cause error) error {
	var e *forwardingError
	if !errors.As(cause, &e) || e.status != 409 {
		return nil
	}
	flows, err := p.strategyThreeFlows(ctx, strategyThreeCanRotate+` AND d.node_id=?`, nodeID)
	if err != nil {
		return err
	}
	for _, f := range flows {
		if err = p.strategyThreeNotice(ctx, f, "restricted", e.Error()); err != nil {
			return err
		}
	}
	return nil
}

// 只读取独立的换 IP 结果，不推进活性、配置或原始自测状态。
func (p *Panel) observeStrategyThreeRotations(ctx context.Context) error {
	flows, err := p.strategyThreeFlows(ctx, "")
	if err != nil {
		return err
	}
	for _, f := range flows {
		if f.rotation == "" {
			continue
		}
		var state, status string
		if err = p.db.QueryRowContext(ctx, "SELECT state,recheck_status FROM rotation_actions WHERE id=?", f.rotation).Scan(&state, &status); err != nil {
			return err
		}
		phase, message := "", ""
		switch {
		case state == "unconfirmed":
			phase, message = "frozen", "换 IP 结果未确认，节点已冻结，请核对或人工处理"
		case state == "cancelled":
			phase, message = "cancelled", "换 IP 已取消，未完成恢复，请人工处理"
		case status == "pending":
			phase, message = "pending", "新 IP 复检待确认，不累计无改善，不自动切回"
		case status == "no_improvement":
			phase, message = "no_improvement", "新 IP 三轮复检无改善，继续受节点共享额度与无改善暂停限制"
		}
		if phase != "" {
			if err = p.strategyThreeNotice(ctx, f, phase, message); err != nil {
				return err
			}
		}
	}
	return nil
}
