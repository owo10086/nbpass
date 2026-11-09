package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"nbpass/internal/controlpb"
	"nbpass/internal/selfcheck"
	"time"
)

func rotationRecheckStatus(a *rotationAction) string {
	if a == nil {
		return ""
	}
	return a.RecheckStatus
}

func (p *Panel) validateRotationRecheck(ctx context.Context, a *rotationAction, result *controlpb.RotationProgress) (string, []byte, error) {
	return p.validateRotationRecheckWith(ctx, p.db, a, result)
}
func (p *Panel) validateRotationRecheckWith(ctx context.Context, q recoveryQuerier, a *rotationAction, result *controlpb.RotationProgress) (string, []byte, error) {
	if result.State != "changed" {
		if len(result.RecheckRounds) > 0 || result.RecheckComplete {
			return "", nil, errors.New("地址未变不能提交复检")
		}
		return "", []byte("[]"), nil
	}
	var configBytes []byte
	if err := q.QueryRowContext(ctx, "SELECT recheck_config FROM rotation_actions WHERE id=?", a.ID).Scan(&configBytes); err != nil {
		return "", nil, err
	}
	if len(configBytes) == 0 {
		return "pending", []byte("[]"), nil
	} // 旧动作不虚构复检结果。
	if session := p.controls[a.NodeID]; session != nil && !session.rotationRecheckCapable && len(result.RecheckRounds) == 0 {
		data, _ := json.Marshal(a.RecheckRounds)
		return "pending", data, nil
	}
	var config selfcheck.Config
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return "", nil, err
	}
	if len(result.RecheckRounds) > 3 || len(result.RecheckRounds) < len(a.RecheckRounds) {
		return "", nil, errors.New("复检轮数无效")
	}
	rounds := make([]selfcheck.Round, 0, len(result.RecheckRounds))
	normal := false
	unknown := false
	for i, data := range result.RecheckRounds {
		if len(data) > 16<<10 {
			return "", nil, errors.New("复检证据过大")
		}
		var r selfcheck.Round
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, err
		}
		if err := r.Validate(p.deps.Clock.Now()); err != nil {
			return "", nil, err
		}
		if r.ConfigKey != selfcheck.ConfigKey(config) || r.Sequence != uint64(i+1) || normal {
			return "", nil, errors.New("复检配置、序号或终态无效")
		}
		if i < len(a.RecheckRounds) {
			old, _ := json.Marshal(a.RecheckRounds[i])
			current, _ := json.Marshal(r)
			if string(old) != string(current) {
				return "", nil, errors.New("不能覆盖已存复检证据")
			}
		}
		normal = normal || r.Result == "normal"
		unknown = unknown || r.Result == "unknown"
		rounds = append(rounds, r)
	}
	status := "checking"
	complete := normal || len(rounds) == 3
	if result.RecheckComplete != complete {
		return "", nil, errors.New("复检完成标记无效")
	}
	if normal {
		status = "recovered"
	} else if complete && unknown {
		status = "pending"
	} else if complete {
		status = "no_improvement"
	}
	data, _ := json.Marshal(rounds)
	return status, data, nil
}
func (p *Panel) finishRotationRecheck(ctx context.Context, tx *sql.Tx, a *rotationAction, status string, data []byte) error {
	if status == "" || status == "checking" || status == a.RecheckStatus {
		return nil
	}
	var rounds []selfcheck.Round
	if err := json.Unmarshal(data, &rounds); err != nil {
		return err
	}
	message := "新 IP 三轮复检含未知，待确认，不累计无改善"
	switch status {
	case "recovered":
		message = "新 IP 自测复检正常，已恢复，解除无改善暂停"
		if len(rounds) > 0 {
			if err := p.rotationNormalEvidence(ctx, tx, a.NodeID, a.ID, rounds[len(rounds)-1].StartedAt); err != nil {
				return err
			}
		}
	case "no_improvement":
		message = "新 IP 三轮复检均异常，记一次无改善"
		var normalAt int64
		err := tx.QueryRowContext(ctx, "SELECT MAX(last_normal_at,last_reset_at) FROM rotation_policy WHERE node_id=?", a.NodeID).Scan(&normalAt)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if len(rounds) > 0 && normalAt >= rounds[len(rounds)-1].FinishedAt.UnixNano() {
			return p.rotationNotice(ctx, tx, a.NodeID, a.ID, status, "历史新 IP 复检异常，之后已有正常自测，不推进暂停")
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO rotation_policy(node_id,no_improvement) VALUES(?,1) ON CONFLICT(node_id) DO UPDATE SET no_improvement=no_improvement+1,paused=(no_improvement+1>=2)`, a.NodeID); err != nil {
			return err
		}
		var paused bool
		if err := tx.QueryRowContext(ctx, "SELECT paused FROM rotation_policy WHERE node_id=?", a.NodeID).Scan(&paused); err != nil {
			return err
		}
		if paused {
			if err := p.rotationNotice(ctx, tx, a.NodeID, a.ID, "paused", "连续两次换 IP 无改善，自动换 IP 已暂停"); err != nil {
				return err
			}
		}
	}
	return p.rotationNotice(ctx, tx, a.NodeID, a.ID, status, message)
}
func (p *Panel) rotationDNSActions(ctx context.Context, tx *sql.Tx, a *rotationAction, newIP string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_id,desired_version FROM entry_publications WHERE active_node_id=? AND expected_value=?`, a.NodeID, a.OldIP)
	if err != nil {
		return nil, err
	}
	var requests []EntryActionRequest
	for rows.Next() {
		r := EntryActionRequest{NodeID: a.NodeID, IPv4: newIP, Kind: "address_update"}
		if err = rows.Scan(&r.GroupID, &r.ExpectedVersion); err != nil {
			break
		}
		requests = append(requests, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	var groups []string
	for _, r := range requests {
		allowed, e := recoveryGroupAllowed(ctx, tx, r.GroupID)
		if e != nil {
			return nil, e
		}
		if !allowed {
			continue
		}
		if _, err = p.submitEntryActionTx(ctx, tx, r); err != nil {
			return nil, err
		}
		groups = append(groups, r.GroupID)
	}
	return groups, nil
}

// 原始发生时间参与保护：独立控制流可乱序补报，接收顺序不能解除较晚的暂停。
func (p *Panel) rotationNormalEvidence(ctx context.Context, tx *sql.Tx, nodeID, roundID string, started time.Time) error {
	var data []byte
	err := tx.QueryRowContext(ctx, `SELECT recheck_rounds FROM rotation_actions WHERE node_id=? AND recheck_status='no_improvement' ORDER BY created_at DESC,rowid DESC LIMIT 1`, nodeID).Scan(&data)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	var rounds []selfcheck.Round
	if len(data) > 0 {
		if err = json.Unmarshal(data, &rounds); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rotation_policy(node_id,last_normal_at) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET last_normal_at=MAX(last_normal_at,excluded.last_normal_at)`, nodeID, started.UnixNano())
	if err != nil {
		return err
	}
	if len(rounds) > 0 && started.Before(rounds[len(rounds)-1].FinishedAt) {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE rotation_policy SET no_improvement=0,paused=0 WHERE node_id=? AND (no_improvement>0 OR paused=1)`, nodeID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n > 0 {
		err = p.rotationNotice(ctx, tx, nodeID, roundID, "resumed", "后续正常自测已解除无改善暂停，已用额度保留")
	}
	return err
}
