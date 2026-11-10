package panel

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

func (p *Panel) recoveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/recovery", p.getRecovery)
	mux.HandleFunc("POST /api/recovery/reconcile", p.reconcileRecovery)
}

type recoveryNodeView struct {
	NodeID                  string  `json:"node_id"`
	HighWater               *uint64 `json:"high_water"`
	HistorySince            int64   `json:"history_since"`
	Verified                bool    `json:"verified"`
	AutomaticRotationPaused bool    `json:"automatic_rotation_paused"`
}
type recoveryGroupView struct {
	GroupID   string `json:"group_id"`
	Verified  bool   `json:"verified"`
	LastError string `json:"last_error"`
}

func (p *Panel) getRecovery(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	var restored sql.NullInt64
	err := p.db.QueryRowContext(r.Context(), "SELECT restored_at FROM recovery_state WHERE id=1").Scan(&restored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		problem(w, 503, "无法读取恢复状态")
		return
	}
	nodes := []recoveryNodeView{}
	groups := []recoveryGroupView{}
	rows, err := p.db.QueryContext(r.Context(), "SELECT node_id,high_water,history_since,verified FROM recovery_nodes ORDER BY node_id")
	if err == nil {
		for rows.Next() {
			var n recoveryNodeView
			var high sql.NullInt64
			if err = rows.Scan(&n.NodeID, &high, &n.HistorySince, &n.Verified); err != nil {
				break
			}
			if high.Valid {
				v := uint64(high.Int64)
				n.HighWater = &v
			}
			nodes = append(nodes, n)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	for i := range nodes {
		if err != nil {
			break
		}
		var allowed bool
		allowed, err = p.recoveryNodeAllowed(r.Context(), p.db, nodes[i].NodeID, true)
		nodes[i].AutomaticRotationPaused = !allowed
	}
	if err == nil {
		rows, err = p.db.QueryContext(r.Context(), "SELECT group_id,verified,last_error FROM recovery_groups ORDER BY group_id")
		if err == nil {
			for rows.Next() {
				var g recoveryGroupView
				if err = rows.Scan(&g.GroupID, &g.Verified, &g.LastError); err != nil {
					break
				}
				groups = append(groups, g)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
	}
	if err != nil {
		problem(w, 503, "无法读取恢复状态")
		return
	}
	// 逐条暴露核对结果，节点级记录的错误也能在既有恢复页接口读取。
	records := []map[string]any{}
	recordRows, err := p.db.QueryContext(r.Context(), `SELECT m.id,m.owner_kind,coalesce(m.group_id,''),coalesce(m.node_id,''),m.domain,m.record_id,m.binding_generation,coalesce(r.verified,0),coalesce(r.last_error,'') FROM managed_records m JOIN recovery_records r ON r.managed_record_id=m.id ORDER BY m.id`)
	if err != nil {
		problem(w, 503, "无法读取逐条恢复状态")
		return
	}
	for recordRows.Next() {
		var id, kind, group, node, domain, record, message string
		var generation int64
		var verified bool
		if err = recordRows.Scan(&id, &kind, &group, &node, &domain, &record, &generation, &verified, &message); err != nil {
			break
		}
		records = append(records, map[string]any{"managed_record_id": id, "owner_kind": kind, "group_id": group, "node_id": node, "domain": domain, "record_id": record, "binding_generation": generation, "verified": verified, "last_error": message})
	}
	if err == nil {
		err = recordRows.Err()
	}
	recordRows.Close()
	if err != nil {
		problem(w, 503, "无法读取逐条恢复状态")
		return
	}
	var at *time.Time
	if restored.Valid {
		v := time.Unix(0, restored.Int64).UTC()
		at = &v
	}
	respond(w, 200, map[string]any{"restored_at": at, "nodes": nodes, "entry_groups": groups, "records": records})
}

// 核对操作不改 DNS：读取当前服务商记录，采用能由已核对成员证明的现状。
func (p *Panel) reconcileRecovery(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !b.Confirm {
		problem(w, 400, "请确认核对节点与 DNSPod 实际状态")
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	ctx := r.Context()
	var active int
	if err := p.db.QueryRowContext(ctx, "SELECT count(*) FROM recovery_state").Scan(&active); err != nil {
		problem(w, 503, "无法读取恢复状态")
		return
	}
	if active == 0 {
		respond(w, 200, map[string]bool{"checked": false})
		return
	}
	rows, err := p.db.QueryContext(ctx, "SELECT node_id FROM recovery_nodes")
	var ids []string
	if err == nil {
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	p.controlMu.Lock()
	for _, id := range ids {
		if err != nil {
			break
		}
		if p.controls[id] == nil {
			continue
		}
		_, err = p.db.ExecContext(ctx, `UPDATE recovery_nodes SET verified=1 WHERE node_id=? AND high_water IS NOT NULL AND EXISTS(SELECT 1 FROM nodes n JOIN node_snapshots s ON s.node_id=n.id WHERE n.id=? AND n.applied_revision=n.desired_revision AND n.applied_digest=s.digest AND n.apply_error='' AND n.last_seen_at>?)`, id, id, p.deps.Clock.Now().Add(-90*time.Second).UnixNano())
	}
	p.controlMu.Unlock()
	if err != nil {
		problem(w, 503, "无法核对节点状态")
		return
	}
	rows, err = p.db.QueryContext(ctx, `SELECT e.id FROM entry_groups e LEFT JOIN recovery_groups g ON g.group_id=e.id WHERE g.verified=0 OR EXISTS(SELECT 1 FROM managed_records m LEFT JOIN recovery_records r ON r.managed_record_id=m.id WHERE m.group_id=e.id AND (coalesce(r.verified,0)=0 OR r.binding_generation<>m.binding_generation)) ORDER BY e.id`)
	var gids []string
	if err == nil {
		for rows.Next() {
			var gid string
			if err = rows.Scan(&gid); err != nil {
				break
			}
			gids = append(gids, gid)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	for _, gid := range gids {
		if err != nil {
			break
		}
		var e error
		e = p.reconcileRecoveryGroup(ctx, gid)
		message := ""
		if e != nil {
			message = e.Error()
		}
		// 成功放行已在最后复核事务内完成；这里只保存失败，不能覆盖并发失效。
		if e != nil {
			_, err = p.db.ExecContext(ctx, `INSERT INTO recovery_groups(group_id,verified,last_error) VALUES(?,0,?) ON CONFLICT(group_id) DO UPDATE SET verified=0,last_error=excluded.last_error`, gid, message)
		}
	}
	if err != nil {
		problem(w, 503, "无法保存核对结果")
		return
	}
	// 节点级记录不属于任何组，也必须通过同一逐记录核对后才能放行。
	recordRows, e := p.db.QueryContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE owner_kind='node' ORDER BY id")
	if e != nil {
		problem(w, 503, "无法读取节点受管记录")
		return
	}
	var records []managedRecord
	for recordRows.Next() {
		b, scanErr := scanManaged(recordRows)
		if scanErr != nil {
			e = scanErr
			break
		}
		records = append(records, b)
	}
	if e == nil {
		e = recordRows.Err()
	}
	recordRows.Close()
	if e != nil {
		problem(w, 503, "无法读取节点受管记录")
		return
	}
	for _, b := range records {
		_ = p.reconcileRecoveryRecord(ctx, b)
		p.coordinator.wake("node:" + b.NodeID)
	}
	for _, gid := range gids {
		p.coordinator.wake(gid)
	}
	respond(w, 200, map[string]bool{"checked": true})
}
