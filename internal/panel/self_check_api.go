package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"nbpass/internal/controlpb"
	"nbpass/internal/selfcheck"
)

func (p *Panel) selfCheckRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/self-check", p.getSelfCheckSettings)
	mux.HandleFunc("PUT /api/settings/self-check", p.putSelfCheckSettings)
	mux.HandleFunc("GET /api/nodes/{id}/self-check", p.getSelfCheck)
	mux.HandleFunc("PUT /api/nodes/{id}/self-check", p.putNodeSelfCheck)
}
func loadSelfCheckSettings(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (selfcheck.Settings, error) {
	s := selfcheck.Defaults()
	var data []byte
	err := q.QueryRowContext(ctx, "SELECT settings FROM self_check_settings WHERE id=1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}
func (p *Panel) getSelfCheckSettings(w http.ResponseWriter, r *http.Request) {
	s, err := loadSelfCheckSettings(r.Context(), p.db)
	if err != nil {
		problem(w, 503, "自测设置暂不可用")
		return
	}
	respond(w, 200, s)
}
func (p *Panel) putSelfCheckSettings(w http.ResponseWriter, r *http.Request) {
	var s selfcheck.Settings
	if !decode(w, r, &s) {
		return
	}
	if err := s.Validate(); err != nil {
		problem(w, 400, err.Error())
		return
	}
	data, _ := json.Marshal(s)
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), "INSERT INTO self_check_settings(id,settings) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET settings=excluded.settings", data)
		return err
	}) {
		return
	}
	respond(w, 204, nil)
}
func (p *Panel) putNodeSelfCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		problem(w, 400, "请指定自测开关")
		return
	}
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM nodes WHERE id=?", r.PathValue("id")).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &forwardingError{404, "节点不存在"}
		}
		_, err := tx.ExecContext(r.Context(), "INSERT INTO node_self_check(node_id,enabled) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET enabled=excluded.enabled", r.PathValue("id"), *body.Enabled)
		return err
	}) {
		return
	}
	respond(w, 204, nil)
}
func (p *Panel) compileSelfChecks(ctx context.Context, tx *sql.Tx, snapshots map[string]*controlpb.ConfigSnapshot) error {
	settings, err := loadSelfCheckSettings(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT node_id,enabled FROM node_self_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var enabled bool
		if err := rows.Scan(&id, &enabled); err != nil {
			return err
		}
		if s := snapshots[id]; s != nil {
			s.SelfCheckConfig, err = json.Marshal(selfcheck.Config{Enabled: enabled, Settings: settings})
			if err != nil {
				return err
			}
		}
	}
	return rows.Err()
}
func (p *Panel) getSelfCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		problem(w, 503, "自测存储暂不可用")
		return
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if exists == 0 {
		problem(w, 404, "节点不存在")
		return
	}
	var enabled bool
	state := "unknown"
	var consecutive int
	err = tx.QueryRowContext(ctx, "SELECT enabled,state,consecutive FROM node_self_check WHERE node_id=?", id).Scan(&enabled, &state, &consecutive)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "自测状态暂不可用")
		return
	}
	limit := 100
	var before int64 = 1<<63 - 1
	for key, target := range map[string]*int64{"before": &before} {
		if v := r.URL.Query().Get(key); v != "" {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 1 {
				problem(w, 400, "自测分页游标无效")
				return
			}
			*target = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 1000 {
			problem(w, 400, "每页数量须为 1–1000")
			return
		}
		limit = n
	}
	rows, err := tx.QueryContext(ctx, "SELECT raw,final_result,evidence_source FROM self_check_rounds WHERE node_id=? AND sequence<? ORDER BY sequence DESC LIMIT ?", id, before, limit+1)
	if err != nil {
		problem(w, 503, "自测历史暂不可用")
		return
	}
	defer rows.Close()
	type roundView struct {
		selfcheck.Round
		FinalResult    string `json:"final_result"`
		EvidenceSource string `json:"evidence_source"`
	}
	rounds := []roundView{}
	for rows.Next() {
		var data []byte
		var v roundView
		if err := rows.Scan(&data, &v.FinalResult, &v.EvidenceSource); err != nil {
			problem(w, 503, "自测历史暂不可用")
			return
		}
		if err := json.Unmarshal(data, &v.Round); err != nil {
			problem(w, 503, "自测历史损坏")
			return
		}
		rounds = append(rounds, v)
	}
	if rows.Err() != nil {
		problem(w, 503, "自测历史暂不可用")
		return
	}
	var next any
	if len(rounds) > limit {
		rounds = rounds[:limit]
		next = rounds[len(rounds)-1].Sequence
	}
	settings, err := loadSelfCheckSettings(ctx, tx)
	if err != nil {
		problem(w, 503, "自测设置暂不可用")
		return
	}
	if err := tx.Commit(); err != nil {
		problem(w, 503, "自测读取暂不可用")
		return
	}
	respond(w, 200, map[string]any{"enabled": enabled, "state": state, "consecutive_abnormal": consecutive, "rounds": rounds, "settings": settings, "next_before": next})
}
