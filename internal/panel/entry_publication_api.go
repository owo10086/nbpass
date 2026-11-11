package panel

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

func (p *Panel) publishEntryGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MemberIPv4     map[string]string `json:"member_ipv4"`
		ActiveNodeID   string            `json:"active_node_id"`
		IPv4           string            `json:"ipv4"`
		ConfirmInbound bool              `json:"confirm_inbound"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !body.ConfirmInbound {
		problem(w, 400, "首次发布须确认活动入口及其公网 IPv4 可直接入站")
		return
	}
	var mode string
	if err := p.db.QueryRowContext(r.Context(), "SELECT publication_mode FROM entry_groups WHERE id=?", r.PathValue("id")).Scan(&mode); err == nil && mode == "all" {
		p.publishAllGroup(w, r, body.MemberIPv4)
		return
	}
	if !p.fillCNAMEAddress(w, r, body.ActiveNodeID, &body.IPv4) {
		return
	}
	a, err := p.SubmitEntryAction(r.Context(), EntryActionRequest{GroupID: r.PathValue("id"), NodeID: body.ActiveNodeID, IPv4: body.IPv4, Kind: "first_publish", ExpectedVersion: 0})
	if err != nil {
		var api *forwardingError
		if errors.As(err, &api) {
			problem(w, api.status, api.message)
		} else {
			problem(w, 503, "无法持久化入口动作，未启动 DNS 写入")
		}
		return
	}
	respond(w, 202, a)
}
func (p *Panel) getEntryPublication(w http.ResponseWriter, r *http.Request) {
	var mode string
	if err := p.db.QueryRowContext(r.Context(), "SELECT publication_mode FROM entry_groups WHERE id=?", r.PathValue("id")).Scan(&mode); err == nil && mode == "all" {
		p.getAllPublication(w, r)
		return
	}
	var pub struct {
		GroupID            string       `json:"group_id"`
		DesiredVersion     int64        `json:"desired_version"`
		ActiveNodeID       string       `json:"active_node_id"`
		PreferredNodeID    string       `json:"preferred_node_id"`
		AutoFailbackPaused bool         `json:"auto_failback_paused"`
		AutoFailbackStatus string       `json:"auto_failback_status"`
		ExpectedValue      string       `json:"expected_value"`
		WrittenAt          *time.Time   `json:"written_at"`
		Action             *EntryAction `json:"action"`
	}
	pub.GroupID = r.PathValue("id")
	var actionID string
	var written sql.NullInt64
	// 同一只读事务保证期望、动作阶段与写入时间来自同一快照。
	tx, err := p.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		problem(w, 503, "无法读取发布状态")
		return
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(r.Context(), `SELECT coalesce(s.desired_version,0),coalesce(s.active_node_id,''),coalesce(s.expected_value,''),coalesce(s.current_action_id,''),s.written_at FROM entry_groups g LEFT JOIN entry_publications s ON s.group_id=g.id WHERE g.id=?`, pub.GroupID).Scan(&pub.DesiredVersion, &pub.ActiveNodeID, &pub.ExpectedValue, &actionID, &written)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "入口组不存在")
		return
	}
	if err != nil {
		problem(w, 503, "无法读取发布状态")
		return
	}
	if err = tx.QueryRowContext(r.Context(), `SELECT m.node_id,g.auto_failback_paused FROM entry_group_members m JOIN entry_groups g ON g.id=m.group_id WHERE m.group_id=? ORDER BY m.position LIMIT 1`, pub.GroupID).Scan(&pub.PreferredNodeID, &pub.AutoFailbackPaused); err != nil {
		problem(w, 503, "无法读取首选入口")
		return
	}
	pub.AutoFailbackStatus = "自动回切已启用"
	if pub.AutoFailbackPaused {
		pub.AutoFailbackStatus = "自动回切已暂停"
	}
	if written.Valid {
		t := time.Unix(0, written.Int64).UTC()
		pub.WrittenAt = &t
	}
	if actionID != "" {
		a, err := scanAction(tx.QueryRowContext(r.Context(), "SELECT "+actionColumns+" FROM entry_actions WHERE id=?", actionID))
		if err != nil {
			problem(w, 503, "无法读取入口动作")
			return
		}
		pub.Action = &a
		pub.Action.Records, err = loadActionRecords(r.Context(), tx, a.ID)
		if err != nil {
			problem(w, 503, "无法读取逐条动作状态")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		problem(w, 503, "无法读取发布状态")
		return
	}
	respond(w, 200, pub)
}
func (p *Panel) listEntryActions(w http.ResponseWriter, r *http.Request) {
	rows, err := p.db.QueryContext(r.Context(), "SELECT "+actionColumns+" FROM entry_actions WHERE group_id=? ORDER BY version DESC LIMIT 100", r.PathValue("id"))
	if err != nil {
		problem(w, 503, "无法读取入口动作")
		return
	}
	defer rows.Close()
	actions := []EntryAction{}
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			problem(w, 503, "无法读取入口动作")
			return
		}
		actions = append(actions, a)
	}
	if rows.Err() != nil {
		rows.Close()
		problem(w, 503, "无法读取入口动作")
		return
	}
	rows.Close()
	for i := range actions {
		actions[i].Records, err = loadActionRecords(r.Context(), p.db, actions[i].ID)
		if err != nil {
			problem(w, 503, "无法读取逐条动作状态")
			return
		}
	}
	respond(w, 200, map[string]any{"actions": actions})
}
