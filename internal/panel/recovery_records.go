package panel

import (
	"context"
	"database/sql"
	"errors"

	"nbpass/internal/dnsprovider"
)

func recoveryRecordNodesReady(ctx context.Context, q recoveryQuerier, b managedRecord) error {
	var pending int
	query := `SELECT count(*) FROM entry_group_members m JOIN recovery_nodes r ON r.node_id=m.node_id WHERE m.group_id=? AND (r.high_water IS NULL OR r.verified=0)`
	owner := b.GroupID
	if b.OwnerKind == "node" {
		query = `SELECT count(*) FROM recovery_nodes WHERE node_id=? AND (high_water IS NULL OR verified=0)`
		owner = b.NodeID
	}
	if err := q.QueryRowContext(ctx, query, owner).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("入口存在无法核对的节点，自动写继续暂停")
	}
	return nil
}

func (p *Panel) reconcileRecoveryGroup(ctx context.Context, gid string) error {
	if err := recoveryRecordNodesReady(ctx, p.db, managedRecord{GroupID: gid}); err != nil {
		return err
	}
	rows, err := p.db.QueryContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE group_id=? ORDER BY id", gid)
	if err != nil {
		return err
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
		return err
	}
	var failure error
	for _, b := range records {
		if err := p.reconcileRecoveryRecord(ctx, b); err != nil {
			failure = err
		}
	}
	if failure != nil {
		return failure
	}
	// 多记录组核对完毕后只抬高一次期望，历史动作永不重放。
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recoveryRecordNodesReady(ctx, tx, managedRecord{GroupID: gid}); err != nil {
		return err
	}
	var pending int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM managed_records m LEFT JOIN recovery_records r ON r.managed_record_id=m.id AND r.binding_generation=m.binding_generation WHERE m.group_id=? AND coalesce(r.verified,0)=0`, gid).Scan(&pending)
	if err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("入口组受管记录尚未全部核对")
	}
	_, err = tx.ExecContext(ctx, `UPDATE entry_publications SET desired_version=desired_version+1,current_action_id='',written_at=? WHERE group_id=? AND current_action_id<>''`, p.deps.Clock.Now().UnixNano(), gid)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE entry_groups SET publication_status=? WHERE id=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=?)`, EntryWritten, gid, gid)
	}
	if err == nil {
		var mode string
		if err = tx.QueryRowContext(ctx, "SELECT publication_mode FROM entry_groups WHERE id=?", gid).Scan(&mode); err == nil && mode == "all" {
			var version int64
			err = tx.QueryRowContext(ctx, "SELECT coalesce((SELECT desired_version FROM entry_publications WHERE group_id=?),0)", gid).Scan(&version)
			if err == nil {
				_, err = tx.ExecContext(ctx, "UPDATE dns_watches SET expected_version=? WHERE entry_group_id=?", version, gid)
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, "DELETE FROM entry_dns_previous WHERE watch_id IN (SELECT id FROM dns_watches WHERE entry_group_id=?)", gid)
			}
			if err == nil {
				err = p.refreshAllWatchTx(ctx, tx, EntryAction{GroupID: gid, Version: version}, p.deps.Clock.Now())
			}
			if err == nil {
				err = resetEntryWatchPaths(ctx, tx, gid)
			}
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO recovery_groups(group_id,verified) VALUES(?,1) ON CONFLICT(group_id) DO UPDATE SET verified=1,last_error=''`, gid)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Panel) reconcileRecoveryRecord(ctx context.Context, b managedRecord) error {
	var verified bool
	err := p.db.QueryRowContext(ctx, "SELECT verified FROM recovery_records WHERE managed_record_id=? AND binding_generation=?", b.ID, b.BindingGeneration).Scan(&verified)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if verified {
		return nil
	}
	if err := recoveryRecordNodesReady(ctx, p.db, b); err != nil {
		return p.noteRecoveryRecord(ctx, b, err)
	}
	cred, err := p.dnsCredential(ctx, b.AccountID)
	if err != nil || p.deps.DNSProvider == nil {
		return p.noteRecoveryRecord(ctx, b, errors.New("DNSPod 凭据或服务不可用，自动写继续暂停"))
	}
	readCtx, cancel := p.dnsContext(ctx)
	actual, err := p.deps.DNSProvider.ReadRecord(readCtx, cred.provider(), dnsprovider.Key{Domain: b.Domain, RecordID: b.RecordID})
	cancel()
	if err != nil {
		return p.noteRecoveryRecord(ctx, b, errors.New("DNSPod 读回失败，自动写继续暂停"))
	}
	if _, err := recordWarnings(actual, b.RecordID, b.LineID); err != nil || actual.Record.Name != b.Name || b.OwnerKind == "group" && !actual.Record.Enabled && b.CurrentEnabled {
		return p.noteRecoveryRecord(ctx, b, errors.New("受管记录身份、线路、类型或启用状态无法核对"))
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := recoveryRecordNodesReady(ctx, tx, b); err != nil {
		return err
	}
	var count int
	var nodeID string
	if b.OwnerKind == "group" {
		err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(min(m.node_id),'') FROM entry_group_members m JOIN node_regions n ON n.node_id=m.node_id WHERE m.group_id=? AND n.public_ipv4=?`, b.GroupID, actual.Record.Value).Scan(&count, &nodeID)
		if err != nil {
			return err
		}
		if count != 1 {
			tx.Rollback()
			return p.noteRecoveryRecord(ctx, b, errors.New("实际 DNS 无法唯一对应已核对的入口节点，自动写继续暂停"))
		}
	} else {
		var confirmed string
		err = tx.QueryRowContext(ctx, "SELECT confirmed_ipv4 FROM node_regions WHERE node_id=?", b.NodeID).Scan(&confirmed)
		var pendingAddress bool
		if b.OwnerKind == "member" {
			if e := tx.QueryRowContext(ctx, "SELECT address_pending FROM entry_member_publications WHERE group_id=? AND node_id=?", b.GroupID, b.NodeID).Scan(&pendingAddress); e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if pendingAddress && !actual.Record.Enabled && actual.Record.Value == b.CurrentValue {
			err = nil
		} else if err != nil || confirmed == "" || confirmed != actual.Record.Value {
			tx.Rollback()
			return p.noteRecoveryRecord(ctx, b, errors.New("记录地址与面板确认的地址不一致，请先手动同步地址"))
		}
	}
	// 身份与代次条件覆盖外部读取期间的换绑；状态和地址一起采用服务商现状。
	err = missingResult(tx.ExecContext(ctx, `UPDATE managed_records SET current_value=?,current_ttl=?,current_enabled=? WHERE id=? AND binding_generation=? AND provider=? AND domain=? AND record_id=?`, actual.Record.Value, actual.Record.TTL, actual.Record.Enabled, b.ID, b.BindingGeneration, b.Provider, b.Domain, b.RecordID))
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO recovery_records(managed_record_id,binding_generation,verified) VALUES(?,?,1) ON CONFLICT(managed_record_id) DO UPDATE SET binding_generation=excluded.binding_generation,verified=1,last_error=''`, b.ID, b.BindingGeneration)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE entry_action_records SET stage=?,last_error='恢复核对已封存旧动作，未重放 DNS 写入' WHERE managed_record_id=? AND stage IN `+pendingRecordStages, EntryArchived, b.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE entry_actions SET stage=?,last_error='恢复核对已封存旧动作，未重放 DNS 写入' WHERE stage NOT IN `+terminalActionStages+` AND id IN (SELECT action_id FROM entry_action_records WHERE managed_record_id=?) AND NOT EXISTS(SELECT 1 FROM entry_action_records r WHERE r.action_id=entry_actions.id AND r.stage IN `+pendingRecordStages+`)`, EntryArchived, b.ID)
	}
	if err == nil && b.OwnerKind == "member" {
		_, err = tx.ExecContext(ctx, `UPDATE entry_member_publications SET expected_value=CASE WHEN address_pending=1 THEN '' ELSE ? END,expected_enabled=?,last_action_error='',removal_action_id='',propagation_status='',reason=CASE WHEN address_pending=1 THEN '地址待确认' WHEN ?=1 THEN '已发布' ELSE '暂停待加回' END WHERE group_id=? AND node_id=?`, actual.Record.Value, actual.Record.Enabled, actual.Record.Enabled, b.GroupID, b.NodeID)
	}
	if err == nil && b.OwnerKind == "group" {
		_, err = tx.ExecContext(ctx, `UPDATE entry_publications SET desired_version=desired_version+1,active_node_id=?,expected_value=?,current_action_id='',written_at=? WHERE group_id=? AND (active_node_id<>? OR expected_value<>? OR current_action_id<>'')`, nodeID, actual.Record.Value, p.deps.Clock.Now().UnixNano(), b.GroupID, nodeID, actual.Record.Value)
	}
	if err == nil && b.OwnerKind == "node" {
		_, err = tx.ExecContext(ctx, `UPDATE node_publications SET desired_version=desired_version+1,current_action_id='',written_at=?,
 published=CASE WHEN ? AND EXISTS(SELECT 1 FROM entry_actions a JOIN entry_action_records r ON r.action_id=a.id WHERE a.id=node_publications.current_action_id AND a.kind='first_publish' AND a.stage IN ('恢复已封存','服务商已写入') AND r.managed_record_id=? AND r.binding_generation=? AND r.explicit_enable=1 AND r.status_started=1 AND r.new_value=? AND r.new_enabled=1) THEN 1 ELSE published END
 WHERE node_id=? AND current_action_id<>''`, p.deps.Clock.Now().UnixNano(), actual.Record.Enabled, b.ID, b.BindingGeneration, actual.Record.Value, b.NodeID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Panel) noteRecoveryRecord(ctx context.Context, b managedRecord, cause error) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO recovery_records(managed_record_id,binding_generation,verified,last_error) SELECT id,binding_generation,0,? FROM managed_records WHERE id=? AND binding_generation=? ON CONFLICT(managed_record_id) DO UPDATE SET verified=0,last_error=excluded.last_error`, cause.Error(), b.ID, b.BindingGeneration)
	if err != nil {
		return err
	}
	return cause
}
