package panel

import (
	"context"
	"database/sql"
	"errors"
)

// 受控换 IP 已确认但同组仍有在途动作时，保留逐记录收尾意图。
// 由既有节点策略循环持 controlMu 调度，不调用 DNS；实际写仍只有记录协调器。
func (p *Panel) reconcileNodeAddressSyncs(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, "SELECT managed_record_id,binding_generation,node_id,address,kind FROM node_address_sync_jobs ORDER BY managed_record_id")
	if err != nil {
		return err
	}
	type job struct {
		record, node, address, kind string
		generation                  int64
	}
	jobs := []job{}
	for rows.Next() {
		var j job
		if err = rows.Scan(&j.record, &j.generation, &j.node, &j.address, &j.kind); err != nil {
			break
		}
		jobs = append(jobs, j)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		b, err := scanManaged(tx.QueryRowContext(ctx, "SELECT "+managedColumns+" FROM managed_records WHERE id=? AND binding_generation=?", j.record, j.generation))
		var address string
		if err == nil {
			err = tx.QueryRowContext(ctx, "SELECT confirmed_ipv4 FROM node_regions WHERE node_id=?", j.node).Scan(&address)
		}
		if errors.Is(err, sql.ErrNoRows) || err == nil && address != j.address {
			_, err = tx.ExecContext(ctx, "DELETE FROM node_address_sync_jobs WHERE managed_record_id=?", j.record)
			if err == nil {
				err = tx.Commit()
			}
			tx.Rollback()
			if err != nil {
				return err
			}
			continue
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		pending, err := recordOwnerPending(ctx, tx, b)
		if err != nil {
			tx.Rollback()
			return err
		}
		if pending {
			tx.Rollback()
			continue
		}
		// 先移除待办再提交动作，同事务失败会还原待办。无需抬高其他成员的在途版本。
		_, err = tx.ExecContext(ctx, "DELETE FROM node_address_sync_jobs WHERE managed_record_id=?", j.record)
		var a EntryAction
		if err == nil {
			a, err = p.submitNodeRecordAddressTx(ctx, tx, b, j.address, j.kind)
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
		p.coordinator.wake(actionWorkerKey(a))
	}
	return nil
}
