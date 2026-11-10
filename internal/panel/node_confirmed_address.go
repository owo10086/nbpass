package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"nbpass/internal/eventlog"
	"nbpass/internal/publicip"
)

// confirmNodeAddressTx 仅由受控换 IP 的首次成功确认或管理员手动同步调用。
// 活性上报继续保存 public_ipv4，绝不能以普通上报覆盖此地址。
// 票 03/04 的手动同步须在同一事务提交相应的逐记录动作，再统一唤醒协调器。
func (p *Panel) confirmNodeAddressTx(ctx context.Context, tx *sql.Tx, nodeID, address string) error {
	return confirmNodeAddressAtTx(ctx, tx, nodeID, address, p.deps.Clock.Now().UnixNano())
}

func confirmNodeAddressAtTx(ctx context.Context, tx *sql.Tx, nodeID, address string, at int64) error {
	ip, err := publicip.Parse(address)
	if err != nil {
		return &forwardingError{400, "请确认单个可入站公网 IPv4"}
	}
	return missingResult(tx.ExecContext(ctx, `INSERT INTO node_regions(node_id,confirmed_ipv4,confirmed_at) SELECT id,?,? FROM nodes WHERE id=? ON CONFLICT(node_id) DO UPDATE SET confirmed_ipv4=excluded.confirmed_ipv4,confirmed_at=excluded.confirmed_at,address_notice_ipv4=''`, ip, at, nodeID))
}

// 只通知已知确认地址的偏差；首次观测不暗中建立确认地址。通知失败由后台续投。
func (p *Panel) notifyNodeAddressDrift(ctx context.Context) error {
	rows, err := p.db.QueryContext(ctx, `SELECT node_id,public_ipv4,confirmed_ipv4 FROM node_regions WHERE confirmed_ipv4<>'' AND public_ipv4<>'' AND public_ipv4<>confirmed_ipv4 AND address_notice_ipv4<>public_ipv4`)
	if err != nil {
		return err
	}
	type drift struct{ node, reported, confirmed string }
	var items []drift
	for rows.Next() {
		var d drift
		if err = rows.Scan(&d.node, &d.reported, &d.confirmed); err != nil {
			break
		}
		items = append(items, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range items {
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var valid bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_regions WHERE node_id=? AND public_ipv4=? AND confirmed_ipv4=? AND address_notice_ipv4<>?)`, d.node, d.reported, d.confirmed, d.reported).Scan(&valid)
		if err != nil || !valid {
			tx.Rollback()
			if err != nil {
				return err
			}
			continue
		}
		now := p.deps.Clock.Now()
		key := fmt.Sprintf("node-address:%x", sha256.Sum256([]byte(d.node+":"+d.reported+":"+d.confirmed)))
		message := fmt.Sprintf("节点 %s 上报地址变化为 %s，面板确认地址仍为 %s；受管记录保持原状，请管理员手动同步地址", d.node, d.reported, d.confirmed)
		err = insertEvent(ctx, tx, d.node, "panel", eventlog.Event{ID: key, Type: "entry_action", OccurredAt: now, Message: message}, now)
		if err == nil {
			err = queueTelegram(ctx, tx, key, d.node, []string{message}, now)
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE node_regions SET address_notice_ipv4=? WHERE node_id=?", d.reported, d.node)
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
		p.wakeTelegram()
	}
	return nil
}
