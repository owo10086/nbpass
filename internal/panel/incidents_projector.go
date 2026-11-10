package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

func (p *Panel) startIncidents(ctx context.Context) {
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		timer := p.deps.Clock.NewTimer(0)
		defer func() { timer.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C():
			}
			err := p.projectIncidents(ctx)
			p.operationsMu.Lock()
			p.operationsError = ""
			if err != nil {
				p.operationsError = "故障记录整理失败，等待重试"
			}
			p.operationsMu.Unlock()
			timer = p.deps.Clock.NewTimer(5 * time.Second)
		}
	}()
}

func (p *Panel) projectIncidents(ctx context.Context) error {
	view, err := p.operationsSnapshot(ctx)
	if err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	observations, err := collectOfflineIncidents(ctx, tx, view)
	if err != nil {
		return err
	}
	blocked, err := collectBlockedIncidents(ctx, tx, view)
	if err != nil {
		return err
	}
	observations = append(observations, blocked...)
	if err = attachEntryIncidentProgress(ctx, tx, observations, view); err != nil {
		return err
	}
	for _, o := range observations {
		if err = saveIncidentObservation(ctx, tx, o, view.GeneratedAt); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE operation_projection SET synced_at=? WHERE id=1", view.GeneratedAt.UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}

func nodeIncidentRefs(view operationsView, node string) []incidentRef {
	refs := []incidentRef{}
	for _, n := range view.Nodes {
		if n.ID == node {
			refs = append(refs, incidentRef{Kind: "node", ID: n.ID, Name: n.Name, Exists: true})
		}
	}
	for _, g := range view.Groups {
		if containsEntryMember(g.Members, node) {
			refs = append(refs, incidentRef{Kind: "group", ID: g.ID, Name: g.Name, Exists: true})
		}
	}
	for _, r := range view.Rules {
		if r.Enabled && containsEntryMember(r.Members, node) {
			refs = append(refs, incidentRef{Kind: "rule", ID: r.ID, Name: r.Name, Exists: true})
		}
	}
	return refs
}

func collectOfflineIncidents(ctx context.Context, tx *sql.Tx, view operationsView) ([]incidentObservation, error) {
	records := map[string]struct {
		id string
		at *time.Time
	}{}
	err := operationRows(ctx, tx, "SELECT node_id,id,offline_at FROM offline_incidents WHERE recovered_at IS NULL", func(rows *sql.Rows) error {
		var node, id string
		var at int64
		if err := rows.Scan(&node, &id, &at); err != nil {
			return err
		}
		t := time.Unix(0, at).UTC()
		records[node] = struct {
			id string
			at *time.Time
		}{id, &t}
		return nil
	})
	if err != nil {
		return nil, err
	}
	observations := []incidentObservation{}
	for _, n := range view.Nodes {
		if n.Status == "pending" {
			continue
		}
		o := incidentObservation{Key: "control:" + n.ID, Kind: "offline", Title: n.Name + "：控制连接异常", Impact: "unknown", Handling: "waiting", Refs: nodeIncidentRefs(view, n.ID), Reasons: []string{"控制连接状态不能证明业务是否中断；节点可能按缓存继续转发"}}
		if n.Connected {
			o.Closure = "异常解除"
			o.Impact = "cleared"
			o.Entries = []incidentEntry{{Key: "recovered", Stage: "控制连接恢复", Message: "已收到当前控制连接；这不是目标业务恢复证明", Source: "控制连接", At: n.LastSeen}}
		} else {
			o.Entries = []incidentEntry{{Key: "observed", Stage: "首次观察到控制连接异常", Message: "此为面板观察记录，未补造之前的故障过程", Source: "控制连接"}}
			if record, ok := records[n.ID]; ok {
				o.Entries = append(o.Entries, incidentEntry{Key: "offline:" + record.id, Stage: "离线已确认", Message: "按现有控制连接时限判定离线；业务影响待确认", Source: "离线事故", At: record.at})
			}
			if n.Status == "maintenance" {
				o.Reasons = append(o.Reasons, "维护期间沿用现有自动处理抑制规则")
			}
		}
		observations = append(observations, o)
	}
	// 已删除的节点只结束展示历史，不通过外键阻止正常删除。
	err = operationRows(ctx, tx, `SELECT source_key,title,refs,reasons FROM operation_incidents WHERE ended_at IS NULL AND kind='offline' AND substr(source_key,9) NOT IN(SELECT id FROM nodes)`, func(rows *sql.Rows) error {
		var key, title, refs, reasons string
		if e := rows.Scan(&key, &title, &refs, &reasons); e != nil {
			return e
		}
		o := incidentObservation{Key: key, Kind: "offline", Title: title, Impact: "unknown", Handling: "ended", Closure: "配置变更结束"}
		if e := json.Unmarshal([]byte(refs), &o.Refs); e != nil {
			return e
		}
		o.Entries = []incidentEntry{{Key: "deleted", Stage: "配置变更结束", Message: "节点已删除，历史保留", Source: "配置"}}
		observations = append(observations, o)
		return nil
	})
	return observations, err
}
