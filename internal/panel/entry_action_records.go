package panel

import (
	"context"
	"database/sql"
	"errors"

	"nbpass/internal/publicip"
)

// ManagedRecordChange 必须携带调用方观察到的绑定代次。空值表示保留服务商当前地址。
// 从暂停到启用时，协调器先改值并保持暂停，再明确启用，供票 03/04 复用。
type ManagedRecordChange struct {
	ManagedRecordID   string
	BindingGeneration int64
	Value             string
	Enabled           bool
	ExplicitEnable    bool
}

// RecordActionRequest 是多记录和节点级动作的事务接缝；调用方负责策略和用户授权。
// 组与节点归属二选一，期望版本在同一事务校验；提交者提交事务后再唤醒协调器。
type RecordActionRequest struct {
	GroupID, OwnerNodeID, NodeID, Kind string
	ExpectedVersion                    int64
	Records                            []ManagedRecordChange
	MinimumEnabled                     int
}

type EntryActionRecord struct {
	ManagedRecordID               string `json:"managed_record_id"`
	BindingGeneration             int64  `json:"binding_generation"`
	Provider                      string `json:"provider"`
	Domain                        string `json:"domain"`
	RecordID                      string `json:"record_id"`
	OldValue                      string `json:"old_value"`
	NewValue                      string `json:"new_value"`
	OldTTL                        int    `json:"old_ttl"`
	NewTTL                        int    `json:"new_ttl"`
	OldEnabled                    bool   `json:"old_enabled"`
	NewEnabled                    bool   `json:"new_enabled"`
	Stage                         string `json:"stage"`
	LastError                     string `json:"last_error"`
	writeStarted, valueConfirmed  bool
	explicitEnable, statusStarted bool
}

func loadActionRecords(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) ([]EntryActionRecord, error) {
	rows, err := q.QueryContext(ctx, `SELECT managed_record_id,binding_generation,provider,domain,record_id,old_value,new_value,old_ttl,new_ttl,old_enabled,new_enabled,stage,last_error,write_started,value_confirmed,explicit_enable,status_started FROM entry_action_records WHERE action_id=? ORDER BY new_enabled DESC,managed_record_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EntryActionRecord{}
	for rows.Next() {
		var r EntryActionRecord
		if err := rows.Scan(&r.ManagedRecordID, &r.BindingGeneration, &r.Provider, &r.Domain, &r.RecordID, &r.OldValue, &r.NewValue, &r.OldTTL, &r.NewTTL, &r.OldEnabled, &r.NewEnabled, &r.Stage, &r.LastError, &r.writeStarted, &r.valueConfirmed, &r.explicitEnable, &r.statusStarted); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func insertActionRecord(ctx context.Context, tx *sql.Tx, id string, b managedRecord, change ManagedRecordChange) error {
	if b.BindingGeneration != change.BindingGeneration {
		return &forwardingError{409, "受管记录绑定已变化，请刷新后重试"}
	}
	if change.Value != "" {
		if _, err := publicip.Parse(change.Value); err != nil {
			return &forwardingError{400, "受管记录地址须为可入站公网 IPv4"}
		}
	}
	ttl := 60
	if change.Value == "" {
		ttl = b.CurrentTTL
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO entry_action_records(action_id,managed_record_id,binding_generation,provider,domain,record_id,old_value,new_value,old_ttl,new_ttl,old_enabled,new_enabled,stage,explicit_enable) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, b.ID, b.BindingGeneration, b.Provider, b.Domain, b.RecordID, b.CurrentValue, change.Value, b.CurrentTTL, ttl, b.CurrentEnabled, change.Enabled, EntryDesired, change.ExplicitEnable)
	return err
}

func (p *Panel) submitRecordActionTx(ctx context.Context, tx *sql.Tx, req RecordActionRequest) (EntryAction, error) {
	a := EntryAction{GroupID: req.GroupID, OwnerNodeID: req.OwnerNodeID, NodeID: req.NodeID, Kind: req.Kind, Stage: EntryDesired, MinimumEnabled: req.MinimumEnabled}
	if a.GroupID != "" {
		a.MinimumEnabled = max(1, a.MinimumEnabled)
	}
	if (req.GroupID == "") == (req.OwnerNodeID == "") || len(req.Records) == 0 || req.NodeID == "" || req.Kind == "" || req.MinimumEnabled < 0 {
		return a, &forwardingError{400, "请指定唯一动作归属及受管记录"}
	}
	if req.GroupID != "" {
		allowed, err := recoveryGroupAllowed(ctx, tx, req.GroupID)
		if err != nil {
			return a, err
		}
		if !allowed {
			return a, &forwardingError{409, "入口组恢复尚未核对"}
		}
	} else {
		allowed, err := p.recoveryNodeAllowed(ctx, tx, req.OwnerNodeID, false)
		if err != nil {
			return a, err
		}
		if !allowed {
			return a, &forwardingError{409, "入口节点恢复尚未核对"}
		}
	}
	query := `SELECT desired_version FROM entry_publications WHERE group_id=?`
	owner := req.GroupID
	if owner == "" {
		query = `SELECT desired_version FROM node_publications WHERE node_id=?`
		owner = req.OwnerNodeID
	}
	var version int64
	err := tx.QueryRowContext(ctx, query, owner).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return a, err
	}
	if version != req.ExpectedVersion {
		return a, &forwardingError{409, "期望版本已变化，请刷新后重试"}
	}
	a.ID, _, err = p.randomCredential()
	if err != nil {
		return a, err
	}
	a.Version = version + 1
	a.CreatedAt = p.deps.Clock.Now()
	_, err = tx.ExecContext(ctx, `INSERT INTO entry_actions(id,group_id,owner_node_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,minimum_enabled) VALUES(?,nullif(?,''),nullif(?,''),?,?,?,'','',60,?,?,?)`, a.ID, a.GroupID, a.OwnerNodeID, a.Version, a.Kind, a.NodeID, a.Stage, a.CreatedAt.UnixNano(), a.MinimumEnabled)
	if err != nil {
		return a, err
	}
	for _, change := range req.Records {
		b, err := scanManaged(tx.QueryRowContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE id=?", change.ManagedRecordID))
		if err != nil {
			return a, err
		}
		if req.GroupID != "" && b.GroupID != req.GroupID || req.OwnerNodeID != "" && (b.OwnerKind != "node" || b.NodeID != req.OwnerNodeID) {
			return a, &forwardingError{409, "受管记录不属于本动作"}
		}
		if req.OwnerNodeID != "" && req.Kind == "first_publish" && change.Enabled {
			change.ExplicitEnable = true
		}
		if err := insertActionRecord(ctx, tx, a.ID, b, change); err != nil {
			return a, err
		}
	}
	if req.GroupID != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO entry_publications(group_id,desired_version,current_action_id) VALUES(?,?,?) ON CONFLICT(group_id) DO UPDATE SET desired_version=excluded.desired_version,current_action_id=excluded.current_action_id,written_at=NULL`, a.GroupID, a.Version, a.ID)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE entry_groups SET publication_status=? WHERE id=?", EntryDesired, a.GroupID)
		}
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO node_publications(node_id,desired_version,current_action_id) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET desired_version=excluded.desired_version,current_action_id=excluded.current_action_id,written_at=NULL`, a.OwnerNodeID, a.Version, a.ID)
	}
	if err == nil {
		err = p.actionEvent(ctx, tx, a, a.Stage)
	}
	return a, err
}

func actionWorkerKey(a EntryAction) string {
	if a.OwnerNodeID != "" {
		return "node:" + a.OwnerNodeID
	}
	return a.GroupID
}
