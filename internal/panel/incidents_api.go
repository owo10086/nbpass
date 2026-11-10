package panel

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (p *Panel) incidentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/incidents", p.listIncidents)
	mux.HandleFunc("GET /api/incidents/{id}", p.getIncident)
	mux.HandleFunc("GET /api/incidents/{id}/timeline", p.getIncidentTimeline)
}
func incidentPage(r *http.Request) (int, int64, bool) {
	limit := 100
	before := int64(0)
	for key, target := range map[string]*int64{"limit": nil, "before": &before} {
		if value := r.URL.Query().Get(key); value != "" {
			n, e := strconv.ParseInt(value, 10, 64)
			if e != nil || n < 1 || (key == "limit" && n > 100) {
				return 0, 0, false
			}
			if target == nil {
				limit = int(n)
			} else {
				*target = n
			}
		}
	}
	return limit, before, true
}
func (p *Panel) listIncidents(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := incidentPage(r)
	if !ok {
		problem(w, 400, "分页条件无效，每页为 1–100 条")
		return
	}
	where := []string{"1=1"}
	args := []any{}
	q := r.URL.Query()
	if before > 0 {
		where = append(where, "sequence<?")
		args = append(args, before)
	}
	switch s := q.Get("status"); s {
	case "":
	case "open":
		where = append(where, "ended_at IS NULL")
	case "ended":
		where = append(where, "ended_at IS NOT NULL")
	case "manual", "waiting", "processing":
		where = append(where, "ended_at IS NULL AND handling=?")
		args = append(args, s)
	default:
		problem(w, 400, "故障状态无效")
		return
	}
	switch kind := q.Get("kind"); kind {
	case "":
	case "offline", "blocked", "rule", "dns":
		where = append(where, "kind=?")
		args = append(args, kind)
	default:
		problem(w, 400, "故障类型无效")
		return
	}
	for key, kind := range map[string]string{"node_id": "node", "group_id": "group", "rule_id": "rule", "watch_id": "watch"} {
		if v := q.Get(key); v != "" {
			if len(v) > 128 {
				problem(w, 400, "筛选条件过长")
				return
			}
			where = append(where, `EXISTS(SELECT 1 FROM json_each(refs) WHERE json_extract(value,'$.kind')=? AND json_extract(value,'$.id')=?)`)
			args = append(args, kind, v)
		}
	}
	var from, to time.Time
	for _, key := range []string{"from", "to"} {
		if v := q.Get(key); v != "" {
			at, e := time.Parse(time.RFC3339Nano, v)
			if e != nil {
				problem(w, 400, "时间须为 RFC3339 格式")
				return
			}
			op := ">="
			if key == "to" {
				op = "<="
				to = at
			} else {
				from = at
			}
			where = append(where, "started_at"+op+"?")
			args = append(args, at.UnixNano())
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		problem(w, 400, "开始时间不能晚于结束时间")
		return
	}
	args = append(args, limit+1)
	rows, err := p.db.QueryContext(r.Context(), "SELECT "+incidentColumns+" FROM operation_incidents WHERE "+strings.Join(where, " AND ")+" ORDER BY sequence DESC LIMIT ?", args...)
	if err != nil {
		problem(w, 503, "故障记录暂不可用")
		return
	}
	items := []incidentRecord{}
	for rows.Next() {
		item, e := scanIncident(rows)
		if e != nil {
			rows.Close()
			problem(w, 503, "故障记录损坏")
			return
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		problem(w, 503, "故障记录暂不可用")
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	synced, message, _ := p.incidentProjectionStatus(r.Context())
	respond(w, 200, map[string]any{"incidents": items, "next_before": next, "synced_at": synced, "sync_error": message})
}
func (p *Panel) getIncident(w http.ResponseWriter, r *http.Request) {
	item, err := scanIncident(p.db.QueryRowContext(r.Context(), "SELECT "+incidentColumns+" FROM operation_incidents WHERE sequence=?", r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "故障不存在或历史已过期")
		return
	}
	if err != nil {
		problem(w, 503, "故障记录暂不可用")
		return
	}
	existing := map[string]bool{}
	err = operationRows(r.Context(), p.db, `SELECT 'node',id FROM nodes UNION ALL SELECT 'group',id FROM entry_groups UNION ALL SELECT 'rule',id FROM forward_rules UNION ALL SELECT 'watch',id FROM dns_watches`, func(rows *sql.Rows) error {
		var kind, id string
		if e := rows.Scan(&kind, &id); e != nil {
			return e
		}
		existing[kind+":"+id] = true
		return nil
	})
	if err != nil {
		problem(w, 503, "关联对象暂不可用")
		return
	}
	for i := range item.Refs {
		item.Refs[i].Exists = existing[item.Refs[i].Kind+":"+item.Refs[i].ID]
	}
	respond(w, 200, item)
}
func (p *Panel) getIncidentTimeline(w http.ResponseWriter, r *http.Request) {
	limit, before, ok := incidentPage(r)
	if !ok {
		problem(w, 400, "分页条件无效")
		return
	}
	var exists int
	if err := p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM operation_incidents WHERE sequence=?", r.PathValue("id")).Scan(&exists); err != nil {
		problem(w, 503, "故障记录暂不可用")
		return
	}
	if exists == 0 {
		problem(w, 404, "故障不存在或历史已过期")
		return
	}
	where := "incident_id=?"
	args := []any{r.PathValue("id")}
	if before > 0 {
		where += " AND sequence<?"
		args = append(args, before)
	}
	args = append(args, limit+1)
	rows, err := p.db.QueryContext(r.Context(), "SELECT sequence,stage,message,source,version,occurred_at,observed_at FROM operation_timeline WHERE "+where+" ORDER BY sequence DESC LIMIT ?", args...)
	if err != nil {
		problem(w, 503, "时间线暂不可用")
		return
	}
	defer rows.Close()
	items := []incidentEntry{}
	for rows.Next() {
		var e incidentEntry
		var at sql.NullInt64
		var observed int64
		if err := rows.Scan(&e.Sequence, &e.Stage, &e.Message, &e.Source, &e.Version, &at, &observed); err != nil {
			problem(w, 503, "时间线读取失败")
			return
		}
		e.At = operationTime(at)
		e.ObservedAt = time.Unix(0, observed).UTC()
		items = append(items, e)
	}
	if rows.Err() != nil {
		problem(w, 503, "时间线读取失败")
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].Sequence
	}
	respond(w, 200, map[string]any{"entries": items, "next_before": next})
}
