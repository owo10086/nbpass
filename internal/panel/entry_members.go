package panel

import (
	"context"
	"database/sql"
	"slices"
)

func containsEntryMember(members []string, id string) bool { return slices.Contains(members, id) }

func validateEntryMembers(ctx context.Context, tx *sql.Tx, members []string) error {
	seen := make(map[string]bool, len(members))
	for _, id := range members {
		if id == "" || seen[id] {
			return &forwardingError{400, "入口组成员不能为空或重复"}
		}
		seen[id] = true
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM nodes WHERE id=?", id).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return &forwardingError{400, "入口成员不存在"}
		}
	}
	return nil
}

func protectPendingEntryMembers(ctx context.Context, tx *sql.Tx, gid string, members []string) error {
	// 期望已切到新成员时，服务商可能仍指向旧成员；在途动作结束前保留
	// 全部现有成员，避免配置撤销先于 DNS 确认而中断旧入口的流量。
	rows, err := tx.QueryContext(ctx, `SELECT node_id FROM entry_group_members WHERE group_id=? AND EXISTS(SELECT 1 FROM entry_actions WHERE group_id=? AND stage<>?)`, gid, gid, EntryWritten)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if !containsEntryMember(members, id) {
			return &forwardingError{409, "入口动作尚未核对完成，不能移除成员"}
		}
	}
	return rows.Err()
}
