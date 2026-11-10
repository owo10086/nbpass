package panel

import (
	"context"
	"database/sql"
	"nbpass/internal/forwarding"
)

func nullableRuleEntry(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// 普通保存和整批导入共用入口引用检查，二选一还由模型与数据库双重约束。
func validateRuleEntry(ctx context.Context, tx *sql.Tx, r forwarding.Rule) error {
	if (r.EntryGroupID == "") == (r.EntryNodeID == "") {
		return &forwardingError{400, "入口节点与入口组必须且只能指定一个"}
	}
	table, id, label := "entry_groups", r.EntryGroupID, "入口组"
	if r.EntryNodeID != "" {
		table, id, label = "nodes", r.EntryNodeID, "入口节点"
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id=?", id).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return &forwardingError{400, label + "不存在"}
	}
	return nil
}

func loadEntryGroupMembers(ctx context.Context, q queryer) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT group_id,node_id FROM entry_group_members ORDER BY group_id,position,node_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := map[string][]string{}
	for rows.Next() {
		var group, id string
		if err := rows.Scan(&group, &id); err != nil {
			return nil, err
		}
		members[group] = append(members[group], id)
	}
	return members, rows.Err()
}

// 轮换准入按规则实际入口取并集，停用和已解除关联的历史节点不参加。
func tunnelEntryNodes(ctx context.Context, tx *sql.Tx, exit string) (map[string]bool, error) {
	rules, err := loadRules(ctx, tx)
	if err != nil {
		return nil, err
	}
	members, err := loadEntryGroupMembers(ctx, tx)
	if err != nil {
		return nil, err
	}
	groups, err := loadExitGroups(ctx, tx)
	if err != nil {
		return nil, err
	}
	related := map[string]bool{}
	for _, g := range groups {
		if !containsEntryMember(g.Members, exit) {
			continue
		}
		for _, r := range rules {
			if r.Enabled && r.Kind == forwarding.Tunnel && r.ExitGroupID == g.ID {
				for _, id := range forwarding.RuleEntryNodes(r, members) {
					related[id] = true
				}
			}
		}
	}
	return related, nil
}
