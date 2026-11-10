package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"nbpass/internal/eventlog"
)

// 迁移 43：观测历史与协调器队列分离；每次开启使用新的去重代次。
const entryObservationMigration = `ALTER TABLE entry_groups ADD COLUMN observation_enabled INTEGER NOT NULL DEFAULT 0 CHECK(observation_enabled IN (0,1));
ALTER TABLE entry_groups ADD COLUMN observation_generation INTEGER NOT NULL DEFAULT 0;
CREATE TABLE entry_observations (
 id TEXT PRIMARY KEY, group_id TEXT NOT NULL REFERENCES entry_groups(id) ON DELETE CASCADE,
 node_id TEXT NOT NULL, kind TEXT NOT NULL, message TEXT NOT NULL, created_at INTEGER NOT NULL
);
CREATE INDEX entry_observations_recent ON entry_observations(group_id,created_at DESC);`

const entryObserved = "观测中"

func automaticEntryKind(kind string) bool {
	switch kind {
	case "switch", "failback", "auto_withdraw", "auto_restore", "writeback":
		return true
	}
	// address_update 是已授权动作的收尾，sync_address 是手动操作。
	return false
}

func groupObserving(ctx context.Context, q recoveryQuerier, group string) (bool, error) {
	var enabled bool
	err := q.QueryRowContext(ctx, "SELECT observation_enabled FROM entry_groups WHERE id=?", group).Scan(&enabled)
	return enabled, err
}

// 返回 true 表示已记录，应在提交事务后返回，不能继续改变期望或创建工作流。
func (p *Panel) observeEntryTx(ctx context.Context, tx *sql.Tx, group, node, kind, identity, detail string) (bool, error) {
	var enabled bool
	var generation int64
	var name string
	err := tx.QueryRowContext(ctx, "SELECT observation_enabled,observation_generation,name FROM entry_groups WHERE id=?", group).Scan(&enabled, &generation, &name)
	if err != nil || !enabled {
		return false, err
	}
	key := fmt.Sprintf("entry-observation:%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s:%s", group, generation, node, kind, identity))))
	message := fmt.Sprintf("入口组 %s 观测中：本来会%s", name, detail)
	now := p.deps.Clock.Now()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entry_observations(id,group_id,node_id,kind,message,created_at) VALUES(?,?,?,?,?,?)`, key, group, node, kind, message, now.UnixNano())
	if err != nil {
		return true, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return true, err
	}
	if err = insertEvent(ctx, tx, node, "panel", eventlog.Event{ID: key, Type: "entry_observation", OccurredAt: now, Message: message}, now); err != nil {
		return true, err
	}
	// 沿用持久通知待办；Telegram 失败不会回滚观测开关，也不阻塞 DNS。
	_, err = tx.ExecContext(ctx, `INSERT INTO self_check_notifications(id,node_id,kind,occurred_at,config_revision,message,event_done) VALUES(?,?, 'entry_observation',?,0,?,1)`, key, node, now.UnixNano(), message)
	return true, err
}

func observationDetail(kind, node, value string) string {
	label := map[string]string{"switch": "切到", "failback": "回切到", "auto_withdraw": "摘除", "auto_restore": "加回", "writeback": "写回记录偏差", "rotate_ip": "自动换 IP"}[kind]
	return fmt.Sprintf("%s %s %s", label, node, value)
}

func (p *Panel) observePrimaryTx(ctx context.Context, tx *sql.Tx, req EntryActionRequest) (bool, error) {
	if !automaticEntryKind(req.Kind) {
		return false, nil
	}
	return p.observeEntryTx(ctx, tx, req.GroupID, req.NodeID, req.Kind, fmt.Sprintf("%d:%s", req.ExpectedVersion, req.IPv4), observationDetail(req.Kind, req.NodeID, req.IPv4))
}

func (p *Panel) observeRecordTx(ctx context.Context, tx *sql.Tx, req RecordActionRequest) (bool, error) {
	if req.GroupID == "" || !automaticEntryKind(req.Kind) {
		return false, nil
	}
	data, err := json.Marshal(req.Records)
	if err != nil {
		return false, err
	}
	details := make([]string, 0, len(req.Records))
	for _, change := range req.Records {
		state := "暂停"
		if change.Enabled {
			state = "启用"
		}
		details = append(details, fmt.Sprintf("记录 %s，地址 %s，%s", change.ManagedRecordID, change.Value, state))
	}
	return p.observeEntryTx(ctx, tx, req.GroupID, req.NodeID, req.Kind, fmt.Sprintf("%d:%s", req.ExpectedVersion, data), observationDetail(req.Kind, req.NodeID, strings.Join(details, "；")))
}

// 调用者持 controlMu；早于跨组切离、额度占用，也用于首次许可投递的最终复核。
func (p *Panel) observeRotationTx(ctx context.Context, tx *sql.Tx, node, identity string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT g.id FROM entry_groups g JOIN entry_group_members m ON m.group_id=g.id WHERE m.node_id=? AND g.observation_enabled=1 ORDER BY g.id`, node)
	if err != nil {
		return false, err
	}
	var groups []string
	for rows.Next() {
		var group string
		if err = rows.Scan(&group); err != nil {
			break
		}
		groups = append(groups, group)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, group := range groups {
		if _, err = p.observeEntryTx(ctx, tx, group, node, "rotate_ip", identity, observationDetail("rotate_ip", node, "（共享节点的所有自动用途暂停）")); err != nil {
			return true, err
		}
	}
	return len(groups) > 0, nil
}

func (p *Panel) observationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("PUT /api/entry-groups/{id}/observation", p.putObservation)
	mux.HandleFunc("GET /api/entry-groups/{id}/observation", p.getObservation)
}

func (p *Panel) getObservation(w http.ResponseWriter, r *http.Request) {
	tx, err := p.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		problem(w, 503, "无法读取观测模式")
		return
	}
	defer tx.Rollback()
	enabled, err := groupObserving(r.Context(), tx, r.PathValue("id"))
	if err != nil {
		p.observationError(w, err)
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,node_id,kind,message,created_at FROM entry_observations WHERE group_id=? ORDER BY created_at DESC,rowid DESC LIMIT 50`, r.PathValue("id"))
	if err != nil {
		problem(w, 503, "无法读取观测动作")
		return
	}
	defer rows.Close()
	type observation struct {
		ID        string    `json:"id"`
		NodeID    string    `json:"node_id"`
		Kind      string    `json:"kind"`
		Message   string    `json:"message"`
		CreatedAt time.Time `json:"created_at"`
	}
	items := []observation{}
	for rows.Next() {
		var item observation
		var at int64
		if err = rows.Scan(&item.ID, &item.NodeID, &item.Kind, &item.Message, &at); err != nil {
			break
		}
		item.CreatedAt = time.Unix(0, at).UTC()
		items = append(items, item)
	}
	if err != nil || rows.Err() != nil {
		problem(w, 503, "无法读取观测动作")
		return
	}
	respond(w, 200, map[string]any{"enabled": enabled, "actions": items})
}

func (p *Panel) putObservation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		problem(w, 400, "请指定观测模式开关")
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.rotationDispatchMu.Lock()
	defer p.rotationDispatchMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "无法保存观测模式")
		return
	}
	defer tx.Rollback()
	group := r.PathValue("id")
	old, err := groupObserving(r.Context(), tx, group)
	if err == nil && old != *body.Enabled {
		_, err = tx.ExecContext(r.Context(), `UPDATE entry_groups SET observation_enabled=?,observation_generation=observation_generation+? WHERE id=?`, *body.Enabled, *body.Enabled, group)
		if err == nil && *body.Enabled {
			err = p.cancelObservedActionsTx(r.Context(), tx, group)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		p.observationError(w, err)
		return
	}
	respond(w, 200, map[string]bool{"enabled": *body.Enabled})
}

func (p *Panel) observationError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "入口组不存在")
		return
	}
	problem(w, 503, "观测模式暂不可用")
}
