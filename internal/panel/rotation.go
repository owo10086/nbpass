package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
	"nbpass/internal/publicip"
	"nbpass/internal/rotation"
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
	ID            string    `json:"id"`
	NodeID        string    `json:"node_id"`
	OldIP         string    `json:"old_ip"`
	NewIP         string    `json:"new_ip"`
	State         string    `json:"state"`
	Reason        string    `json:"reason"`
	Sequence      uint64    `json:"sequence"`
	Attempts      uint32    `json:"attempts"`
	UnsafeNetwork bool      `json:"unsafe_network"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (p *Panel) rotationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{id}/ip-rotation", p.getRotation)
	mux.HandleFunc("PUT /api/nodes/{id}/ip-rotation", p.putRotation)
	mux.HandleFunc("POST /api/nodes/{id}/ip-rotation/actions", p.startRotation)
}
func (p *Panel) latestRotation(ctx context.Context, id string) (*rotationAction, error) {
	a := &rotationAction{}
	var created, expires, updated int64
	err := p.db.QueryRowContext(ctx, `SELECT id,node_id,old_ip,new_ip,state,reason,sequence,attempts,unsafe_network,created_at,expires_at,updated_at FROM rotation_actions WHERE node_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, id).Scan(&a.ID, &a.NodeID, &a.OldIP, &a.NewIP, &a.State, &a.Reason, &a.Sequence, &a.Attempts, &a.UnsafeNetwork, &created, &expires, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt = time.Unix(0, created).UTC()
	a.ExpiresAt = time.Unix(0, expires).UTC()
	a.UpdatedAt = time.Unix(0, updated).UTC()
	return a, nil
}
func (p *Panel) getRotation(w http.ResponseWriter, r *http.Request) {
	var name string
	var unsafe bool
	err := p.db.QueryRowContext(r.Context(), `SELECT COALESCE(s.secret_name,''),COALESCE(s.unsafe_network,0) FROM nodes n LEFT JOIN rotation_settings s ON s.node_id=n.id WHERE n.id=?`, r.PathValue("id")).Scan(&name, &unsafe)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err != nil {
		problem(w, 503, "无法读取换 IP 设置")
		return
	}
	a, err := p.latestRotation(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 503, "无法读取换 IP 动作")
		return
	}
	warning := ""
	if unsafe {
		warning = "检测到 NAT、多 IP 或查询源不一致的迹象，不支持自动换 IP；出站 IP 不能证明可直接入站。"
	}
	respond(w, 200, map[string]any{"configured": name != "", "unsafe_network": unsafe, "automatic_eligible": name != "" && !unsafe, "warning": warning, "action": a})
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
	id := r.PathValue("id")
	var ip, secret string
	var last sql.NullInt64
	err := p.db.QueryRowContext(r.Context(), `SELECT COALESCE(r.public_ipv4,''),COALESCE(s.secret_name,''),n.last_seen_at FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id LEFT JOIN rotation_settings s ON s.node_id=n.id WHERE n.id=?`, id).Scan(&ip, &secret, &last)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err != nil {
		problem(w, 503, "无法创建换 IP 动作")
		return
	}
	session := p.controls[id]
	if session == nil || !last.Valid || p.deps.Clock.Now().Sub(time.Unix(0, last.Int64)) >= 90*time.Second {
		problem(w, 409, "节点控制连接离线，不签发执行许可")
		return
	}
	if _, err := publicip.Parse(ip); err != nil || secret == "" {
		problem(w, 409, "请先配置接口并等待有效公网 IPv4")
		return
	}
	pending, err := p.latestRotation(r.Context(), id)
	if err != nil {
		problem(w, 503, "无法读取换 IP 动作")
		return
	}
	if pending != nil && pending.State != "changed" && pending.State != "cancelled" {
		problem(w, 409, "已有进行中或结果未确认的换 IP")
		return
	}
	actionID, _, err := p.randomCredential()
	if err != nil {
		problem(w, 503, "无法创建动作 ID")
		return
	}
	now := p.deps.Clock.Now()
	expires := now.Add(2 * time.Minute)
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "无法保存动作")
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `INSERT INTO rotation_actions(id,node_id,old_ip,state,created_at,expires_at,updated_at,secret_name) VALUES(?,?,?,'authorized',?,?,?,?)`, actionID, id, ip, now.UnixNano(), expires.UnixNano(), now.UnixNano(), secret)
	if err == nil {
		err = insertEvent(r.Context(), tx, id, "panel", eventlog.Event{ID: "rotation-authorized:" + actionID, Type: "ip_rotation_authorized", OccurredAt: now, Message: "手动换 IP 已授权，动作 " + actionID}, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "无法保存换 IP 动作")
		return
	}
	select {
	case session.changed <- struct{}{}:
	default:
	}
	a, _ := p.latestRotation(r.Context(), id)
	respond(w, 202, map[string]any{"action": a})
}

// 唯一节点级授权接缝：只有当前节点的动作会进入它的独立命令，绝不混入配置快照。
func (p *Panel) rotationCommand(ctx context.Context, id string) (*controlpb.RotationCommand, error) {
	var actionID, oldIP, secret, name string
	var expires, created int64
	err := p.db.QueryRowContext(ctx, `SELECT a.id,a.old_ip,a.expires_at,a.created_at,a.secret_name,n.name FROM rotation_actions a JOIN nodes n ON n.id=a.node_id WHERE a.node_id=? AND a.state='authorized' ORDER BY a.created_at DESC LIMIT 1`, id).Scan(&actionID, &oldIP, &expires, &created, &secret, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := p.secrets.Get(ctx, secret)
	if err != nil {
		return nil, err
	}
	return &controlpb.RotationCommand{ActionId: actionID, NodeId: id, OldIp: oldIP, ExpiresAt: expires, IssuedAt: created, ConfigJson: data, NodeName: name}, nil
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
	if a.State == "changed" || a.State == "cancelled" {
		return nil
	}
	if result.Sequence > 100 || result.Attempts > 5 {
		return errors.New("换 IP 进度无效")
	}
	switch result.State {
	case "started", "observing", "changed", "cancelled", "unconfirmed":
	default:
		return errors.New("换 IP 状态无效")
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
	_, err = tx.ExecContext(ctx, `UPDATE rotation_actions SET new_ip=?,state=?,reason=?,sequence=?,attempts=?,unsafe_network=?,updated_at=? WHERE id=?`, result.NewIp, result.State, message, result.Sequence, result.Attempts, result.UnsafeNetwork, now.UnixNano(), a.ID)
	if err == nil && result.State == "changed" {
		_, err = tx.ExecContext(ctx, `INSERT INTO node_regions(node_id,public_ipv4,auto_name,auto_mainland) VALUES(?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET public_ipv4=excluded.public_ipv4,auto_name=excluded.auto_name,auto_mainland=excluded.auto_mainland`, id, result.NewIp, region.Name, region.MainlandChina)
	}
	if err == nil && result.UnsafeNetwork {
		_, err = tx.ExecContext(ctx, "UPDATE rotation_settings SET unsafe_network=1 WHERE node_id=?", id)
	}
	if err == nil {
		err = insertEvent(ctx, tx, id, "panel", eventlog.Event{ID: "rotation:" + a.ID + ":" + result.State, Type: "ip_rotation_" + result.State, OccurredAt: now, Message: "动作 " + a.ID + "：" + message}, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	return nil
}
