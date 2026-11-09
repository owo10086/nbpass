package panel

import (
	"context"
	"database/sql"
	"nbpass/internal/eventlog"
	"net/http"
	"time"
)

func (p *Panel) rotationPolicyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/nodes/{id}/ip-rotation/automatic", p.putAutomaticRotation)
	mux.HandleFunc("POST /api/nodes/{id}/ip-rotation/resume", p.resumeRotation)
	mux.HandleFunc("GET /api/entry-groups/{id}/blocked-policy", p.getBlockedPolicy)
	mux.HandleFunc("PUT /api/entry-groups/{id}/blocked-policy", p.putBlockedPolicy)
}
func (p *Panel) putAutomaticRotation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil {
		problem(w, 400, "请指定自动换 IP 开关")
		return
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	result, err := p.db.ExecContext(r.Context(), "UPDATE rotation_settings SET automatic_enabled=? WHERE node_id=?", *b.Enabled, r.PathValue("id"))
	if err == nil {
		err = missingResult(result, nil)
	}
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法保存自动换 IP 开关")
		}
		return
	}
	respond(w, 200, map[string]bool{"enabled": *b.Enabled})
}
func (p *Panel) resumeRotation(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !b.Confirm {
		problem(w, 400, "请确认解除无改善暂停，额度仍保留")
		return
	}
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "无法解除暂停")
		return
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRowContext(r.Context(), "SELECT count(*) FROM nodes WHERE id=?", r.PathValue("id")).Scan(&exists)
	if err == nil && exists == 0 {
		problem(w, 404, "节点不存在")
		return
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO rotation_policy(node_id,last_reset_at) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET paused=0,no_improvement=0,last_reset_at=excluded.last_reset_at`, r.PathValue("id"), p.deps.Clock.Now().UnixNano())
	}
	key, _, e := p.randomCredential()
	if err == nil {
		err = e
	}
	if err == nil {
		err = p.rotationNotice(r.Context(), tx, r.PathValue("id"), key, "resumed", "管理员已解除无改善暂停，已用额度保留")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "无法解除暂停")
		return
	}
	respond(w, 200, map[string]bool{"paused": false})
}
func (p *Panel) getBlockedPolicy(w http.ResponseWriter, r *http.Request) {
	var policy string
	err := p.db.QueryRowContext(r.Context(), "SELECT blocked_policy FROM entry_groups WHERE id=?", r.PathValue("id")).Scan(&policy)
	if err != nil {
		if err == sql.ErrNoRows {
			problem(w, 404, "入口组不存在")
		} else {
			problem(w, 503, "无法读取策略")
		}
		return
	}
	respond(w, 200, map[string]string{"policy": policy})
}
func (p *Panel) putBlockedPolicy(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Policy string `json:"policy"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Policy != "disabled" && b.Policy != "rotate_ip" && b.Policy != "switch_entry" {
		problem(w, 400, "本版本支持停用、策略①仅换 IP 或策略②仅入口切换")
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	result, err := p.db.ExecContext(r.Context(), "UPDATE entry_groups SET blocked_policy=? WHERE id=?", b.Policy, r.PathValue("id"))
	if err == nil {
		err = missingResult(result, nil)
	}
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法保存策略")
		}
		return
	}
	respond(w, 200, b)
}

// 与动作事务一起保存通知待办，复用持久事件/Telegram 投递，网络不阻塞授权。
func (p *Panel) rotationNotice(ctx context.Context, tx *sql.Tx, nodeID, actionID, kind, message string) error {
	now := p.deps.Clock.Now()
	id := "rotation-notice:" + actionID + ":" + kind
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM nodes WHERE id=?", nodeID).Scan(&name); err != nil {
		return err
	}
	message = name + "：" + message + "，动作 " + actionID
	if err := insertEvent(ctx, tx, nodeID, "panel", eventlog.Event{ID: id, Type: "ip_rotation_" + kind, OccurredAt: now, Message: message}, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO self_check_notifications(id,node_id,kind,occurred_at,config_revision,message,event_done) VALUES(?,?,?,?,0,?,1)`, id, nodeID, "ip_rotation_"+kind, now.UnixNano(), message)
	return err
}
func (p *Panel) startRotationPolicy(ctx context.Context) {
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		for ctx.Err() == nil {
			p.controlMu.Lock()
			rows, err := p.db.QueryContext(ctx, `SELECT s.node_id FROM rotation_settings s JOIN node_self_check c ON c.node_id=s.node_id WHERE s.automatic_enabled=1 AND c.enabled=1 AND c.state='blocked' AND `+automaticRotationPurpose)
			var ids []string
			if err == nil {
				for rows.Next() {
					var id string
					if rows.Scan(&id) == nil {
						ids = append(ids, id)
					}
				}
				rows.Close()
			}
			for _, id := range ids {
				_, _ = p.authorizeRotation(ctx, id, false)
			}
			p.controlMu.Unlock()
			timer := p.deps.Clock.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C():
			}
		}
	}()
}
