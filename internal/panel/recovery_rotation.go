package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/publicip"
	"nbpass/internal/rotation"
)

// importRecoveryRotation 将恢复日志接回现有核对/解除接口；不重新签发商家调用许可。
// 恢复专用同序号核对不能放宽正常进度上报的去重规则。
func (p *Panel) importRecoveryRotation(ctx context.Context, tx *sql.Tx, id string, a *controlpb.RecoveryRotation) error {
	progress := a.Progress
	if progress == nil || progress.ActionId != a.ActionId || progress.State != a.State || progress.Sequence == 0 || progress.Sequence > math.MaxInt64 || progress.ControlSequence > math.MaxInt64 || progress.Attempts > 5 {
		return errors.New("恢复动作进度无效")
	}
	if _, ok := rotation.Reasons[progress.Reason]; !ok {
		return errors.New("恢复动作原因无效")
	}
	if ip, e := publicip.Parse(progress.OldIp); e != nil || ip != progress.OldIp {
		return errors.New("恢复动作旧地址无效")
	}
	if progress.State == "changed" {
		if ip, e := publicip.Parse(progress.NewIp); e != nil || ip == progress.OldIp || ip != progress.NewIp {
			return errors.New("恢复动作新地址无效")
		}
	} else if progress.NewIp != "" {
		return errors.New("恢复动作新地址无效")
	}
	var owner, old string
	var sequence, control uint64
	err := tx.QueryRowContext(ctx, "SELECT node_id,old_ip,sequence,control_sequence FROM rotation_actions WHERE id=?", a.ActionId).Scan(&owner, &old, &sequence, &control)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (owner != id || old != progress.OldIp) {
		return errors.New("恢复动作身份不符")
	}
	wasKnown := err == nil
	if err == nil && (sequence > progress.Sequence || control > progress.ControlSequence) {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO rotation_actions(id,node_id,old_ip,new_ip,state,reason,sequence,attempts,unsafe_network,created_at,expires_at,updated_at,secret_name,origin,permit_sent,control_sequence,observation_complete,recheck_status) VALUES(?,?,?,?,?,?,?,?,?,?,0,?,COALESCE((SELECT secret_name FROM rotation_settings WHERE node_id=?),''),'recovered',1,?,?,?)`, a.ActionId, id, progress.OldIp, progress.NewIp, progress.State, rotation.Reasons[progress.Reason], progress.Sequence, progress.Attempts, progress.UnsafeNetwork, a.AttemptedAt, p.deps.Clock.Now().UnixNano(), id, progress.ControlSequence, progress.ObservationComplete, recoveredRecheckStatus(progress))
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE rotation_actions SET new_ip=?,state=?,reason=?,sequence=?,attempts=?,unsafe_network=?,updated_at=?,expires_at=0,permit_sent=1,control_sequence=?,observation_complete=?,pending_operation='' WHERE id=?`, progress.NewIp, progress.State, rotation.Reasons[progress.Reason], progress.Sequence, progress.Attempts, progress.UnsafeNetwork, p.deps.Clock.Now().UnixNano(), progress.ControlSequence, progress.ObservationComplete, a.ActionId)
	}
	if err == nil && wasKnown && progress.State == "changed" && a.AttemptedAt > p.deps.Clock.Now().Add(-24*time.Hour).UnixNano() {
		a := &rotationAction{ID: progress.ActionId, NodeID: id, OldIP: progress.OldIp, NewIP: progress.NewIp, State: progress.State}
		var data []byte
		if err = tx.QueryRowContext(ctx, "SELECT recheck_status,recheck_rounds FROM rotation_actions WHERE id=?", a.ID).Scan(&a.RecheckStatus, &data); err != nil {
			return err
		}
		if err = json.Unmarshal(data, &a.RecheckRounds); err != nil {
			return err
		}
		status, rounds, e := p.validateRotationRecheckWith(ctx, tx, a, progress)
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, "UPDATE rotation_actions SET recheck_status=?,recheck_rounds=? WHERE id=?", status, rounds, a.ID); err != nil {
			return err
		}
		if err = p.finishRotationRecheck(ctx, tx, a, status, rounds); err != nil {
			return err
		}
	}
	if err == nil && wasKnown && progress.State == "changed" && a.AttemptedAt <= p.deps.Clock.Now().Add(-24*time.Hour).UnixNano() {
		_, err = tx.ExecContext(ctx, "UPDATE rotation_actions SET recheck_status='pending' WHERE id=? AND recheck_status='checking'", a.ActionId)
	}
	if err == nil && progress.State == "cancelled" {
		_, err = tx.ExecContext(ctx, "DELETE FROM rotation_quota WHERE action_id=?", a.ActionId)
	}
	return err
}

// 未知签发时的复检配置不能伪造成功；保留当前地址，等待新的正常自测。
func recoveredRecheckStatus(progress *controlpb.RotationProgress) string {
	if progress.State == "changed" {
		return "pending"
	}
	return ""
}
