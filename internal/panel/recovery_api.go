package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"nbpass/internal/dnsprovider"
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
	var at *time.Time
	if restored.Valid {
		v := time.Unix(0, restored.Int64).UTC()
		at = &v
	}
	respond(w, 200, map[string]any{"restored_at": at, "nodes": nodes, "entry_groups": groups})
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
	rows, err = p.db.QueryContext(ctx, "SELECT group_id FROM recovery_groups WHERE verified=0")
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
		_, err = p.db.ExecContext(ctx, `INSERT INTO recovery_groups(group_id,verified,last_error) VALUES(?,?,?) ON CONFLICT(group_id) DO UPDATE SET verified=excluded.verified,last_error=excluded.last_error`, gid, e == nil, message)
	}
	if err != nil {
		problem(w, 503, "无法保存核对结果")
		return
	}
	for _, gid := range gids {
		p.coordinator.wake(gid)
	}
	respond(w, 200, map[string]bool{"checked": true})
}

func (p *Panel) reconcileRecoveryGroup(ctx context.Context, gid string) error {
	var pending int
	err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM entry_group_members m LEFT JOIN recovery_nodes r ON r.node_id=m.node_id WHERE m.group_id=? AND r.node_id IS NOT NULL AND (r.high_water IS NULL OR r.verified=0)`, gid).Scan(&pending)
	if err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("入口组存在无法核对的节点，自动写继续暂停")
	}
	b, err := p.loadManaged(ctx, gid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	cred, err := p.dnsCredential(ctx, b.AccountID)
	if err != nil {
		return errors.New("DNSPod 凭据不可用，自动写继续暂停")
	}
	readCtx, cancel := p.dnsContext(ctx)
	actual, err := p.deps.DNSProvider.ReadRecord(readCtx, dnsprovider.Credentials{TokenID: cred.TokenID, Token: cred.Token}, dnsprovider.Key{Domain: b.Domain, RecordID: b.RecordID})
	cancel()
	if err != nil {
		return errors.New("DNSPod 读回失败，自动写继续暂停")
	}
	if _, err = recordWarnings(actual, b.RecordID, b.LineID); err != nil || actual.Record.Name != b.Name {
		return errors.New("受管记录身份、线路或类型无法核对")
	}
	var count int
	var nodeID string
	err = p.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(MIN(m.node_id),'') FROM entry_group_members m JOIN node_regions n ON n.node_id=m.node_id WHERE m.group_id=? AND n.public_ipv4=?`, gid, actual.Record.Value).Scan(&count, &nodeID)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("实际 DNS 无法唯一对应已核对的入口节点，自动写继续暂停")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 旧队列封存为已处理，不能重新投递或覆盖服务商当前值。
	_, err = tx.ExecContext(ctx, `UPDATE entry_actions SET stage=?,last_error='恢复核对已封存旧动作，未重放 DNS 写入' WHERE group_id=? AND stage<>?`, EntryWritten, gid, EntryWritten)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE managed_records SET current_value=?,current_ttl=? WHERE group_id=?`, actual.Record.Value, actual.Record.TTL, gid)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE entry_publications SET desired_version=desired_version+1,active_node_id=?,expected_value=?,current_action_id='',written_at=? WHERE group_id=? AND (active_node_id<>? OR expected_value<>? OR current_action_id<>'')`, nodeID, actual.Record.Value, p.deps.Clock.Now().UnixNano(), gid, nodeID, actual.Record.Value)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE entry_groups SET publication_status=? WHERE id=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=?)", EntryWritten, gid, gid)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
