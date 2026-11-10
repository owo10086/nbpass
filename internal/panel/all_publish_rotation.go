package panel

import (
	"context"
	"database/sql"
	"fmt"
)

type rotationMember struct {
	group, record                string
	version, generation, binding int64
	minimum                      int
	enabled                      bool
}

// 未首发的组不自动发布；无绑定的新增成员没有可摘除的远端记录。
const rotationMembersFrom = ` FROM entry_groups g JOIN entry_group_members m ON m.group_id=g.id
 JOIN entry_publications s ON s.group_id=g.id
 JOIN managed_records r ON r.group_id=g.id AND r.node_id=m.node_id AND r.owner_kind='member'
 JOIN entry_member_publications p ON p.group_id=g.id AND p.node_id=m.node_id
 WHERE m.node_id=? AND g.publication_mode='all' AND g.first_published_at IS NOT NULL`

func loadRotationMembers(ctx context.Context, q queryer, node string) ([]rotationMember, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id,s.desired_version,g.minimum_kept_generation,
 MIN(g.minimum_kept,(SELECT count(*) FROM entry_group_members WHERE group_id=g.id)),r.id,r.binding_generation,p.expected_enabled`+rotationMembersFrom+` ORDER BY g.id`, node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []rotationMember
	for rows.Next() {
		var m rotationMember
		if err = rows.Scan(&m.group, &m.version, &m.generation, &m.minimum, &m.record, &m.binding, &m.enabled); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// 只持 controlMu，绝不反向获取 entryMu。全部写入沿用事务协调器及自动摘除唯一提交点。
// 同组其他异常一起交给票 05 选择，不能逐个抢占最少保留数。
func (p *Panel) prepareAllMemberRotation(ctx context.Context, node string) ([]rotationMember, error) {
	members, err := loadRotationMembers(ctx, p.db, node)
	if err != nil {
		return nil, err
	}
	for _, b := range members {
		allowed, err := recoveryGroupAllowed(ctx, p.db, b.group)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, &forwardingError{409, "全部发布组恢复核对尚未完成"}
		}
		var pending bool
		if err = p.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entry_actions WHERE group_id=? AND stage IN `+pendingRecordStages+`)`, b.group).Scan(&pending); err != nil {
			return nil, err
		}
		if pending {
			continue
		}
		all, err := loadAllMembers(ctx, p.db, b.group)
		if err != nil {
			return nil, err
		}
		reasons := map[string]allWithdrawal{}
		for _, m := range all {
			if m.Record == nil || !m.ExpectedEnabled || !m.Record.CurrentEnabled || m.RemovalActionID != "" || m.LastError != "" {
				continue
			}
			r, err := p.allWithdrawalReason(ctx, b.group, m)
			if err != nil {
				return nil, err
			}
			if m.NodeID == node && r.reason == "" {
				r = allWithdrawal{reason: "跨组换 IP 自动摘除", identity: fmt.Sprint(b.version)}
			}
			if r.reason != "" {
				reasons[m.NodeID] = r
			}
		}
		if err = p.decideAllWithdrawals(ctx, b.group, reasons); err != nil {
			return nil, err
		}
	}
	// 动作提交会抬高版本；只保存实际核对过且已完成的版本。
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	members, err = loadRotationMembers(ctx, tx, node)
	if err != nil {
		return nil, err
	}
	current, err := rotationMembersCurrent(ctx, tx, node, members)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, &forwardingError{409, "全部发布组摘除尚未确认或尚未满足最少保留数屏障"}
	}
	return members, nil
}

// 授权事务与首次许可投递共用此复核。版本、身份或保留数变化都不能复用旧决定。
func rotationMembersCurrent(ctx context.Context, tx *sql.Tx, node string, members []rotationMember) (bool, error) {
	current, err := loadRotationMembers(ctx, tx, node)
	if err != nil {
		return false, err
	}
	if len(current) != len(members) {
		return false, nil
	}
	for i, b := range current {
		if b != members[i] {
			return false, nil
		}
		var ready bool
		err = tx.QueryRowContext(ctx, `SELECT r.current_enabled=p.expected_enabled AND p.last_action_error='' AND p.removal_action_id=''
 AND NOT EXISTS(SELECT 1 FROM entry_actions a WHERE a.group_id=g.id AND a.stage IN `+pendingRecordStages+`)
 AND (p.expected_enabled=0 OR (SELECT count(*) FROM managed_records x JOIN entry_member_publications y ON y.group_id=x.group_id AND y.node_id=x.node_id
 WHERE x.group_id=g.id AND x.owner_kind='member' AND x.current_enabled=1 AND y.expected_enabled=1 AND y.last_action_error='')<=?)`+rotationMembersFrom+` AND g.id=? AND r.id=? AND r.binding_generation=?`, b.minimum, node, b.group, b.record, b.binding).Scan(&ready)
		if err != nil || !ready {
			return false, err
		}
	}
	return true, nil
}

func saveRotationMembers(ctx context.Context, tx *sql.Tx, action string, members []rotationMember) error {
	for _, m := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rotation_member_barriers(action_id,group_id,desired_version,minimum_generation,minimum_kept,managed_record_id,binding_generation,enabled) VALUES(?,?,?,?,?,?,?,?)`, action, m.group, m.version, m.generation, m.minimum, m.record, m.binding, m.enabled); err != nil {
			return err
		}
	}
	return nil
}

func rotationMemberPermitCurrent(ctx context.Context, tx *sql.Tx, node, action string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_id,desired_version,minimum_generation,minimum_kept,managed_record_id,binding_generation,enabled FROM rotation_member_barriers WHERE action_id=? ORDER BY group_id`, action)
	if err != nil {
		return false, err
	}
	var members []rotationMember
	for rows.Next() {
		var m rotationMember
		if err = rows.Scan(&m.group, &m.version, &m.generation, &m.minimum, &m.record, &m.binding, &m.enabled); err != nil {
			break
		}
		members = append(members, m)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	return rotationMembersCurrent(ctx, tx, node, members)
}
