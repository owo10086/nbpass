package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"nbpass/internal/eventlog"
	"nbpass/internal/publicip"
)

const (
	EntryDesired     = "期望已变更"
	EntryWritten     = "服务商已写入"
	EntryUnconfirmed = "结果未确认"
)

// EntryActionRequest 是后续手动切换、回切和偏差写回的唯一提交接缝。
// 调用方先完成策略判定，必须携带观察到的期望版本；协调器不接受过期请求。
type EntryActionRequest struct {
	GroupID         string
	NodeID          string
	IPv4            string
	Kind            string
	ExpectedVersion int64
}

type EntryAction struct {
	ID           string     `json:"id"`
	GroupID      string     `json:"group_id"`
	Version      int64      `json:"version"`
	Kind         string     `json:"kind"`
	NodeID       string     `json:"node_id"`
	OldValue     string     `json:"old_value"`
	NewValue     string     `json:"new_value"`
	OldTTL       int        `json:"old_ttl"`
	Stage        string     `json:"stage"`
	LastError    string     `json:"last_error"`
	CreatedAt    time.Time  `json:"created_at"`
	WrittenAt    *time.Time `json:"written_at"`
	writeStarted bool
}

const actionColumns = `id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,last_error,created_at,written_at,write_started`

func scanAction(row scanner) (EntryAction, error) {
	var a EntryAction
	var created int64
	var written sql.NullInt64
	err := row.Scan(&a.ID, &a.GroupID, &a.Version, &a.Kind, &a.NodeID, &a.OldValue, &a.NewValue, &a.OldTTL, &a.Stage, &a.LastError, &created, &written, &a.writeStarted)
	a.CreatedAt = time.Unix(0, created).UTC()
	if written.Valid {
		t := time.Unix(0, written.Int64).UTC()
		a.WrittenAt = &t
	}
	return a, err
}
func (p *Panel) actionEvent(ctx context.Context, tx *sql.Tx, a EntryAction, stage string) error {
	now := p.deps.Clock.Now()
	return insertEvent(ctx, tx, a.NodeID, "panel", eventlog.Event{ID: a.ID + ":" + stage, Type: "entry_action", OccurredAt: now, Message: fmt.Sprintf("入口组 %s 动作 %s 期望版本 %d：%s", a.GroupID, a.ID, a.Version, stage)}, now)
}

func (p *Panel) SubmitEntryAction(ctx context.Context, req EntryActionRequest) (EntryAction, error) {
	ip, err := publicip.Parse(req.IPv4)
	if err != nil || req.NodeID == "" {
		return EntryAction{}, &forwardingError{400, "请选择活动入口并确认单个可入站公网 IPv4"}
	}
	switch req.Kind {
	case "first_publish", "manual_switch", "switch", "failback", "address_update", "writeback":
	default:
		return EntryAction{}, &forwardingError{400, "入口动作类型无效"}
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	if p.coordinator.ctx.Err() != nil {
		return EntryAction{}, errors.New("面板正在关闭")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return EntryAction{}, err
	}
	defer tx.Rollback()
	var a EntryAction
	a.GroupID, a.NodeID, a.Kind, a.NewValue, a.Stage = req.GroupID, req.NodeID, req.Kind, ip, EntryDesired
	err = tx.QueryRowContext(ctx, "SELECT current_value,current_ttl FROM managed_records WHERE group_id=?", req.GroupID).Scan(&a.OldValue, &a.OldTTL)
	if errors.Is(err, sql.ErrNoRows) {
		return a, &forwardingError{409, "请先绑定受管记录"}
	}
	if err != nil {
		return a, err
	}
	var member int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM entry_group_members WHERE group_id=? AND node_id=?", req.GroupID, req.NodeID).Scan(&member); err != nil {
		return a, err
	}
	if member != 1 {
		return a, &forwardingError{400, "活动入口必须是入口组成员"}
	}
	var version int64
	err = tx.QueryRowContext(ctx, "SELECT desired_version FROM entry_publications WHERE group_id=?", req.GroupID).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	if version == 0 && req.Kind != "first_publish" {
		return a, &forwardingError{409, "请先由管理员确认活动入口并首次发布"}
	}
	if version != req.ExpectedVersion || (req.Kind == "first_publish" && version != 0) {
		return a, &forwardingError{409, "期望版本已变化，请刷新后重新操作"}
	}
	if err := entryActionPolicy(ctx, tx, req); err != nil {
		return a, err
	}
	a.Version = version + 1
	a.ID, _, err = p.randomCredential()
	if err != nil {
		return a, err
	}
	a.CreatedAt = p.deps.Clock.Now()
	_, err = tx.ExecContext(ctx, `INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, a.ID, a.GroupID, a.Version, a.Kind, a.NodeID, a.OldValue, a.NewValue, a.OldTTL, a.Stage, a.CreatedAt.UnixNano())
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id) VALUES(?,?,?,?,?) ON CONFLICT(group_id) DO UPDATE SET desired_version=excluded.desired_version,active_node_id=excluded.active_node_id,expected_value=excluded.expected_value,current_action_id=excluded.current_action_id,written_at=NULL`, a.GroupID, a.Version, a.NodeID, a.NewValue, a.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE entry_groups SET publication_status=? WHERE id=?", EntryDesired, a.GroupID)
	}
	if err == nil {
		err = p.actionEvent(ctx, tx, a, EntryDesired)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return EntryAction{}, err
	}
	p.coordinator.wake(a.GroupID)
	return a, nil
}
