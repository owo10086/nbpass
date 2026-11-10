package panel

import (
	"context"
	"database/sql"
	"fmt"
)

// 迁移35：跨组影响复用被墙决策，去重标记独立于可清理的事件历史。
const crossGroupRotationMigration = `ALTER TABLE entry_blocked_decisions ADD COLUMN cross_rotation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE entry_blocked_decisions ADD COLUMN cross_notified INTEGER NOT NULL DEFAULT 0;
CREATE TABLE rotation_entry_barriers (
 action_id TEXT NOT NULL REFERENCES rotation_actions(id) ON DELETE CASCADE,
 group_id TEXT NOT NULL, desired_version INTEGER NOT NULL, policy TEXT NOT NULL,
 PRIMARY KEY(action_id,group_id)
);`

type rotationEntry struct {
	group, active, ip, policy, incident string
	version                             int64
	written                             sql.NullInt64
}

const rotationEntriesFrom = ` FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id AND g.publication_mode='primary_backup'
 JOIN entry_group_members m ON m.group_id=g.id WHERE m.node_id=?`

func (p *Panel) rotationEntries(ctx context.Context, node string) ([]rotationEntry, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT s.group_id,s.active_node_id,s.expected_value,g.blocked_policy,s.desired_version,s.written_at,
 COALESCE((SELECT incident_id FROM entry_blocked_incidents WHERE node_id=m.node_id),'cross:'||m.node_id||':'||s.expected_value)`+rotationEntriesFrom+` ORDER BY s.group_id`, node)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []rotationEntry
	for rows.Next() {
		var e rotationEntry
		if err = rows.Scan(&e.group, &e.active, &e.ip, &e.policy, &e.version, &e.written, &e.incident); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// 调用方只持 controlMu。复用事务协调器，不反向获取 entryMu，也不在此调用 DNS。
// 切离以当前期望的写入确认为准；传播观察不阻挡许可。未发布的组不自动发布。
func (p *Panel) prepareCrossGroupRotation(ctx context.Context, node string) ([]rotationEntry, error) {
	entries, err := p.rotationEntries(ctx, node)
	if err != nil {
		return nil, err
	}
	// 未发布的组不制造首次发布，但仍算节点的跨组归属。
	var groups int
	if err = p.db.QueryRowContext(ctx, "SELECT count(*) FROM entry_group_members WHERE node_id=?", node).Scan(&groups); err != nil {
		return nil, err
	}
	// 单组策略①仍保持仅换 IP；跨组时才为其他组共同切离。
	if groups < 2 {
		return entries, nil
	}
	states, err := p.checkEntryCandidates(ctx)
	if err != nil {
		return nil, err
	}
	waiting := false
	for _, e := range entries {
		if !e.written.Valid {
			waiting = true
			continue
		}
		if e.active != node {
			continue
		}
		for key, state := range states {
			if key[0] == e.group && key[1] != node && state.Candidate {
				waiting = true
			}
		}
		if err = p.submitBlockedSwitch(ctx, blockedEntry{group: e.group, node: node, ip: e.ip, version: e.version, incident: e.incident, policy: e.policy, crossRotation: true}, states); err != nil {
			return nil, err
		}
	}
	if err = p.notifyCrossGroupRotation(ctx, node); err != nil {
		return nil, err
	}
	if waiting {
		return nil, &forwardingError{409, "跨组入口切离尚未全部写入 DNSPod，等待核对"}
	}
	// 返回实际检查过的版本，不能重新读取并把并发手动操作冒充已通过的屏障。
	return entries, nil
}

// 手动切换不持 controlMu；授权最终事务须再次比对每个组的成员归属和期望版本。
func rotationEntriesCurrent(ctx context.Context, tx *sql.Tx, node string, entries []rotationEntry) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*)`+rotationEntriesFrom, node).Scan(&count); err != nil {
		return false, err
	}
	if count != len(entries) {
		return false, nil
	}
	for _, e := range entries {
		var current bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+rotationEntriesFrom+`
 AND s.group_id=? AND s.desired_version=? AND s.active_node_id=? AND s.expected_value=? AND g.blocked_policy=?
 AND s.written_at IS NOT NULL)`, node, e.group, e.version, e.active, e.ip, e.policy).Scan(&current)
		if err != nil || !current {
			return false, err
		}
	}
	return true, nil
}

// 屏障与许可同事务保存。即使授权后重启，首次投递仍核对原组/版本；
// 可能已经投递的许可只续观察，不根据新期望假定商家未执行。
func (p *Panel) markRotationPermit(ctx context.Context, node, action, origin string) (bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var sent bool
	if err = tx.QueryRowContext(ctx, "SELECT permit_sent FROM rotation_actions WHERE id=?", action).Scan(&sent); err != nil {
		return false, err
	}
	if origin == "automatic" && !sent {
		var current bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM rotation_settings WHERE node_id=? AND automatic_enabled=1)
 AND (SELECT COUNT(*) FROM rotation_entry_barriers WHERE action_id=?)=(SELECT COUNT(*)`+rotationEntriesFrom+`)
 AND NOT EXISTS(SELECT 1 FROM rotation_entry_barriers b LEFT JOIN entry_publications s ON s.group_id=b.group_id
 LEFT JOIN entry_groups g ON g.id=b.group_id WHERE b.action_id=? AND
 (s.group_id IS NULL OR s.desired_version<>b.desired_version OR s.written_at IS NULL OR g.blocked_policy<>b.policy
 OR NOT EXISTS(SELECT 1 FROM entry_group_members m WHERE m.group_id=b.group_id AND m.node_id=?)))`, node, action, node, action, node).Scan(&current)
		if err != nil {
			return false, err
		}
		if !current {
			_, err = tx.ExecContext(ctx, `UPDATE rotation_actions SET state='cancelled',reason='跨组切离期望或节点自动授权已变化，未投递许可已取消',updated_at=? WHERE id=?`, p.deps.Clock.Now().UnixNano(), action)
			if err == nil {
				_, err = tx.ExecContext(ctx, "DELETE FROM rotation_quota WHERE action_id=?", action)
			}
			if err == nil {
				err = p.rotationNotice(ctx, tx, node, action, "cancelled", "跨组切离期望或节点自动授权已变化，未投递许可已取消且释放额度")
			}
			if err == nil {
				err = tx.Commit()
			}
			return false, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE rotation_actions SET permit_sent=1 WHERE id=?", action); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (p *Panel) notifyCrossGroupRotation(ctx context.Context, node string) error {
	rows, err := p.db.QueryContext(ctx, `SELECT id,group_id,policy,outcome FROM entry_blocked_decisions WHERE node_id=? AND cross_rotation=1 AND cross_notified=0`, node)
	if err != nil {
		return err
	}
	type notice struct{ id, group, policy, outcome string }
	var notices []notice
	for rows.Next() {
		var n notice
		if err = rows.Scan(&n.id, &n.group, &n.policy, &n.outcome); err != nil {
			break
		}
		notices = append(notices, n)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range notices {
		impact := "先切到就绪候选，等待 DNSPod 确认切离后才允许节点换 IP"
		if n.outcome == "no_candidate" {
			impact = "没有就绪候选，保留最后的解析；共用节点换 IP 期间可能中断，新地址确认后只更新当时仍活动于该节点的记录"
		}
		if n.policy == "switch_entry" || n.policy == "disabled" {
			impact += "；本组不申请换 IP，也不否决其他组的申请"
		}
		tx, err := p.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		err = p.rotationNotice(ctx, tx, node, n.id, "cross_group", fmt.Sprintf("跨组换 IP 影响：入口组 %s：%s", n.group, impact))
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE entry_blocked_decisions SET cross_notified=1 WHERE id=?`, n.id)
		}
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return err
		}
	}
	return nil
}
