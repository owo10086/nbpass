package panel

import (
	"context"
	"database/sql"
	"errors"
	"nbpass/internal/publicip"
	"net/http"
	"time"
)

type allMemberPublication struct {
	RestoreGeneration int64          `json:"-"`
	WithdrawnAt       *time.Time     `json:"withdrawn_at"`
	LastError         string         `json:"last_error"`
	NodeID            string         `json:"node_id"`
	Record            *managedRecord `json:"record"`
	ExpectedValue     string         `json:"expected_value"`
	ExpectedEnabled   bool           `json:"expected_enabled"`
	AddressPending    bool           `json:"address_pending"`
	Reason            string         `json:"reason"`
	ManualLocked      bool           `json:"manual_locked"`
	RemovalActionID   string         `json:"removal_action_id"`
	PropagationStatus string         `json:"propagation_status"`
}

func loadAllMembers(ctx context.Context, q interface {
	recoveryQuerier
	queryer
}, g string) ([]allMemberPublication, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.node_id,coalesce(s.expected_value,''),coalesce(s.expected_enabled,0),coalesce(s.address_pending,1),coalesce(s.reason,'未绑定记录'),coalesce(s.manual_locked,0),coalesce(s.removal_action_id,''),coalesce(s.propagation_status,''),coalesce(s.last_action_error,''),s.withdrawn_at,coalesce(s.restore_generation,0) FROM entry_group_members m LEFT JOIN entry_member_publications s ON s.group_id=m.group_id AND s.node_id=m.node_id WHERE m.group_id=? ORDER BY m.position`, g)
	if err != nil {
		return nil, err
	}
	members := []allMemberPublication{}
	for rows.Next() {
		var m allMemberPublication
		var withdrawn sql.NullInt64
		if err = rows.Scan(&m.NodeID, &m.ExpectedValue, &m.ExpectedEnabled, &m.AddressPending, &m.Reason, &m.ManualLocked, &m.RemovalActionID, &m.PropagationStatus, &m.LastError, &withdrawn, &m.RestoreGeneration); err != nil {
			break
		}
		if withdrawn.Valid {
			at := time.Unix(0, withdrawn.Int64).UTC()
			m.WithdrawnAt = &at
		}
		members = append(members, m)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range members {
		b, e := scanManaged(q.QueryRowContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE owner_kind='member' AND group_id=? AND node_id=?", g, members[i].NodeID))
		if e == nil {
			members[i].Record = &b
		} else if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
	}
	return members, nil
}
func requireNoGroupAction(ctx context.Context, tx *sql.Tx, g string) error {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entry_actions WHERE group_id=? AND stage IN `+pendingRecordStages+`)`, g).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return &forwardingError{409, "入口动作尚未核对完成，请稍后重试"}
	}
	return nil
}

// submitMemberActionTx 是成员级策略的统一接缝。调用者持 entryMu，先更新成员期望，
// 同事务落盘版本与记录动作；提交后 wake。票 05/06 可复用，自动策略自行判定。
func (p *Panel) submitMemberActionTx(ctx context.Context, tx *sql.Tx, g, node, kind string, version int64, changes []ManagedRecordChange) (EntryAction, error) {
	if err := requireNoGroupAction(ctx, tx, g); err != nil {
		return EntryAction{}, err
	}
	for _, change := range changes {
		if _, err := tx.ExecContext(ctx, `UPDATE entry_member_publications SET last_action_error='' WHERE group_id=? AND node_id=(SELECT node_id FROM managed_records WHERE id=?)`, g, change.ManagedRecordID); err != nil {
			return EntryAction{}, err
		}
	}
	a, err := p.submitRecordActionTx(ctx, tx, RecordActionRequest{GroupID: g, NodeID: node, Kind: kind, ExpectedVersion: version, Records: changes, MinimumEnabled: 1})
	if err != nil || a.Stage == entryObserved {
		return a, err
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE dns_watches SET expected_version=?,propagation_status=?,observation_deadline=NULL,next_sample_at=? WHERE entry_group_id=?`, a.Version, EntryDesired, p.deps.Clock.Now().UnixNano(), g)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE entry_dns_audits SET next_check_at=? WHERE group_id=?", p.deps.Clock.Now().Add(5*time.Minute).UnixNano(), g)
	}
	if err == nil {
		err = resetEntryWatchPaths(ctx, tx, g)
	}
	return a, err
}
func (p *Panel) publishAllGroup(w http.ResponseWriter, r *http.Request, addresses map[string]string) {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	states, err := p.checkEntryCandidates(r.Context())
	if err != nil {
		problem(w, 503, "无法核对成员就绪状态")
		return
	}
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	ctx := r.Context()
	g := r.PathValue("id")
	var published bool
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT g.first_published_at IS NOT NULL,coalesce(s.desired_version,0) FROM entry_groups g LEFT JOIN entry_publications s ON s.group_id=g.id WHERE g.id=?`, g).Scan(&published, &version)
	if err == nil && published {
		err = &forwardingError{409, "该组已首次发布"}
	}
	var members []allMemberPublication
	if err == nil {
		members, err = loadAllMembers(ctx, tx, g)
	}
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法读取发布状态")
		}
		return
	}
	validIDs := map[string]bool{}
	for _, m := range members {
		validIDs[m.NodeID] = true
	}
	for id, ip := range addresses {
		if !validIDs[id] {
			problem(w, 400, "地址须属于组内成员")
			return
		}
		if _, e := publicip.Parse(ip); e != nil {
			problem(w, 400, "成员地址须为可入站公网 IPv4")
			return
		}
	}
	changes := []ManagedRecordChange{}
	healthy := 0
	for _, m := range members {
		if m.Record == nil {
			var name string
			_ = tx.QueryRowContext(ctx, "SELECT name FROM nodes WHERE id=?", m.NodeID).Scan(&name)
			problem(w, 409, "成员“"+name+"”尚未绑定受管记录")
			return
		}
		ip := addresses[m.NodeID]
		if ip == "" {
			err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT confirmed_ipv4 FROM node_regions WHERE node_id=?),'')`, m.NodeID).Scan(&ip)
			if err != nil {
				break
			}
		}
		var confirmed string
		if err = tx.QueryRowContext(ctx, `SELECT coalesce((SELECT confirmed_ipv4 FROM node_regions WHERE node_id=?),'')`, m.NodeID).Scan(&confirmed); err != nil {
			break
		}
		if addresses[m.NodeID] != "" && confirmed != "" && addresses[m.NodeID] != confirmed {
			err = &forwardingError{409, "成员地址与面板确认地址不同，请先在节点页同步地址"}
			break
		}
		if confirmed == "" && addresses[m.NodeID] != "" {
			if err = p.confirmNodeAddressTx(ctx, tx, m.NodeID, addresses[m.NodeID]); err != nil {
				break
			}
		}
		ip, _ = publicip.Parse(ip)
		state := states[[2]string{g, m.NodeID}]
		enabled := state.Ready && ip != ""
		for _, reason := range state.CandidateReasons {
			if reason.Code == "control_offline" || reason.Code == "maintenance" || reason.Code == "blocked" || reason.Code == "not_ready" {
				enabled = false
			}
		}
		reason := "暂停待加回"
		if ip == "" {
			reason = "地址待确认"
		}
		if enabled {
			healthy++
			reason = "已发布"
		}
		// 首发地址确认只作为本组发布期望；不暗中同步该节点的其他记录。
		_, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_value=?,expected_enabled=?,address_pending=?,reason=? WHERE group_id=? AND node_id=?`, ip, enabled, ip == "", reason, g, m.NodeID)
		if err != nil {
			break
		}
		changes = append(changes, ManagedRecordChange{ManagedRecordID: m.Record.ID, BindingGeneration: m.Record.BindingGeneration, Value: ip, Enabled: enabled, ExplicitEnable: enabled})
	}
	if err == nil && healthy == 0 {
		err = &forwardingError{409, "没有在线、就绪、未被墙且不在维护中的有效地址成员，不能首次发布"}
	}
	var a EntryAction
	if err == nil {
		a, err = p.submitMemberActionTx(ctx, tx, g, members[0].NodeID, "first_publish", version, changes)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法持久化首次发布动作")
		}
		return
	}
	p.coordinator.wake(actionWorkerKey(a))
	respond(w, 202, a)
}

func (p *Panel) getAllPublication(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		problem(w, 503, "无法读取发布状态")
		return
	}
	defer tx.Rollback()
	g := r.PathValue("id")
	var version int64
	var published bool
	var minimum int
	var actionID string
	var written sql.NullInt64
	err = tx.QueryRowContext(r.Context(), `SELECT coalesce(s.desired_version,0),g.first_published_at IS NOT NULL,coalesce(s.current_action_id,''),s.written_at,g.minimum_kept FROM entry_groups g LEFT JOIN entry_publications s ON s.group_id=g.id WHERE g.id=?`, g).Scan(&version, &published, &actionID, &written, &minimum)
	var members []allMemberPublication
	var a *EntryAction
	if err == nil {
		members, err = loadAllMembers(r.Context(), tx, g)
	}
	if err == nil && actionID != "" {
		var item EntryAction
		item, err = scanAction(tx.QueryRowContext(r.Context(), "SELECT "+actionColumns+" FROM entry_actions WHERE id=?", actionID))
		if err == nil {
			item.Records, err = loadActionRecords(r.Context(), tx, item.ID)
			a = &item
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "无法读取发布状态")
		return
	}
	var at *time.Time
	if written.Valid {
		v := time.Unix(0, written.Int64).UTC()
		at = &v
	}
	states := []entryMemberState{}
	for _, m := range members {
		states = append(states, p.entryStateView(g, m.NodeID, m.NodeID))
	}
	respond(w, 200, map[string]any{"group_id": g, "publication_mode": "all", "desired_version": version, "first_published": published, "members": members, "member_states": states, "minimum_kept": minimum, "effective_minimum_kept": min(minimum, len(members)), "action": a, "written_at": at})
}
