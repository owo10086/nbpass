package panel

import (
	"errors"

	"nbpass/internal/dnsprovider"
)

var errObservationWrite = errors.New("观测模式：自动 DNS 动作仅核对已调用结果，不再写入")

// 与开关使用同一把 entryMu，使最终检查、持久化调用标记与调用不可被开关穿插。
// 只在自动 DNS 外部写入期间持锁；读回与保存结果不持锁，不获取 controlMu。
func (c *entryCoordinator) dispatchRecordWrite(a EntryAction, r EntryActionRecord, write func() error) (bool, error) {
	if a.GroupID != "" && automaticEntryKind(a.Kind) {
		c.p.entryMu.Lock()
		defer c.p.entryMu.Unlock()
		enabled, err := groupObserving(c.ctx, c.p.db, a.GroupID)
		if err != nil {
			return false, err
		}
		if enabled {
			// 开关事务已作废未调用记录；旧 worker 不能按旧内存快照发送。
			return false, errObservationWrite
		}
	}
	if err := c.markRecordWriting(a, r); err != nil {
		return false, err
	}
	return true, write()
}

// 改值已调用、启停尚未调用时，先核对改值，再作废尚未执行的启停步骤。
// 不能把整条记录取消当作没调用过，也不能让未下发的步骤永久阻塞手动操作。
func (c *entryCoordinator) cancelObservedRecordRemainder(a EntryAction, r EntryActionRecord, actual dnsprovider.Record) (bool, error) {
	if a.GroupID == "" || !automaticEntryKind(a.Kind) || !r.writeStarted || r.statusStarted || actual.Enabled == r.NewEnabled && !r.explicitEnable || actual.Value != r.NewValue || actual.TTL != r.NewTTL {
		return false, nil
	}
	p := c.p
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	tx, err := p.db.BeginTx(c.ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	enabled, err := groupObserving(c.ctx, tx, a.GroupID)
	if err != nil || !enabled {
		return false, err
	}
	if _, err = p.observeEntryTx(c.ctx, tx, a.GroupID, a.NodeID, a.Kind, "remainder:"+a.ID+":"+r.ManagedRecordID, "核对已调用的改值；尚未调用的启停已作废，保留实际启用状态"); err != nil {
		return true, err
	}
	if err = missingResult(tx.ExecContext(c.ctx, `UPDATE entry_action_records SET stage=?,last_error='观测模式：改值已核对，未调用的启停步骤作废',value_confirmed=1 WHERE action_id=? AND managed_record_id=? AND binding_generation=? AND write_started=1 AND status_started=0 AND stage IN `+pendingRecordStages, EntrySuperseded, a.ID, r.ManagedRecordID, r.BindingGeneration)); err != nil {
		return true, err
	}
	if _, err = tx.ExecContext(c.ctx, `UPDATE managed_records SET current_value=?,current_ttl=?,current_enabled=? WHERE id=? AND binding_generation=?`, actual.Value, actual.TTL, actual.Enabled, r.ManagedRecordID, r.BindingGeneration); err != nil {
		return true, err
	}
	if a.Kind == "auto_restore" || a.Kind == "auto_withdraw" {
		_, err = tx.ExecContext(c.ctx, `UPDATE entry_member_publications SET expected_value=?,expected_enabled=?,reason='观测模式：未调用的启停步骤已作废' WHERE group_id=? AND node_id=(SELECT node_id FROM managed_records WHERE id=?) AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND current_action_id=? AND desired_version=?)`, actual.Value, actual.Enabled, a.GroupID, r.ManagedRecordID, a.GroupID, a.ID, a.Version)
	}
	if err != nil {
		return true, err
	}
	return true, tx.Commit()
}
