package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/publicip"
	"nbpass/internal/rotation"
	"nbpass/internal/selfcheck"
)

const rotationMigration = `CREATE TABLE rotation_settings (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id), secret_name TEXT NOT NULL,
 unsafe_network INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE rotation_actions (
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id),
 old_ip TEXT NOT NULL, new_ip TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
 sequence INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0,
 unsafe_network INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 secret_name TEXT NOT NULL
);
CREATE UNIQUE INDEX rotation_node_pending ON rotation_actions(node_id)
 WHERE state NOT IN ('changed','cancelled');`

type rotationAction struct {
	ID                  string            `json:"id"`
	NodeID              string            `json:"node_id"`
	OldIP               string            `json:"old_ip"`
	NewIP               string            `json:"new_ip"`
	State               string            `json:"state"`
	Reason              string            `json:"reason"`
	Sequence            uint64            `json:"sequence"`
	Attempts            uint32            `json:"attempts"`
	UnsafeNetwork       bool              `json:"unsafe_network"`
	CreatedAt           time.Time         `json:"created_at"`
	ExpiresAt           time.Time         `json:"expires_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	RecheckStatus       string            `json:"recheck_status"`
	RecheckRounds       []selfcheck.Round `json:"recheck_rounds"`
	Origin              string            `json:"origin"`
	ControlSequence     uint64            `json:"control_sequence"`
	PendingOperation    string            `json:"pending_operation"`
	ObservationComplete bool              `json:"observation_complete"`
}

func (p *Panel) rotationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{id}/ip-rotation", p.getRotation)
	mux.HandleFunc("PUT /api/nodes/{id}/ip-rotation", p.putRotation)
	mux.HandleFunc("POST /api/nodes/{id}/ip-rotation/actions", p.startRotation)
	mux.HandleFunc("POST /api/nodes/{id}/ip-rotation/reconcile", p.reconcileRotation)
	mux.HandleFunc("POST /api/nodes/{id}/ip-rotation/unfreeze", p.unfreezeRotation)
	p.rotationPolicyRoutes(mux)
}
func (p *Panel) latestRotation(ctx context.Context, id string) (*rotationAction, error) {
	a := &rotationAction{}
	var created, expires, updated int64
	var rounds []byte
	err := p.db.QueryRowContext(ctx, `SELECT id,node_id,old_ip,new_ip,state,reason,sequence,attempts,unsafe_network,created_at,expires_at,updated_at,control_sequence,pending_operation,observation_complete,origin,recheck_status,recheck_rounds FROM rotation_actions WHERE node_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, id).Scan(&a.ID, &a.NodeID, &a.OldIP, &a.NewIP, &a.State, &a.Reason, &a.Sequence, &a.Attempts, &a.UnsafeNetwork, &created, &expires, &updated, &a.ControlSequence, &a.PendingOperation, &a.ObservationComplete, &a.Origin, &a.RecheckStatus, &rounds)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(rounds, &a.RecheckRounds); err != nil {
		return nil, err
	}
	a.CreatedAt = time.Unix(0, created).UTC()
	a.ExpiresAt = time.Unix(0, expires).UTC()
	a.UpdatedAt = time.Unix(0, updated).UTC()
	return a, nil
}
func (p *Panel) getRotation(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	var name string
	var unsafe bool
	err := p.db.QueryRowContext(r.Context(), `SELECT COALESCE(s.secret_name,''),COALESCE(s.unsafe_network,0) FROM nodes n LEFT JOIN rotation_settings s ON s.node_id=n.id WHERE n.id=?`, r.PathValue("id")).Scan(&name, &unsafe)
	var a *rotationAction
	if err == nil {
		a, err = p.latestRotation(r.Context(), r.PathValue("id"))
	}
	var auto, paused bool
	var noImprovement int
	if err == nil {
		err = p.db.QueryRowContext(r.Context(), `SELECT COALESCE(s.automatic_enabled,0),COALESCE(p.paused,0),COALESCE(p.no_improvement,0) FROM nodes n LEFT JOIN rotation_settings s ON s.node_id=n.id LEFT JOIN rotation_policy p ON p.node_id=n.id WHERE n.id=?`, r.PathValue("id")).Scan(&auto, &paused, &noImprovement)
	}
	q, quotaErr := p.rotationQuota(r.Context(), r.PathValue("id"))
	if err == nil {
		err = quotaErr
	}
	p.controlMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err != nil {
		problem(w, 503, "无法读取换 IP 设置")
		return
	}
	warning := ""
	if unsafe {
		warning = "检测到 NAT、多 IP 或查询源不一致的迹象，不支持自动换 IP；出站 IP 不能证明可直接入站。"
	}
	frozen, reason, operation, manageable := false, "", "", false
	if a != nil {
		frozen = a.State == "unconfirmed"
		if frozen {
			reason = a.Reason
		}
		operation = a.PendingOperation
		manageable = frozen && a.ObservationComplete && operation == ""
	}
	respond(w, 200, map[string]any{"recheck_status": rotationRecheckStatus(a), "automatic_enabled": auto, "paused": paused, "no_improvement": noImprovement, "quota": q, "configured": name != "", "unsafe_network": unsafe, "automatic_eligible": name != "" && !unsafe && !rotationPending(a) && q.RetryAfterSeconds == 0 && !paused, "warning": warning, "action": a, "frozen": frozen, "freeze_reason": reason, "pending_operation": operation, "can_reconcile": manageable, "can_unfreeze": manageable})
}
func (p *Panel) putRotation(w http.ResponseWriter, r *http.Request) {
	var c rotation.Config
	if !decode(w, r, &c) {
		return
	}
	if err := c.Validate(); err != nil {
		problem(w, 400, err.Error())
		return
	}
	data, _ := json.Marshal(c)
	if len(data) > 8192 {
		problem(w, 400, "换 IP 模板序列化后超过 8 KB")
		return
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	var exists int
	err := p.db.QueryRowContext(r.Context(), "SELECT 1 FROM nodes WHERE id=?", r.PathValue("id")).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err != nil {
		problem(w, 503, "无法保存换 IP 设置")
		return
	}
	suffix, _, err := p.randomCredential()
	if err != nil {
		problem(w, 503, "无法保存换 IP 设置")
		return
	}
	// 整个模板均视为秘密，URL、头、JSON 体不进入普通设置表或 API 回显。
	name := "rotation." + r.PathValue("id") + "." + suffix
	if err = p.secrets.Put(r.Context(), name, data); err == nil {
		_, err = p.db.ExecContext(r.Context(), `INSERT INTO rotation_settings(node_id,secret_name) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET secret_name=excluded.secret_name`, r.PathValue("id"), name)
	}
	if err != nil {
		problem(w, 503, "无法保存换 IP 设置")
		return
	}
	respond(w, 200, map[string]bool{"configured": true})
}
func (p *Panel) startRotation(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	a, err := p.authorizeRotation(r.Context(), r.PathValue("id"), true)
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法创建换 IP 动作")
		}
		return
	}
	respond(w, 202, map[string]any{"action": a})
}

// 调用方持有 controlMu；节点所有用途共用本事务的互斥与额度。
func (p *Panel) authorizeRotation(ctx context.Context, id string, manual bool) (*rotationAction, error) {
	var ip, secret string
	var last sql.NullInt64
	err := p.db.QueryRowContext(ctx, `SELECT COALESCE(r.public_ipv4,''),COALESCE(s.secret_name,''),n.last_seen_at FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id LEFT JOIN rotation_settings s ON s.node_id=n.id WHERE n.id=?`, id).Scan(&ip, &secret, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &forwardingError{404, "节点不存在"}
	}
	if err != nil {
		return nil, &forwardingError{503, "无法创建换 IP 动作"}
	}
	session := p.controls[id]
	if session == nil || !last.Valid || p.deps.Clock.Now().Sub(time.Unix(0, last.Int64)) >= 90*time.Second {
		return nil, &forwardingError{409, "节点控制连接离线，不签发执行许可"}
	}
	if _, err := publicip.Parse(ip); err != nil || secret == "" {
		return nil, &forwardingError{409, "请先配置接口并等待有效公网 IPv4"}
	}
	pending, err := p.latestRotation(ctx, id)
	if err != nil {
		return nil, &forwardingError{503, "无法读取换 IP 动作"}
	}
	if rotationPending(pending) {
		return nil, &forwardingError{409, "已有进行中或结果未确认的换 IP"}
	}
	if !manual {
		var enabled, unsafe, paused bool
		var maintenance sql.NullInt64
		err = p.db.QueryRowContext(ctx, `SELECT s.automatic_enabled,s.unsafe_network,COALESCE(p.paused,0),n.maintenance_until FROM rotation_settings s JOIN nodes n ON n.id=s.node_id LEFT JOIN rotation_policy p ON p.node_id=s.node_id WHERE s.node_id=?`, id).Scan(&enabled, &unsafe, &paused, &maintenance)
		if err != nil {
			return nil, err
		}
		q, e := p.rotationQuota(ctx, id)
		if e != nil {
			return nil, e
		}
		if !session.rotationRecheckCapable || !enabled || unsafe || paused || q.RetryAfterSeconds > 0 || (maintenance.Valid && maintenance.Int64 > p.deps.Clock.Now().UnixNano()) {
			return nil, &forwardingError{409, "自动换 IP 受额度、暂停、维护或网络风险限制"}
		}
	}
	actionID, _, err := p.randomCredential()
	if err != nil {
		return nil, &forwardingError{503, "无法创建动作 ID"}
	}
	origin, label := "manual", "手动"
	if !manual {
		origin, label = "automatic", "自动"
	}
	now := p.deps.Clock.Now()
	expires := now.Add(2 * time.Minute)
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, &forwardingError{503, "无法保存动作"}
	}
	defer tx.Rollback()
	if !manual {
		var eligible int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM node_self_check c WHERE c.node_id=? AND c.enabled=1 AND c.state='blocked' AND EXISTS(SELECT 1 FROM entry_group_members m JOIN entry_groups g ON g.id=m.group_id WHERE m.node_id=c.node_id AND g.blocked_policy='rotate_ip')`, id).Scan(&eligible); err != nil {
			return nil, err
		}
		if eligible == 0 {
			return nil, &forwardingError{409, "自动换 IP 申请策略或被墙状态已变化"}
		}
	}
	settings, e := loadSelfCheckSettings(ctx, tx)
	if e != nil {
		return nil, e
	}
	recheckConfig := []byte{}
	if session.rotationRecheckCapable {
		recheckConfig, _ = json.Marshal(selfcheck.Config{Enabled: true, Settings: settings})
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name,origin) VALUES(?,?,?,'authorized',?,?,?,?,?)`, actionID, id, ip, now.UnixNano(), expires.UnixNano(), now.UnixNano(), secret, origin)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE rotation_actions SET recheck_config=?,recheck_revision=MAX(1,(SELECT desired_revision FROM nodes WHERE id=?)) WHERE id=?`, recheckConfig, id, actionID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO rotation_quota(action_id,node_id,attempted_at) VALUES(?,?,?)", actionID, id, now.UnixNano())
	}

	if err == nil {
		err = p.rotationNotice(ctx, tx, id, actionID, "authorized", label+"换 IP 已授权")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, &forwardingError{503, "无法保存换 IP 动作"}
	}
	select {
	case session.changed <- struct{}{}:
	default:
	}
	return p.latestRotation(ctx, id)
}

// 唯一节点级授权接缝：只有当前节点的动作会进入它的独立命令，绝不混入配置快照。
func (p *Panel) rotationCommand(ctx context.Context, id string) (*controlpb.RotationCommand, error) {
	var actionID, oldIP, secret, name string
	var expires, created int64
	var operation string
	var sequence uint64
	err := p.db.QueryRowContext(ctx, `SELECT a.id,a.old_ip,a.expires_at,a.created_at,a.secret_name,n.name,a.pending_operation,a.control_sequence FROM rotation_actions a JOIN nodes n ON n.id=a.node_id WHERE a.node_id=? AND (a.state='authorized' OR a.pending_operation!='') ORDER BY a.created_at DESC LIMIT 1`, id).Scan(&actionID, &oldIP, &expires, &created, &secret, &name, &operation, &sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if operation != "" {
		return &controlpb.RotationCommand{ActionId: actionID, NodeId: id, OldIp: oldIP, Operation: operation, ControlSequence: sequence}, nil
	}
	var recheck []byte
	var revision uint64
	var origin string
	if err = p.db.QueryRowContext(ctx, "SELECT recheck_config,recheck_revision,origin FROM rotation_actions WHERE id=?", actionID).Scan(&recheck, &revision, &origin); err != nil {
		return nil, err
	}
	session := p.controls[id]
	if session == nil || !session.rotationRecheckCapable {
		if origin == "automatic" {
			tx, e := p.db.BeginTx(ctx, nil)
			if e != nil {
				return nil, e
			}
			defer tx.Rollback()
			var sent bool
			if e = tx.QueryRowContext(ctx, "SELECT permit_sent FROM rotation_actions WHERE id=?", actionID).Scan(&sent); e != nil {
				return nil, e
			}
			state, message := "cancelled", "节点不支持新 IP 复检，自动许可已取消且未投递"
			if sent {
				state, message = "authorized", "节点复检能力变化，许可可能已执行；暂停投递，升级节点后核对原动作"
			}
			if _, e = tx.ExecContext(ctx, `UPDATE rotation_actions SET state=?,reason=?,observation_complete=0,updated_at=? WHERE id=? AND state='authorized'`, state, message, p.deps.Clock.Now().UnixNano(), actionID); e == nil && !sent {
				_, e = tx.ExecContext(ctx, "DELETE FROM rotation_quota WHERE action_id=?", actionID)
			}
			if e == nil {
				kind := state
				if sent {
					kind = "waiting_upgrade"
				}
				e = p.rotationNotice(ctx, tx, id, actionID, kind, message)
			}
			if e == nil {
				e = tx.Commit()
			}
			return nil, e
		}
		recheck = nil
	}
	data, err := p.secrets.Get(ctx, secret)
	if err != nil {
		return nil, err
	}

	if _, err = p.db.ExecContext(ctx, "UPDATE rotation_actions SET permit_sent=1 WHERE id=?", actionID); err != nil {
		return nil, err
	}
	return &controlpb.RotationCommand{Automatic: origin == "automatic", RecheckConfig: recheck, RecheckRevision: revision, ActionId: actionID, NodeId: id, OldIp: oldIP, ExpiresAt: expires, IssuedAt: created, ConfigJson: data, NodeName: name}, nil
}

// 独立结果只推进匹配动作，不推进活性、配置、探针或事件确认。
func (p *Panel) acceptRotation(ctx context.Context, id string, result *controlpb.RotationProgress) error {
	if result == nil {
		return errors.New("缺少换 IP 进度")
	}
	a, err := p.latestRotation(ctx, id)
	if err != nil {
		return err
	}
	if a == nil || a.ID != result.ActionId || a.OldIP != result.OldIp || result.Sequence <= a.Sequence {
		return nil
	}
	if !rotationPending(a) && !(a.State == "changed" && a.RecheckStatus == "checking") {
		return nil
	}
	if result.ControlSequence < a.ControlSequence {
		return nil
	}
	if result.Sequence > 1<<63-1 || result.Attempts > 5 || result.ControlSequence != a.ControlSequence {
		return errors.New("换 IP 进度无效")
	}
	switch result.State {
	case "started", "observing", "changed", "cancelled", "unconfirmed", "released":
	default:
		return errors.New("换 IP 状态无效")
	}
	if result.State == "released" && (a.PendingOperation != "release" || !result.ObservationComplete || result.Reason != "manual_release") {
		return errors.New("缺少解除冻结授权")
	}
	if a.PendingOperation == "release" && result.State != "released" {
		return errors.New("解除冻结结果无效")
	}
	if a.PendingOperation == "reconcile" && result.State != "changed" && result.State != "unconfirmed" {
		return errors.New("核对结果无效")
	}
	message, ok := rotation.Reasons[result.Reason]
	if !ok {
		return errors.New("换 IP 原因无效")
	}
	if result.State != "changed" && result.NewIp != "" {
		return errors.New("换 IP 新地址无效")
	}
	if result.State == "changed" {
		ip, e := publicip.Parse(result.NewIp)
		if e != nil || ip == a.OldIP || ip != result.NewIp {
			return errors.New("换 IP 新地址无效")
		}
	}
	if a.State == "changed" && (result.State != "changed" || result.NewIp != a.NewIP) {
		return errors.New("复检不能改写已确认地址")
	}
	recheckStatus, rounds, err := p.validateRotationRecheck(ctx, a, result)
	if err != nil {
		return err
	}
	region := Region{Name: "未知"}
	if result.State == "changed" && p.deps.RegionLookup != nil {
		if resolved, e := p.deps.RegionLookup(ctx, result.NewIp); e == nil && resolved.Name != "" {
			region = resolved
		}
	}
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE rotation_actions SET new_ip=?,state=?,reason=?,sequence=?,attempts=?,unsafe_network=?,updated_at=?,observation_complete=?,pending_operation='' WHERE id=?`, result.NewIp, result.State, message, result.Sequence, result.Attempts, result.UnsafeNetwork, now.UnixNano(), result.ObservationComplete, a.ID)
	if err == nil && result.State == "changed" {
		_, err = tx.ExecContext(ctx, "UPDATE rotation_actions SET recheck_status=?,recheck_rounds=? WHERE id=?", recheckStatus, rounds, a.ID)
	}
	var groups []string
	if err == nil && result.State == "changed" && a.State != "changed" {
		groups, err = p.rotationDNSActions(ctx, tx, a, result.NewIp)
	}
	if err == nil {
		err = p.finishRotationRecheck(ctx, tx, a, recheckStatus, rounds)
	}
	if err == nil {
		err = p.rotationNotice(ctx, tx, id, a.ID, result.State, message)
	}
	if err == nil && result.State == "cancelled" {
		_, err = tx.ExecContext(ctx, "DELETE FROM rotation_quota WHERE action_id=?", a.ID)
	}
	if err == nil && result.State == "changed" {
		_, err = tx.ExecContext(ctx, `INSERT INTO node_regions(node_id,public_ipv4,auto_name,auto_mainland,domestic_since) VALUES(?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET public_ipv4=excluded.public_ipv4,auto_name=excluded.auto_name,auto_mainland=excluded.auto_mainland,domestic_since=CASE WHEN node_regions.public_ipv4<>excluded.public_ipv4 THEN excluded.domestic_since ELSE node_regions.domestic_since END`, id, result.NewIp, region.Name, region.MainlandChina, p.deps.Clock.Now().UnixNano())
	}
	if err == nil && recheckStatus == "recovered" {
		var evidence []selfcheck.Round
		if err = json.Unmarshal(rounds, &evidence); err == nil && len(evidence) > 0 {
			err = p.recordAddressDomesticEvidence(ctx, tx, id, result.NewIp, "self_check", evidence[len(evidence)-1].StartedAt)
		}
	}
	if err == nil && result.UnsafeNetwork {
		_, err = tx.ExecContext(ctx, "UPDATE rotation_settings SET unsafe_network=1 WHERE node_id=?", id)
	}

	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	for _, gid := range groups {
		p.coordinator.wake(gid)
	}
	return nil
}
