package panel

import (
	"net/http"
	"strconv"

	"nbpass/internal/eventlog"
)

// 迁移 14：追加控制代次和观察完成标记，保留原动作和所有去重序号。
const rotationFreezeMigration = `ALTER TABLE rotation_actions ADD COLUMN control_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rotation_actions ADD COLUMN pending_operation TEXT NOT NULL DEFAULT '';
ALTER TABLE rotation_actions ADD COLUMN observation_complete INTEGER NOT NULL DEFAULT 0;
UPDATE rotation_actions SET observation_complete=1 WHERE state IN ('changed','cancelled') OR (state='unconfirmed' AND attempts=5);
DROP INDEX rotation_node_pending;
CREATE UNIQUE INDEX rotation_node_pending ON rotation_actions(node_id)
 WHERE state NOT IN ('changed','cancelled','released');`

func rotationPending(a *rotationAction) bool {
	return a != nil && (a.RecheckStatus == "checking" || (a.State != "changed" && a.State != "cancelled" && a.State != "released"))
}

func (p *Panel) reconcileRotation(w http.ResponseWriter, r *http.Request) {
	p.controlRotation(w, r, "reconcile")
}

func (p *Panel) unfreezeRotation(w http.ResponseWriter, r *http.Request) {
	p.controlRotation(w, r, "release")
}

// 管理操作绑定当前动作与单调代次。面板提交、节点落盘补报后才解除互斥。
// 离线可排队；命令不携带商家凭据，也不复用已经过期的执行许可。
func (p *Panel) controlRotation(w http.ResponseWriter, r *http.Request, operation string) {
	var input struct {
		ActionID string `json:"action_id"`
	}
	if !decode(w, r, &input) {
		return
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	a, err := p.latestRotation(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 503, "无法读取换 IP 动作")
		return
	}
	if a == nil || a.ID != input.ActionID || a.State != "unconfirmed" || !a.ObservationComplete || a.PendingOperation != "" {
		problem(w, 409, "请等待当前动作观察结束，并核对动作 ID；不能同时处理其他操作")
		return
	}
	if a.ControlSequence >= 1<<62 {
		problem(w, 409, "动作控制代次已达上限")
		return
	}
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "无法保存换 IP 操作")
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `UPDATE rotation_actions SET control_sequence=control_sequence+1,pending_operation=?,updated_at=? WHERE id=?`, operation, now.UnixNano(), a.ID)
	message := "管理员请求重新核对公网 IPv4，不调用换 IP 接口"
	if operation == "release" {
		message = "管理员请求解除结果未确认冻结，保留未知结果及已用额度"
	}
	if err == nil {
		err = insertEvent(r.Context(), tx, a.NodeID, "panel", eventlog.Event{ID: "rotation-control:" + a.ID + ":" + strconv.FormatUint(a.ControlSequence+1, 10), Type: "ip_rotation_" + operation + "_requested", OccurredAt: now, Message: "动作 " + a.ID + "：" + message}, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "无法保存换 IP 操作")
		return
	}
	if session := p.controls[a.NodeID]; session != nil {
		select {
		case session.changed <- struct{}{}:
		default:
		}
	}
	respond(w, 202, map[string]bool{"queued": true})
}
