package panel

import (
	"context"
	"errors"

	"nbpass/internal/dnsprovider"
)

func (c *entryCoordinator) execute(a EntryAction) error {
	rows, err := loadActionRecords(c.ctx, c.p.db, a.ID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("入口动作缺少记录身份，等待人工核对")
	}
	var pending error
	for _, r := range rows {
		if r.Stage != EntryDesired && r.Stage != EntryUnconfirmed {
			continue
		}
		if err := c.executeRecord(a, r); err != nil {
			pending = err
		}
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
	}
	if err := c.aggregate(a); err != nil {
		return err
	}
	return pending
}

func (c *entryCoordinator) executeRecord(a EntryAction, r EntryActionRecord) error {
	p := c.p
	// worker 可能在开关事务之前取到队列快照，不能把作废行重新写活。
	var stage string
	if err := p.db.QueryRowContext(c.ctx, "SELECT stage,write_started FROM entry_action_records WHERE action_id=? AND managed_record_id=?", a.ID, r.ManagedRecordID).Scan(&stage, &r.writeStarted); err != nil {
		return err
	}
	if stage != EntryDesired && stage != EntryUnconfirmed {
		return nil
	}
	b, err := c.boundRecord(a, r)
	if err != nil {
		return err
	}
	allowed, err := recoveryRecordAllowed(c.ctx, p.db, b.ID, b.BindingGeneration)
	if err != nil {
		return err
	}
	if !allowed {
		return c.recordNote(a, r, "恢复后的受管记录尚未核对，自动写暂停")
	}
	if a.GroupID != "" {
		allowed, err = recoveryGroupAllowed(c.ctx, p.db, a.GroupID)
	} else {
		allowed, err = p.recoveryNodeAllowed(c.ctx, p.db, a.OwnerNodeID, false)
	}
	if err != nil {
		return err
	}
	if !allowed {
		return c.recordNote(a, r, "从旧备份恢复，入口尚未核对，自动写暂停")
	}
	current, err := c.currentVersion(c.ctx, p.db, a)
	if err != nil {
		return err
	}
	if !current && !r.writeStarted {
		return c.recordResult(a, r, EntrySuperseded, "期望版本已变化，旧动作失效")
	}
	credentials, err := p.dnsCredential(c.ctx, b.AccountID)
	if err != nil {
		return c.recordNote(a, r, "DNSPod 凭据不可用，等待核对")
	}
	if p.deps.DNSProvider == nil {
		return c.recordNote(a, r, "DNS 服务商不可用，等待核对")
	}
	key := dnsprovider.Key{Domain: r.Domain, RecordID: r.RecordID}
	auth := credentials.provider()
	ctx, cancel := p.dnsContext(c.ctx)
	actual, err := p.deps.DNSProvider.ReadRecord(ctx, auth, key)
	cancel()
	if err != nil {
		return c.recordNote(a, r, "DNSPod 读回失败，等待核对")
	}
	if _, err := recordWarnings(actual, r.RecordID, b.LineID); err != nil || actual.Record.Name != b.Name || b.OwnerKind == "group" && !actual.Record.Enabled && !r.explicitEnable {
		return c.recordNote(a, r, "受管记录类型、名称、启用状态或线路变化，等待人工核对")
	}
	if r.preserveEnabled {
		r.NewEnabled = actual.Record.Enabled
	}
	value := r.NewValue
	if value == "" {
		value = actual.Record.Value
		r.NewValue = value
	}
	if cancelled, err := c.cancelObservedRecordRemainder(a, r, actual.Record); err != nil || cancelled {
		return err
	}
	if actual.Record.Value == value && actual.Record.TTL == r.NewTTL && actual.Record.Enabled == r.NewEnabled && (!r.explicitEnable || r.statusStarted) {
		return c.recordResult(a, r, EntryWritten, "")
	}
	if !current {
		return c.recordResult(a, r, EntrySuperseded, "已读回旧动作，期望版本已变化，不再写入")
	}
	if r.writeStarted && actual.Record.Value != r.OldValue && actual.Record.Value != value {
		return c.recordNote(a, r, "实际记录与动作旧值、新值均不同，等待人工核对")
	}
	if !r.writeStarted {
		r.OldValue, r.OldTTL, r.OldEnabled = actual.Record.Value, actual.Record.TTL, actual.Record.Enabled
	}
	// 改值始终保持读回的启用状态；启用须另走明确的状态步骤。
	if actual.Record.Value != value || actual.Record.TTL != r.NewTTL {
		desired := actual.Record
		desired.Value, desired.TTL = value, r.NewTTL
		ctx, cancel = p.dnsContext(c.ctx)
		started, writeErr := c.dispatchRecordWrite(a, r, func() error { return p.deps.DNSProvider.WriteRecord(ctx, auth, key, desired) })
		cancel()
		err = writeErr
		if errors.Is(err, errObservationWrite) {
			if cancelled, e := c.cancelObservedRecordRemainder(a, r, actual.Record); e != nil || cancelled {
				return e
			}
			if r.writeStarted {
				return c.recordNote(a, r, err.Error())
			}
			return nil
		}
		if !started {
			return err
		}
		r.writeStarted = true
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		if err != nil {
			return c.recordWriteError(a, r, err)
		}
		actual.Record = desired
	}
	if err := c.confirmRecordValue(a, r, actual.Record); err != nil {
		return err
	}
	if actual.Record.Enabled != r.NewEnabled || r.explicitEnable && !r.statusStarted {
		if !r.NewEnabled && a.MinimumEnabled > 0 {
			var remaining int
			err := p.db.QueryRowContext(c.ctx, `SELECT count(*) FROM managed_records m WHERE group_id=? AND id<>? AND current_enabled=1 AND NOT EXISTS(SELECT 1 FROM entry_action_records r WHERE r.managed_record_id=m.id AND r.binding_generation=m.binding_generation AND r.new_enabled=0 AND r.status_started=1 AND r.stage IN `+pendingRecordStages+`)`, a.GroupID, r.ManagedRecordID).Scan(&remaining)
			if err != nil {
				return err
			}
			if remaining < a.MinimumEnabled {
				var enabling int
				if err := p.db.QueryRowContext(c.ctx, `SELECT count(*) FROM entry_action_records WHERE action_id=? AND new_enabled=1 AND stage IN `+pendingRecordStages, a.ID).Scan(&enabling); err != nil {
					return err
				}
				if enabling > 0 {
					return c.recordNote(a, r, "目标启用尚未确认，先核对再暂停最后的原记录")
				}
				return c.recordResult(a, r, EntryFailed, "目标启用尚未确认，保留最后的已启用记录")
			}
		}
		ctx, cancel = p.dnsContext(c.ctx)
		statusRecord := r
		statusRecord.statusStarted = true
		started, writeErr := c.dispatchRecordWrite(a, statusRecord, func() error { return p.deps.DNSProvider.SetRecordEnabled(ctx, auth, key, r.NewEnabled) })
		cancel()
		err = writeErr
		if errors.Is(err, errObservationWrite) {
			if cancelled, e := c.cancelObservedRecordRemainder(a, r, actual.Record); e != nil || cancelled {
				return e
			}
			if r.writeStarted {
				return c.recordNote(a, r, err.Error())
			}
			return nil
		}
		if !started {
			return err
		}
		r.writeStarted = true
		r.statusStarted = true
		if c.ctx.Err() != nil {
			return c.ctx.Err()
		}
		if err != nil {
			return c.recordWriteError(a, r, err)
		}
	}
	return c.recordResult(a, r, EntryWritten, "")
}

func (c *entryCoordinator) boundRecord(a EntryAction, r EntryActionRecord) (managedRecord, error) {
	b, err := scanManaged(c.p.db.QueryRowContext(c.ctx, `SELECT `+managedColumns+` FROM managed_records WHERE id=? AND binding_generation=? AND provider=? AND domain=? AND record_id=? AND ((group_id=? AND ?<>'') OR (owner_kind='node' AND node_id=? AND ?<>''))`, r.ManagedRecordID, r.BindingGeneration, r.Provider, r.Domain, r.RecordID, a.GroupID, a.GroupID, a.OwnerNodeID, a.OwnerNodeID))
	return b, err
}

func (c *entryCoordinator) currentVersion(ctx context.Context, q recoveryQuerier, a EntryAction) (bool, error) {
	var current bool
	query := `SELECT EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND desired_version=? AND current_action_id=?)`
	owner := a.GroupID
	if a.OwnerNodeID != "" {
		query = `SELECT EXISTS(SELECT 1 FROM node_publications WHERE node_id=? AND desired_version=? AND current_action_id=?)`
		owner = a.OwnerNodeID
	}
	err := q.QueryRowContext(ctx, query, owner, a.Version, a.ID).Scan(&current)
	return current, err
}

func (c *entryCoordinator) markRecordWriting(a EntryAction, r EntryActionRecord) error {
	tx, err := c.p.db.BeginTx(c.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := c.currentVersion(c.ctx, tx, a)
	if err != nil {
		return err
	}
	if !current {
		return errors.New("期望版本已变化，等待重新核对")
	}
	if err := missingResult(tx.ExecContext(c.ctx, `UPDATE entry_actions SET old_value=CASE WHEN write_started=0 THEN ? ELSE old_value END,old_ttl=CASE WHEN write_started=0 THEN ? ELSE old_ttl END,write_started=1,last_error='' WHERE id=? AND version=? AND stage NOT IN `+terminalActionStages, r.OldValue, r.OldTTL, a.ID, a.Version)); err != nil {
		return err
	}
	err = missingResult(tx.ExecContext(c.ctx, `UPDATE entry_action_records SET old_value=?,old_ttl=?,old_enabled=?,write_started=1,status_started=?,new_enabled=?,last_error='' WHERE action_id=? AND managed_record_id=? AND binding_generation=? AND stage IN `+pendingRecordStages+` AND EXISTS(SELECT 1 FROM managed_records m WHERE m.id=managed_record_id AND m.binding_generation=entry_action_records.binding_generation AND m.provider=entry_action_records.provider AND m.domain=entry_action_records.domain AND m.record_id=entry_action_records.record_id)`, r.OldValue, r.OldTTL, r.OldEnabled, r.statusStarted, r.NewEnabled, a.ID, r.ManagedRecordID, r.BindingGeneration))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (c *entryCoordinator) recordWriteError(a EntryAction, r EntryActionRecord, err error) error {
	var rejected *dnsprovider.RejectionError
	if errors.As(err, &rejected) {
		return c.recordResult(a, r, EntryFailed, rejected.Error())
	}
	return c.recordNote(a, r, "DNSPod 写入结果未确认，先读回核对")
}

func (c *entryCoordinator) recordNote(a EntryAction, r EntryActionRecord, message string) error {
	stage := EntryDesired
	if r.writeStarted {
		stage = EntryUnconfirmed
	}
	if err := c.recordResult(a, r, stage, message); err != nil {
		return err
	}
	return errors.New(message)
}

func (c *entryCoordinator) recordResult(a EntryAction, r EntryActionRecord, stage, message string) error {
	tx, err := c.p.db.BeginTx(c.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 迟到回包必须同时匹配动作、远端身份、绑定代次与未终结状态。
	var written any
	if stage == EntryWritten {
		written = c.p.deps.Clock.Now().UnixNano()
	}
	err = missingResult(tx.ExecContext(c.ctx, `UPDATE entry_action_records SET stage=?,last_error=?,write_started=?,new_enabled=?,written_at=?,value_confirmed=MAX(value_confirmed,?),new_value=? WHERE action_id=? AND managed_record_id=? AND binding_generation=? AND provider=? AND domain=? AND record_id=? AND stage IN `+pendingRecordStages+` AND EXISTS(SELECT 1 FROM entry_actions a WHERE a.id=action_id AND a.version=? AND a.stage NOT IN `+terminalActionStages+`) AND EXISTS(SELECT 1 FROM managed_records m WHERE m.id=managed_record_id AND m.binding_generation=entry_action_records.binding_generation AND m.provider=entry_action_records.provider AND m.domain=entry_action_records.domain AND m.record_id=entry_action_records.record_id)`, stage, message, r.writeStarted, r.NewEnabled, written, stage == EntryWritten, r.NewValue, a.ID, r.ManagedRecordID, r.BindingGeneration, r.Provider, r.Domain, r.RecordID, a.Version))
	if err != nil {
		return err
	}
	if stage == EntryFailed {
		_, err = tx.ExecContext(c.ctx, `UPDATE entry_member_publications SET last_action_error=?,reason=CASE WHEN ? THEN '启用失败' ELSE '暂停失败' END WHERE group_id=? AND node_id=(SELECT node_id FROM managed_records WHERE id=?)`, message, r.NewEnabled, a.GroupID, r.ManagedRecordID)
	}
	if stage == EntryWritten {
		_, err = tx.ExecContext(c.ctx, `UPDATE managed_records SET current_value=CASE WHEN ?='' THEN current_value ELSE ? END,current_ttl=?,current_enabled=? WHERE id=? AND binding_generation=? AND provider=? AND domain=? AND record_id=?`, r.NewValue, r.NewValue, r.NewTTL, r.NewEnabled, r.ManagedRecordID, r.BindingGeneration, r.Provider, r.Domain, r.RecordID)
	}
	if err == nil && stage == EntryWritten && r.preserveEnabled {
		// 同步按执行时读回的实际状态改地址；成员期望也须采用这个状态，
		// 避免审计把保留的暂停误当偏差重新启用。迟到旧动作不得覆盖新期望。
		_, err = tx.ExecContext(c.ctx, `UPDATE entry_member_publications SET reason=CASE WHEN expected_enabled<>? THEN CASE WHEN ? THEN '已发布' ELSE '暂停待加回' END ELSE reason END,expected_enabled=? WHERE group_id=? AND node_id=(SELECT node_id FROM managed_records WHERE id=? AND binding_generation=? AND owner_kind='member') AND EXISTS(SELECT 1 FROM entry_publications s WHERE s.group_id=entry_member_publications.group_id AND s.desired_version=? AND s.current_action_id=?)`, r.NewEnabled, r.NewEnabled, r.NewEnabled, a.GroupID, r.ManagedRecordID, r.BindingGeneration, a.Version, a.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (c *entryCoordinator) confirmRecordValue(a EntryAction, r EntryActionRecord, actual dnsprovider.Record) error {
	tx, err := c.p.db.BeginTx(c.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = missingResult(tx.ExecContext(c.ctx, `UPDATE entry_action_records SET value_confirmed=1,new_value=? WHERE action_id=? AND managed_record_id=? AND binding_generation=? AND stage IN `+pendingRecordStages+` AND EXISTS(SELECT 1 FROM entry_actions WHERE id=? AND version=? AND stage NOT IN `+terminalActionStages+`) AND EXISTS(SELECT 1 FROM managed_records m WHERE m.id=managed_record_id AND m.binding_generation=entry_action_records.binding_generation AND m.provider=entry_action_records.provider AND m.domain=entry_action_records.domain AND m.record_id=entry_action_records.record_id)`, actual.Value, a.ID, r.ManagedRecordID, r.BindingGeneration, a.ID, a.Version))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(c.ctx, `UPDATE managed_records SET current_value=?,current_ttl=?,current_enabled=? WHERE id=? AND binding_generation=?`, actual.Value, actual.TTL, actual.Enabled, r.ManagedRecordID, r.BindingGeneration)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (c *entryCoordinator) aggregate(a EntryAction) error {
	p := c.p
	tx, err := p.db.BeginTx(c.ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 写前读回会修正旧值/TTL；传播窗口必须使用已落盘的实际旧 TTL。
	a, err = scanAction(tx.QueryRowContext(c.ctx, "SELECT "+actionColumns+" FROM entry_actions WHERE id=?", a.ID))
	if err != nil {
		return err
	}
	if a.Stage == EntrySuperseded || a.Stage == EntryArchived {
		return nil
	}
	var pending, confirmed, failed, unknown, total, cancelledStarted int
	var message string
	err = tx.QueryRowContext(c.ctx, `SELECT count(*),coalesce(sum(stage IN `+pendingRecordStages+`),0),coalesce(sum(stage=?),0),coalesce(sum(stage=?),0),coalesce(sum(stage=?),0),coalesce(max(nullif(last_error,'')),''),coalesce(sum(write_started=1 AND stage='已失效'),0) FROM entry_action_records WHERE action_id=?`, EntryWritten, EntryFailed, EntryUnconfirmed, a.ID).Scan(&total, &pending, &confirmed, &failed, &unknown, &message, &cancelledStarted)
	if err != nil {
		return err
	}
	stage := EntryDesired
	switch {
	case pending > 0 && unknown > 0:
		stage = EntryUnconfirmed
	case pending > 0:
		stage = EntryDesired
	case confirmed == total:
		stage = EntryWritten
	case confirmed > 0 || cancelledStarted > 0:
		stage = EntryPartial
	case failed > 0:
		stage = EntryFailed
	default:
		stage = EntrySuperseded
	}
	var written any
	now := p.deps.Clock.Now()
	if stage == EntryWritten {
		written = now.UnixNano()
	}
	if err := missingResult(tx.ExecContext(c.ctx, `UPDATE entry_actions SET stage=?,last_error=?,written_at=? WHERE id=? AND version=? AND stage NOT IN `+terminalActionStages, stage, message, written, a.ID, a.Version)); err != nil {
		return err
	}
	if a.GroupID != "" {
		_, err = tx.ExecContext(c.ctx, `UPDATE entry_publications SET written_at=? WHERE group_id=? AND current_action_id=? AND desired_version=?`, written, a.GroupID, a.ID, a.Version)
		if err == nil {
			_, err = tx.ExecContext(c.ctx, `UPDATE entry_groups SET publication_status=? WHERE id=? AND EXISTS(SELECT 1 FROM entry_publications WHERE group_id=? AND current_action_id=? AND desired_version=?)`, stage, a.GroupID, a.GroupID, a.ID, a.Version)
		}
	} else {
		_, err = tx.ExecContext(c.ctx, `UPDATE node_publications SET written_at=?,published=CASE WHEN ?='first_publish' AND ?='服务商已写入' THEN 1 ELSE published END WHERE node_id=? AND current_action_id=? AND desired_version=?`, written, a.Kind, stage, a.OwnerNodeID, a.ID, a.Version)
	}
	if err == nil {
		err = p.actionEvent(c.ctx, tx, a, stage)
	}
	if err == nil && a.GroupID != "" {
		var mode string
		err = tx.QueryRowContext(c.ctx, "SELECT publication_mode FROM entry_groups WHERE id=?", a.GroupID).Scan(&mode)
		if err == nil && mode == "all" {
			if confirmed > 0 {
				_, err = tx.ExecContext(c.ctx, `UPDATE entry_groups SET first_published_at=coalesce(first_published_at,?) WHERE id=? AND EXISTS(SELECT 1 FROM entry_action_records WHERE action_id=? AND new_enabled=1 AND stage=?)`, now.UnixNano(), a.GroupID, a.ID, EntryWritten)
			}
			if err == nil {
				err = p.refreshAllWatchTx(c.ctx, tx, a, now)
			}
		}
	}
	// 主备组沿用单地址传播。
	if err == nil && stage == EntryWritten && a.GroupID != "" && a.NewValue != "" {
		err = p.beginEntryPropagation(c.ctx, tx, a, now)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		p.wakeDNSWatch()
		p.finishAllMemberRemovals(c.ctx)
	}
	return err
}

func recoveryRecordAllowed(ctx context.Context, q recoveryQuerier, id string, generation int64) (bool, error) {
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM recovery_state) OR EXISTS(SELECT 1 FROM recovery_records WHERE managed_record_id=? AND binding_generation=? AND verified=1)`, id, generation).Scan(&allowed)
	return allowed, err
}
