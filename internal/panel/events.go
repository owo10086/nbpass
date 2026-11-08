package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"nbpass/internal/controlpb"
	"nbpass/internal/eventlog"
)

const eventMigration = `CREATE TABLE events (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	node_id TEXT NOT NULL,
	source TEXT NOT NULL,
	event_id TEXT NOT NULL,
	type TEXT NOT NULL,
	occurred_at INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	config_revision INTEGER NOT NULL,
	message TEXT NOT NULL,
	gap TEXT,
	UNIQUE(node_id, source, event_id)
);
CREATE INDEX events_node_time ON events(node_id, occurred_at);
CREATE INDEX events_type_time ON events(type, occurred_at);
CREATE INDEX events_time ON events(occurred_at);`

type eventWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertEvent(ctx context.Context, db eventWriter, nodeID, source string, e eventlog.Event, now time.Time) error {
	var gap any
	if e.Gap != nil {
		data, err := json.Marshal(e.Gap)
		if err != nil {
			return err
		}
		gap = string(data)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO events(node_id, source, event_id, type, occurred_at, received_at, config_revision, message, gap)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(node_id, source, event_id) DO NOTHING`, nodeID, source, e.ID, e.Type, e.OccurredAt.UnixNano(), now.UnixNano(), e.ConfigRevision, e.Message, gap)
	return err
}

// RecordEvent 是后续面板动作与同步失败的统一写入入口；空 ID 和时间由面板生成。
// 生产者只传入安全的摘要，不传凭据、请求头或第三方原始响应。
func (p *Panel) RecordEvent(ctx context.Context, nodeID string, e eventlog.Event) error {
	if e.ID == "" {
		id, _, err := p.randomCredential()
		if err != nil {
			return err
		}
		e.ID = id
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = p.deps.Clock.Now()
	}
	if err := e.Validate(); err != nil {
		return err
	}
	return insertEvent(ctx, p.db, nodeID, "panel", e, p.deps.Clock.Now())
}

func (p *Panel) acceptEvents(ctx context.Context, nodeID string, session *controlSession, report *controlpb.NodeReport) ([]string, error) {
	if len(report.Events) > eventlog.MaxBatch {
		return nil, status.Error(codes.InvalidArgument, "事件批次过大")
	}
	now := p.deps.Clock.Now()
	batch := make([]eventlog.Event, 0, len(report.Events))
	for _, wire := range report.Events {
		e, err := eventlog.FromProto(wire)
		if err != nil || e.OccurredAt.After(now.Add(5*time.Minute)) {
			return nil, status.Error(codes.InvalidArgument, "事件字段或时间无效")
		}
		batch = append(batch, e)
	}
	if len(batch) == 0 && session.eventsLogged {
		return nil, nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "事件存储暂不可用")
	}
	defer tx.Rollback()
	if !session.eventsLogged {
		id, _, randomErr := p.randomCredential()
		if randomErr != nil {
			return nil, status.Error(codes.Unavailable, "无法生成事件 ID")
		}
		err = insertEvent(ctx, tx, nodeID, "panel", eventlog.Event{ID: id, Type: "node_online", OccurredAt: now, Message: "节点控制连接已建立"}, now)
	}
	acked := make([]string, 0, len(batch))
	for _, e := range batch {
		if err != nil {
			break
		}
		acked = append(acked, e.ID)
		// 服务端再次执行 24 小时上限；过期内容只留下缺口摘要并确认消费。
		if e.Type != "event_gap" && e.OccurredAt.Before(now.Add(-eventlog.BackfillWindow)) {
			e = eventlog.Event{ID: "expired:" + e.ID, Type: "event_gap", OccurredAt: now, Message: "拒收超过 24 小时的离线事件", Gap: &eventlog.Gap{Count: 1, From: e.OccurredAt, To: e.OccurredAt, Reason: "expired"}}
		}
		err = insertEvent(ctx, tx, nodeID, "node", e, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "事件存储暂不可用")
	}
	session.eventsLogged = true
	return acked, nil
}

type eventRow struct {
	Sequence   int64     `json:"sequence"`
	NodeID     string    `json:"node_id"`
	NodeName   string    `json:"node_name"`
	Source     string    `json:"source"`
	ReceivedAt time.Time `json:"received_at"`
	eventlog.Event
}

func (p *Panel) listEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	where := []string{"1 = 1"}
	args := []any{}
	for key, column := range map[string]string{"node_id": "e.node_id", "type": "e.type"} {
		if v := query.Get(key); v != "" {
			if len(v) > 128 {
				problem(w, 400, "筛选条件过长")
				return
			}
			where = append(where, column+" = ?")
			args = append(args, v)
		}
	}
	var from, to time.Time
	for _, key := range []string{"from", "to"} {
		if v := query.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				problem(w, 400, "时间须使用 RFC3339 格式")
				return
			}
			operator := ">="
			if key == "to" {
				operator = "<="
				to = t
			} else {
				from = t
			}
			where = append(where, "e.occurred_at "+operator+" ?")
			args = append(args, t.UnixNano())
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		problem(w, 400, "开始时间不能晚于结束时间")
		return
	}
	limit := 100
	if v := query.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			problem(w, 400, "每页数量须为 1–1000")
			return
		}
		limit = n
	}
	if v := query.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			problem(w, 400, "分页游标无效")
			return
		}
		where = append(where, "e.sequence < ?")
		args = append(args, n)
	}
	args = append(args, limit+1)
	rows, err := p.db.QueryContext(r.Context(), `SELECT e.sequence, e.node_id, COALESCE(n.name, ''), e.source, e.event_id, e.type, e.occurred_at, e.received_at, e.config_revision, e.message, e.gap
		FROM events e LEFT JOIN nodes n ON n.id = e.node_id WHERE `+strings.Join(where, " AND ")+` ORDER BY e.sequence DESC LIMIT ?`, args...)
	if err != nil {
		problem(w, 503, "事件存储暂不可用")
		return
	}
	defer rows.Close()
	result := []eventRow{}
	for rows.Next() {
		var row eventRow
		var occurred, received int64
		var gap sql.NullString
		if err := rows.Scan(&row.Sequence, &row.NodeID, &row.NodeName, &row.Source, &row.ID, &row.Type, &occurred, &received, &row.ConfigRevision, &row.Message, &gap); err != nil {
			problem(w, 503, "无法读取事件")
			return
		}
		row.OccurredAt, row.ReceivedAt = time.Unix(0, occurred).UTC(), time.Unix(0, received).UTC()
		if gap.Valid {
			if err := json.Unmarshal([]byte(gap.String), &row.Gap); err != nil {
				problem(w, 503, "事件缺口损坏")
				return
			}
		}
		result = append(result, row)
	}
	if rows.Err() != nil {
		problem(w, 503, "无法读取事件")
		return
	}
	var next any
	if len(result) > limit {
		result = result[:limit]
		next = result[len(result)-1].Sequence
	}
	respond(w, 200, map[string]any{"events": result, "next_before": next})
}
