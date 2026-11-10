package panel

import (
	"context"
	"database/sql"
	"net/http"

	"nbpass/internal/publicip"
)

func (p *Panel) syncNodeAddress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IPv4           string `json:"ipv4"`
		ConfirmInbound bool   `json:"confirm_inbound"`
	}
	if !decode(w, r, &body) {
		return
	}
	address, err := publicip.Parse(body.IPv4)
	if err != nil || !body.ConfirmInbound {
		problem(w, 400, "请确认单个可入站公网 IPv4")
		return
	}
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	defer tx.Rollback()
	var reported string
	err = tx.QueryRowContext(r.Context(), "SELECT coalesce(r.public_ipv4,'') FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id WHERE n.id=?", r.PathValue("id")).Scan(&reported)
	if err == nil && reported != address {
		err = &forwardingError{409, "节点上报地址已变化，请刷新后重新确认"}
	}
	var actions []EntryAction
	if err == nil {
		actions, err = p.syncNodeRecordsTx(r.Context(), tx, r.PathValue("id"), address, "sync_address", true)
	}
	if err == nil {
		err = p.confirmNodeAddressTx(r.Context(), tx, r.PathValue("id"), address)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	for _, a := range actions {
		p.coordinator.wake(actionWorkerKey(a))
	}
	p.wakeDNSWatch()
	respond(w, 200, map[string]any{"confirmed_ipv4": address, "actions": actions})
}

// 手动同步与受控换 IP 共用逐记录提交：不要求存在节点级记录，覆盖所有已发布成员。
// 调用者持 controlMu 并在同一事务确认地址；不得在这里反向获取 entryMu。
// address_update 是已授权换 IP 的收尾，票 07 应允许此收尾完成。
func (p *Panel) syncNodeRecordsTx(ctx context.Context, tx *sql.Tx, nodeID, address, kind string, protectPending bool) ([]EntryAction, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+managedColumns+` FROM managed_records WHERE node_id=? AND (owner_kind='node' OR (owner_kind='member' AND EXISTS(SELECT 1 FROM entry_publications p WHERE p.group_id=managed_records.group_id AND p.desired_version>0))) ORDER BY coalesce(group_id,''),id`, nodeID)
	if err != nil {
		return nil, err
	}
	var records []managedRecord
	for rows.Next() {
		b, e := scanManaged(rows)
		if e != nil {
			err = e
			break
		}
		records = append(records, b)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	actions := []EntryAction{}
	for _, b := range records {
		pending, err := recordOwnerPending(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		var queued bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM node_address_sync_jobs WHERE managed_record_id=?)", b.ID).Scan(&queued); err != nil {
			return nil, err
		}
		if pending || queued {
			if protectPending {
				return nil, &forwardingError{409, "节点或所属入口组有未完成动作，请稍后同步地址"}
			}
			// 保留组内其他成员的在途期望；新地址确认与持久化收尾待办同事务提交。
			_, err = tx.ExecContext(ctx, `INSERT INTO node_address_sync_jobs(managed_record_id,binding_generation,node_id,address,kind) VALUES(?,?,?,?,?) ON CONFLICT(managed_record_id) DO UPDATE SET binding_generation=excluded.binding_generation,node_id=excluded.node_id,address=excluded.address,kind=excluded.kind`, b.ID, b.BindingGeneration, nodeID, address, kind)
			if err != nil {
				return nil, err
			}
			continue
		}
		a, err := p.submitNodeRecordAddressTx(ctx, tx, b, address, kind)
		if err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, nil
}

// 期望版本属于整个组，不能仅检查当前节点的那一条记录。
func recordOwnerPending(ctx context.Context, tx *sql.Tx, b managedRecord) (bool, error) {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entry_actions WHERE ((?<>'' AND group_id=?) OR (?='node' AND owner_node_id=?)) AND stage IN `+pendingRecordStages+`)`, b.GroupID, b.GroupID, b.OwnerKind, b.NodeID).Scan(&pending)
	return pending, err
}
func (p *Panel) submitNodeRecordAddressTx(ctx context.Context, tx *sql.Tx, b managedRecord, address, kind string) (EntryAction, error) {
	req := RecordActionRequest{NodeID: b.NodeID, Kind: kind, Records: []ManagedRecordChange{{ManagedRecordID: b.ID, BindingGeneration: b.BindingGeneration, Value: address, Enabled: b.CurrentEnabled, PreserveEnabled: true}}}
	if b.OwnerKind == "node" {
		req.OwnerNodeID = b.NodeID
		if err := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT desired_version FROM node_publications WHERE node_id=?),0)", b.NodeID).Scan(&req.ExpectedVersion); err != nil {
			return EntryAction{}, err
		}
		return p.submitRecordActionTx(ctx, tx, req)
	}
	if err := tx.QueryRowContext(ctx, "SELECT desired_version FROM entry_publications WHERE group_id=?", b.GroupID).Scan(&req.ExpectedVersion); err != nil {
		return EntryAction{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_value=?,address_pending=0 WHERE group_id=? AND node_id=?`, address, b.GroupID, b.NodeID); err != nil {
		return EntryAction{}, err
	}
	// 成员记录走全部发布的成员动作接缝，同事务刷新集合期望与偏差审计。
	return p.submitMemberActionTx(ctx, tx, b.GroupID, b.NodeID, kind, req.ExpectedVersion, req.Records)
}
