package panel

import (
	"context"
	"database/sql"
	"time"

	"nbpass/internal/forwarding"
)

func operationTime(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	at := time.Unix(0, v.Int64).UTC()
	return &at
}

func operationRows(ctx context.Context, q queryer, statement string, scan func(*sql.Rows) error) error {
	rows, err := q.QueryContext(ctx, statement)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// 控制状态只复制一次；数据库读取使用同一只读事务，不制造就绪样本或网络请求。
func (p *Panel) operationsSnapshot(ctx context.Context) (operationsView, error) {
	now := p.deps.Clock.Now().UTC()
	view := operationsView{GeneratedAt: now, Complete: true, Nodes: []operationNode{}, Groups: []operationGroup{}, Rules: []operationRule{}}
	p.controlMu.Lock()
	connected := map[string]bool{}
	for id, s := range p.controls {
		select {
		case <-s.done:
		default:
			connected[id] = true
		}
	}
	checks := make(map[[2]string]entryMemberState, len(p.entryChecks))
	for key, s := range p.entryChecks {
		checks[key] = s
	}
	p.controlMu.Unlock()
	tx, err := p.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return view, err
	}
	defer tx.Rollback()
	nodes := map[string]operationNode{}
	err = operationRows(ctx, tx, `SELECT n.id,n.name,n.last_seen_at,n.disconnected_at,n.maintenance_until,n.desired_revision,COALESCE(s.state,'unknown') FROM nodes n LEFT JOIN node_self_check s ON s.node_id=n.id ORDER BY n.id`, func(rows *sql.Rows) error {
		var n operationNode
		var last, disconnected, maintenance sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Name, &last, &disconnected, &maintenance, &n.Revision, &n.Blocked); err != nil {
			return err
		}
		n.LastSeen = operationTime(last)
		n.Connected = connected[n.ID] && last.Valid && now.Sub(*n.LastSeen) >= 0 && now.Sub(*n.LastSeen) < offlineWindow
		n.Status = "online"
		if !n.Connected {
			n.Status = "disconnected"
			if !last.Valid {
				n.Status = "pending"
			}
		}
		base := last
		if disconnected.Valid {
			base = disconnected
		}
		if maintenance.Valid && (!base.Valid || maintenance.Int64 > base.Int64) {
			base = maintenance
		}
		if !n.Connected && base.Valid && now.Sub(time.Unix(0, base.Int64)) > offlineWindow && now.Sub(p.startedAt) >= offlineWindow {
			n.Status = "offline"
		}
		if maintenance.Valid && now.UnixNano() < maintenance.Int64 {
			n.Status = "maintenance"
		}
		nodes[n.ID] = n
		view.Nodes = append(view.Nodes, n)
		return nil
	})
	if err != nil {
		return view, err
	}
	members, err := loadEntryGroupMembers(ctx, tx)
	if err != nil {
		return view, err
	}
	rules, err := loadRules(ctx, tx)
	if err != nil {
		return view, err
	}
	view.Counts.Nodes = len(nodes)
	view.Counts.Rules = len(rules)
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM exit_groups").Scan(&view.Counts.Exit); err != nil {
		return view, err
	}
	err = operationRows(ctx, tx, "SELECT id,name FROM entry_groups ORDER BY id", func(rows *sql.Rows) error {
		var g operationGroup
		if e := rows.Scan(&g.ID, &g.Name); e != nil {
			return e
		}
		g.Members = members[g.ID]
		g.Reasons = []operationReason{}
		g.Stage = "未发布"
		g.Redundancy = "待确认"
		view.Groups = append(view.Groups, g)
		return nil
	})
	if err != nil {
		return view, err
	}
	if err = attachOperationPublications(ctx, tx, &view, checks, nodes, now); err != nil {
		return view, err
	}
	for _, r := range rules {
		item := operationRule{ID: r.ID, Name: r.Name, GroupID: r.EntryGroupID, NodeID: r.EntryNodeID, Enabled: r.Enabled, Status: "unknown", Reasons: []operationReason{}, Members: forwarding.RuleEntryNodes(r, members)}
		if item.Members == nil {
			item.Members = []string{}
		}
		for _, id := range item.Members {
			item.Revision = max(item.Revision, nodes[id].Revision)
		}
		evaluateOperationRule(&item, checks, nodes, now)
		scopeOperationRule(&item, view.Groups)
		view.Rules = append(view.Rules, item)
	}
	view.Counts.Entry = len(view.Groups)
	for _, r := range view.Rules {
		if r.Enabled {
			view.Counts.EnabledRules++
			switch r.Status {
			case "abnormal":
				view.Counts.AbnormalRules++
			case "unknown":
				view.Counts.UnknownRules++
			}
		}
	}
	view.Counts.Alerts = view.Counts.AbnormalRules + view.Counts.UnknownRules
	return view, tx.Commit()
}
