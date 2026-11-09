package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"nbpass/internal/eventlog"
	"nbpass/internal/node"
	"nbpass/internal/nodeupdate"
)

const nodeUpgradeMigration = `CREATE TABLE node_upgrades (
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id), manifest BLOB NOT NULL,
 progress BLOB NOT NULL, initial_instance TEXT NOT NULL, created_at INTEGER NOT NULL, active INTEGER NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX node_upgrades_active ON node_upgrades(node_id) WHERE active=1;
CREATE INDEX node_upgrades_node ON node_upgrades(node_id,created_at);`

type nodeUpgradeView struct {
	PanelVersion    string               `json:"panel_version"`
	VersionMismatch bool                 `json:"version_mismatch"`
	UpgradeCapable  bool                 `json:"upgrade_capable"`
	Upgrade         *nodeupdate.Progress `json:"upgrade,omitempty"`
}

func (p *Panel) upgradeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nodes/{id}/upgrade", p.startNodeUpgrade)
	mux.HandleFunc("GET /api/nodes/{id}/upgrade", p.getNodeUpgrade)
	mux.HandleFunc("GET /node-upgrades/{task}/{resource}", p.nodeUpgradeResource)
}
func (p *Panel) latestUpgrade(ctx context.Context, id string) (*nodeupdate.Progress, error) {
	var data []byte
	err := p.db.QueryRowContext(ctx, "SELECT progress FROM node_upgrades WHERE node_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v nodeupdate.Progress
	err = json.Unmarshal(data, &v)
	return &v, err
}
func (p *Panel) upgradeView(ctx context.Context, id, version string) (nodeUpgradeView, error) {
	task, err := p.latestUpgrade(ctx, id)
	v := nodeUpgradeView{PanelVersion: node.SoftwareVersion, VersionMismatch: version != "" && version != node.SoftwareVersion, Upgrade: task}
	if session := p.controls[id]; session != nil {
		v.UpgradeCapable = session.upgradeCapable
	}
	return v, err
}
func (p *Panel) getNodeUpgrade(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	var version string
	if err := p.db.QueryRowContext(r.Context(), "SELECT software_version FROM nodes WHERE id=?", r.PathValue("id")).Scan(&version); err != nil {
		problem(w, 404, "节点不存在")
		return
	}
	view, err := p.upgradeView(r.Context(), r.PathValue("id"), version)
	if err != nil {
		problem(w, 503, "无法读取升级任务")
		return
	}
	respond(w, 200, view)
}
func (p *Panel) startNodeUpgrade(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	id := r.PathValue("id")
	session := p.controls[id]
	var last sql.NullInt64
	if err := p.db.QueryRowContext(r.Context(), "SELECT last_seen_at FROM nodes WHERE id=?", id).Scan(&last); err != nil {
		problem(w, 404, "节点不存在")
		return
	}
	if session == nil || !session.upgradeCapable || !last.Valid || p.deps.Clock.Now().Sub(time.Unix(0, last.Int64)) >= offlineWindow {
		problem(w, 409, "节点须在线且已安装独立更新服务")
		return
	}
	if task, err := p.latestUpgrade(r.Context(), id); err != nil || task != nil && !nodeupdate.Terminal(task.State) {
		problem(w, 409, "已有未完成的升级任务")
		return
	}
	// Do not overlap a potentially dispatched IP rotation with the restart window.
	if action, err := p.latestRotation(r.Context(), id); err != nil || action != nil && rotationPending(action) {
		problem(w, 409, "先核对并结束进行中的换 IP 动作")
		return
	}
	if p.deps.NodeBinary == nil {
		problem(w, 503, "面板未内置节点程序")
		return
	}
	image, err := p.deps.NodeBinary()
	if err != nil {
		problem(w, 503, "无法读取节点程序")
		return
	}
	defer image.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(image, nodeupdate.MaxSize+1))
	if err != nil || size <= 0 || size > nodeupdate.MaxSize {
		problem(w, 503, "节点产物大小无效")
		return
	}
	taskID, _, err := p.randomCredential()
	if err != nil {
		problem(w, 503, "无法创建任务")
		return
	}
	m := nodeupdate.Manifest{TaskID: taskID, Version: node.SoftwareVersion, Platform: nodeupdate.Platform, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}
	progress := nodeupdate.Progress{Manifest: m, State: "queued", Steps: []string{"queued"}}
	manifest, _ := json.Marshal(m)
	data, _ := json.Marshal(progress)
	now := p.deps.Clock.Now()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), "INSERT INTO node_upgrades(id,node_id,manifest,progress,initial_instance,created_at) VALUES(?,?,?,?,?,?)", taskID, id, manifest, data, session.instance, now.UnixNano())
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE nodes SET maintenance_until=? WHERE id=?", now.Add(10*time.Minute).UnixNano(), id)
	}
	if err == nil {
		err = insertEvent(r.Context(), tx, id, "panel", eventlog.Event{ID: "upgrade:" + taskID + ":queued", Type: "node_upgrade", OccurredAt: now, Message: "单节点升级已排队，进入 10 分钟维护期"}, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "无法保存升级任务")
		return
	}
	select {
	case session.changed <- struct{}{}:
	default:
	}
	respond(w, 202, map[string]any{"task": progress})
}
func (p *Panel) upgradeCommand(ctx context.Context, id string) ([]byte, error) {
	var data []byte
	err := p.db.QueryRowContext(ctx, "SELECT manifest FROM node_upgrades WHERE node_id=? AND active=1", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return data, err
}
func (p *Panel) acceptUpgrade(ctx context.Context, id string, data []byte) error {
	if len(data) > 8192 {
		return errors.New("升级报告过大")
	}
	var v nodeupdate.Progress
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if v.Manifest.Validate() != nil || len(v.Steps) > 16 || len(v.Error) > 512 {
		return errors.New("无效升级报告")
	}
	switch v.State {
	case "downloading", "verifying", "switching", "rolling_back", "rolled_back", "reconnecting", "unconfirmed", "failed":
	default:
		return errors.New("无效升级阶段")
	}
	var saved []byte
	var active bool
	err := p.db.QueryRowContext(ctx, "SELECT progress,active FROM node_upgrades WHERE id=? AND node_id=?", v.Manifest.TaskID, id).Scan(&saved, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var old nodeupdate.Progress
	if err = json.Unmarshal(saved, &old); err != nil {
		return err
	}
	if !active || old.Manifest != v.Manifest || v.Sequence < old.Sequence {
		return nil
	}
	if v.Sequence == old.Sequence {
		if v.State != old.State && old.State != "unconfirmed" {
			return nil
		}
		// Readiness can recover while updater progress stays fixed. Refresh only the
		// running-process evidence; an equal sequence cannot rewrite the task stage.
		old.Verification = v.Verification
		v = old
	}
	if old.State == "reconnecting" && v.State != "reconnecting" && v.State != "unconfirmed" {
		return nil
	}
	if old.State == "unconfirmed" && v.State != "rolled_back" && v.State != "failed" && v.State != "unconfirmed" {
		v.State = "unconfirmed"
		v.Steps = append(v.Steps, "unconfirmed")
	}
	now := p.deps.Clock.Now()
	terminal := v.State == "rolled_back" || v.State == "failed"
	data, _ = json.Marshal(v)
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "UPDATE node_upgrades SET progress=?,active=? WHERE id=? AND active=1", data, !terminal, v.Manifest.TaskID)
	if err == nil && terminal {
		_, err = tx.ExecContext(ctx, "UPDATE nodes SET maintenance_until=? WHERE id=?", now.UnixNano(), id)
	}
	if err == nil {
		err = insertEvent(ctx, tx, id, "panel", eventlog.Event{ID: "upgrade:" + v.Manifest.TaskID + ":" + v.State, Type: "node_upgrade", OccurredAt: now, Message: "节点升级阶段：" + v.State}, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	return err
}

// Called only after an authenticated heartbeat; progress reports cannot create liveness.
func (p *Panel) confirmUpgrade(ctx context.Context, id, instance, version string) error {
	var data []byte
	var initial string
	err := p.db.QueryRowContext(ctx, "SELECT progress,initial_instance FROM node_upgrades WHERE node_id=? AND active=1", id).Scan(&data, &initial)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var v nodeupdate.Progress
	if err = json.Unmarshal(data, &v); err != nil {
		return err
	}
	if (v.State != "reconnecting" && v.State != "unconfirmed") || initial == instance || version != v.Manifest.Version {
		return nil
	}
	if !v.Verification.Matches(v.Manifest, instance) {
		return nil
	}
	var desired, applied int64
	var applyError string
	if err = p.db.QueryRowContext(ctx, "SELECT desired_revision,applied_revision,apply_error FROM nodes WHERE id=?", id).Scan(&desired, &applied, &applyError); err != nil {
		return err
	}
	if desired != applied || applyError != "" {
		return nil
	}
	v.State = "ready"
	v.Steps = append(v.Steps, "ready")
	data, _ = json.Marshal(v)
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, "UPDATE node_upgrades SET progress=?,active=0 WHERE id=?", data, v.Manifest.TaskID)
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE nodes SET maintenance_until=? WHERE id=?", p.deps.Clock.Now().UnixNano(), id)
	}
	if err == nil {
		now := p.deps.Clock.Now()
		err = insertEvent(ctx, tx, id, "panel", eventlog.Event{ID: "upgrade:" + v.Manifest.TaskID + ":ready", Type: "node_upgrade", OccurredAt: now, Message: "节点升级完成，新进程版本一致且本地业务就绪"}, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	return err
}
func (p *Panel) nodeUpgradeResource(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(token) != 43 {
		problem(w, 401, "节点凭据无效")
		return
	}
	digest := sha256.Sum256([]byte(token))
	var raw []byte
	err := p.db.QueryRowContext(r.Context(), `SELECT u.manifest FROM node_upgrades u JOIN nodes n ON n.id=u.node_id WHERE u.id=? AND n.token_hash=? AND u.active=1 AND n.maintenance_until>?`, r.PathValue("task"), digest[:], p.deps.Clock.Now().UnixNano()).Scan(&raw)
	if err != nil {
		problem(w, 404, "升级任务无效或已过期")
		return
	}
	var m nodeupdate.Manifest
	if json.Unmarshal(raw, &m) != nil {
		problem(w, 503, "清单无法读取")
		return
	}
	switch r.PathValue("resource") {
	case "manifest":
		respond(w, 200, m)
	case "artifact":
		if p.deps.NodeBinary == nil {
			problem(w, 503, "产物不可用")
			return
		}
		f, err := p.deps.NodeBinary()
		if err != nil {
			problem(w, 503, "产物不可用")
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, nodeupdate.MaxSize+1))
		if err != nil || nodeupdate.Verify(m, data) != nil {
			problem(w, 503, "当前产物与任务清单不符")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data)
	default:
		problem(w, 404, "只允许固定清单与产物")
	}
}
