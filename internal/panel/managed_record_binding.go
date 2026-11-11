package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const pendingRecordStages = `('期望已变更','结果未确认')`
const terminalActionStages = `('服务商已写入','明确失败','部分完成','已失效','恢复已封存')`

// bindRecordTx 是三种归属共用的绑定接缝，不调用服务商，不改旧记录。
// 调用方先读取并校验服务商身份；绑定变更与归属、代次检查必须在同一事务提交。
func (p *Panel) bindRecordTx(ctx context.Context, tx *sql.Tx, b managedRecord) (managedRecord, error) {
	var err error
	b.Domain, err = normalizeDNSDomain(b.Domain)
	if err != nil {
		return b, err
	}
	if b.Type == "" {
		b.Type = "A"
	}
	if b.Provider != "dnspod" || b.RecordID == "" || b.LineID == "" || (b.Type != "A" && (b.Type != "CNAME" || b.OwnerKind != "group")) {
		return b, &forwardingError{400, "受管记录身份无效"}
	}
	var valid bool
	switch b.OwnerKind {
	case "group":
		valid = b.GroupID != "" && b.NodeID == ""
	case "member":
		if b.GroupID != "" && b.NodeID != "" {
			err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM entry_group_members WHERE group_id=? AND node_id=?)", b.GroupID, b.NodeID).Scan(&valid)
		}
	case "node":
		valid = b.GroupID == "" && b.NodeID != ""
	}
	if err != nil {
		return b, err
	}
	if !valid {
		return b, &forwardingError{400, "受管记录必须唯一归属主备入口组、组成员或入口节点"}
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM managed_records WHERE owner_kind=? AND coalesce(group_id,'')=? AND coalesce(node_id,'')=?`, b.OwnerKind, b.GroupID, b.NodeID).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	var remoteOwner string
	err = tx.QueryRowContext(ctx, "SELECT id FROM managed_records WHERE provider=? AND domain=? AND record_id=?", b.Provider, b.Domain, b.RecordID).Scan(&remoteOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	if remoteOwner != "" && remoteOwner != existing {
		return b, &forwardingError{409, "该远端记录已有归属，账号别名不能重复绑定"}
	}
	if existing != "" {
		if err := protectRecordBinding(ctx, tx, existing); err != nil {
			return b, err
		}
		b.ID = existing
		var oldDomain, oldRecord string
		if err := tx.QueryRowContext(ctx, "SELECT domain,record_id FROM managed_records WHERE id=?", existing).Scan(&oldDomain, &oldRecord); err != nil {
			return b, err
		}
		if oldDomain != b.Domain || oldRecord != b.RecordID {
			b.Warnings = append(b.Warnings, "旧 DNSPod 记录（"+oldDomain+" / "+oldRecord+"）保持原状，请管理员自行核对和处理")
		}
	} else {
		b.ID, _, err = p.randomCredential()
		if err != nil {
			return b, err
		}
	}
	data, _ := json.Marshal(b.Warnings)
	_, err = tx.ExecContext(ctx, `INSERT INTO managed_records(id,owner_kind,group_id,node_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,current_enabled,warnings,record_type)
 VALUES(?,?,nullif(?,''),nullif(?,''),?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET
 account_id=excluded.account_id,record_type=excluded.record_type,provider=excluded.provider,domain=excluded.domain,record_id=excluded.record_id,name=excluded.name,line=excluded.line,line_id=excluded.line_id,
 current_value=excluded.current_value,current_ttl=excluded.current_ttl,current_enabled=excluded.current_enabled,warnings=excluded.warnings,binding_generation=managed_records.binding_generation+1`,
		b.ID, b.OwnerKind, b.GroupID, b.NodeID, b.AccountID, b.Provider, b.Domain, b.RecordID, b.Name, b.Line, b.LineID, b.CurrentValue, b.CurrentTTL, b.CurrentEnabled, string(data), b.Type)
	if err != nil {
		return b, err
	}
	b, err = scanManaged(tx.QueryRowContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE id=?", b.ID))
	if err != nil {
		return b, err
	}
	var tracked bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM recovery_records WHERE managed_record_id=?)", b.ID).Scan(&tracked); err != nil {
		return b, err
	}
	// 恢复后的全新手动绑定已有本次服务商读回证据，不继承旧备份的暂停。
	// 恢复清单内的旧绑定改代次则必须再次核对，防止沿用旧放行结果。
	_, err = tx.ExecContext(ctx, `INSERT INTO recovery_records(managed_record_id,binding_generation,verified) SELECT id,binding_generation,1 FROM managed_records WHERE id=? AND EXISTS(SELECT 1 FROM recovery_state) ON CONFLICT(managed_record_id) DO UPDATE SET binding_generation=excluded.binding_generation,verified=0,last_error=''`, b.ID)
	if err == nil && tracked && b.GroupID != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO recovery_groups(group_id) SELECT ? WHERE EXISTS(SELECT 1 FROM recovery_state) ON CONFLICT(group_id) DO UPDATE SET verified=0,last_error=''`, b.GroupID)
	}
	return b, err
}

func protectRecordBinding(ctx context.Context, tx *sql.Tx, id string) error {
	var pending bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entry_action_records WHERE managed_record_id=? AND stage IN `+pendingRecordStages+`) OR EXISTS(SELECT 1 FROM node_address_sync_jobs WHERE managed_record_id=?)`, id, id).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return &forwardingError{409, "该受管记录有未完成动作，不能解绑或更换"}
	}
	return nil
}

// unbindRecordTx 仅解绑本地归属。返回的提示须由后续节点/成员页面展示。
func unbindRecordTx(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	if err := protectRecordBinding(ctx, tx, id); err != nil {
		return "", err
	}
	err := missingResult(tx.ExecContext(ctx, "DELETE FROM managed_records WHERE id=?", id))
	return "旧 DNSPod 记录保持原状，请管理员自行核对和处理", err
}
