package panel

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"nbpass/internal/probe"
)

func (p *Panel) randomCredential() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(p.deps.Random, raw); err != nil {
		return "", nil, err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(value))
	return value, digest[:], nil
}

func (p *Panel) installation(id, ticket string, expires time.Time) map[string]any {
	return map[string]any{
		"id": id, "installation_ticket": ticket, "ticket_expires_at": expires.UTC().Format(time.RFC3339),
		"installation_command": "curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL " + shellQuote(p.config.PublicOrigin+"/install-node.sh") + " | sh -s -- -control-url " + shellQuote(p.config.ControlURL) + " -ticket " + shellQuote(ticket),
	}
}

func (p *Panel) addNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" || len(body.Name) > 200 {
		problem(w, 400, "节点名称须为 1–200 字节")
		return
	}
	id, _, err := p.randomCredential()
	if err != nil {
		problem(w, 500, "无法创建节点")
		return
	}
	ticket, digest, err := p.randomCredential()
	if err != nil {
		problem(w, 500, "无法创建安装票据")
		return
	}
	now := p.deps.Clock.Now()
	expires := now.Add(30 * time.Minute)
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), "INSERT INTO nodes(id, name, created_at) VALUES(?, ?, ?)", id, body.Name, now.UnixNano())
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "INSERT INTO installation_tickets(ticket_hash, node_id, expires_at) VALUES(?, ?, ?)", digest, id, expires.UnixNano())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 201, p.installation(id, ticket, expires))
}

type nodeView struct {
	nodeUpgradeView
	nodeRegionView
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Status           string       `json:"status"`
	StatusLabel      string       `json:"status_label"`
	Connected        bool         `json:"connected"`
	LastSeenAt       *time.Time   `json:"last_seen_at"`
	MaintenanceUntil *time.Time   `json:"maintenance_until"`
	DesiredRevision  int64        `json:"desired_revision"`
	AppliedRevision  int64        `json:"applied_revision"`
	ConfigStatus     string       `json:"config_status"`
	ApplyError       string       `json:"apply_error"`
	AppliedDigest    string       `json:"applied_digest"`
	DesiredDigest    string       `json:"desired_digest"`
	SoftwareVersion  string       `json:"software_version"`
	ProcessInstance  string       `json:"process_instance"`
	Probe            probe.Values `json:"probe"`
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	t := time.Unix(0, value.Int64).UTC()
	return &t
}

func (p *Panel) listNodes(w http.ResponseWriter, r *http.Request) {
	if err := p.observeAllOffline(r.Context()); err != nil {
		problem(w, 503, "离线状态存储暂不可用")
		return
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	rows, err := p.db.QueryContext(r.Context(), `SELECT id, name, last_seen_at, disconnected_at, maintenance_until,
		desired_revision, applied_revision, software_version, process_instance, apply_error, applied_digest, COALESCE((SELECT digest FROM node_snapshots WHERE node_id=nodes.id),''),
		COALESCE(public_ipv4, ''), COALESCE(auto_name, '未知'), COALESCE(auto_mainland, 0),
		override_name, COALESCE(override_mainland, 0), COALESCE(checker_disabled, 0)
		FROM nodes LEFT JOIN node_regions ON node_id = id ORDER BY `+listOrders["nodes"].orderBy)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer rows.Close()
	nodes := []nodeView{}
	now := p.deps.Clock.Now()
	for rows.Next() {
		var n nodeView
		var last, disconnected, maintenance sql.NullInt64
		var override sql.NullString
		var mainland bool
		if err := rows.Scan(&n.ID, &n.Name, &last, &disconnected, &maintenance, &n.DesiredRevision, &n.AppliedRevision, &n.SoftwareVersion, &n.ProcessInstance, &n.ApplyError, &n.AppliedDigest, &n.DesiredDigest,
			&n.PublicIPv4, &n.AutomaticRegion.Name, &n.AutomaticRegion.MainlandChina, &override, &mainland, &n.CheckerDisabled); err != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
		n.resolve(override, mainland)
		n.LastSeenAt, n.MaintenanceUntil = nullableTime(last), nullableTime(maintenance)
		state := p.presence(n.ID, last, disconnected, maintenance, now)
		n.Connected, n.Status, n.StatusLabel = state.connected, state.status, state.label
		n.ConfigStatus = "待确认"
		if n.Connected {
			n.ConfigStatus = "未同步"
			if n.ApplyError != "" {
				n.ConfigStatus = "同步失败"
			}
			if n.DesiredRevision == n.AppliedRevision {
				n.ConfigStatus = "正常"
			}
		}
		nodes = append(nodes, n)
	}
	if rows.Err() != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	rows.Close()
	snapshots, err := p.probeSnapshots(r.Context())
	if err != nil {
		problem(w, 503, "探针存储暂不可用")
		return
	}
	for i := range nodes {
		n := &nodes[i]
		if s, ok := snapshots[n.ID]; ok && probeFresh(p.liveSession(n.ID), n.ProcessInstance, s.instance, n.LastSeenAt, s.received, now) {
			n.Probe = s.values
		}
	}
	for i := range nodes {
		view, err := p.upgradeView(r.Context(), nodes[i].ID, nodes[i].SoftwareVersion)
		if err != nil {
			problem(w, 503, "无法读取升级任务")
			return
		}
		nodes[i].nodeUpgradeView = view
	}
	respond(w, 200, map[string]any{"nodes": nodes})
}

func (p *Panel) resetNodeToken(w http.ResponseWriter, r *http.Request) { p.changeNodeToken(w, r, true) }
func (p *Panel) revokeNodeToken(w http.ResponseWriter, r *http.Request) {
	p.changeNodeToken(w, r, false)
}

func (p *Panel) changeNodeToken(w http.ResponseWriter, r *http.Request, reset bool) {
	var ticket string
	var digest []byte
	var err error
	if reset {
		ticket, digest, err = p.randomCredential()
		if err != nil {
			problem(w, 500, "无法创建安装票据")
			return
		}
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	id := r.PathValue("id")
	now := p.deps.Clock.Now()
	var exists string
	err = tx.QueryRowContext(r.Context(), "UPDATE nodes SET token_hash = NULL, disconnected_at = ? WHERE id = ? RETURNING id", now.UnixNano(), id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "DELETE FROM installation_tickets WHERE node_id = ?", id)
	}
	expires := now.Add(30 * time.Minute)
	if err == nil && reset {
		_, err = tx.ExecContext(r.Context(), "INSERT INTO installation_tickets(ticket_hash, node_id, expires_at) VALUES(?, ?, ?)", digest, id, expires.UnixNano())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if session := p.controls[id]; session != nil {
		delete(p.controls, id)
		session.stop()
	}
	for session := range p.pendingControls[id] {
		session.stop()
	}
	delete(p.pendingControls, id)
	if reset {
		respond(w, 200, p.installation(id, ticket, expires))
	} else {
		respond(w, 204, nil)
	}
}

func (p *Panel) maintainNode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		problem(w, 400, "请指定维护状态")
		return
	}
	var until any
	now := p.deps.Clock.Now()
	if *body.Enabled {
		until = now.Add(10 * time.Minute).UnixNano()
	} else {
		until = now.UnixNano()
	}
	result, err := p.db.ExecContext(r.Context(), "UPDATE nodes SET maintenance_until = ? WHERE id = ?", until, r.PathValue("id"))
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		problem(w, 404, "节点不存在")
		return
	}
	respond(w, 204, nil)
}
