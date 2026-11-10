package panel

import (
	"context"
	"fmt"
	"strings"
)

// 与节点策略循环同持 controlMu。事件/Telegram 沿用逐成员持久决策去重，
// 不访问外部网络，也不以通知失败阻塞 DNS 摘除或地址收尾。
func (p *Panel) observeAllMemberRotations(ctx context.Context, restrictions map[string]error) error {
	rows, err := p.db.QueryContext(ctx, `SELECT DISTINCT m.node_id FROM entry_group_members m
 JOIN entry_groups g ON g.id=m.group_id LEFT JOIN node_self_check c ON c.node_id=m.node_id
 WHERE g.publication_mode='all' AND g.first_published_at IS NOT NULL
 AND (c.enabled=1 AND c.state='blocked' OR EXISTS(SELECT 1 FROM rotation_member_barriers b JOIN rotation_actions a ON a.id=b.action_id WHERE a.node_id=m.node_id AND b.group_id=g.id))`)
	if err != nil {
		return err
	}
	var nodes []string
	for rows.Next() {
		var node string
		if err = rows.Scan(&node); err != nil {
			break
		}
		nodes = append(nodes, node)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if err = p.notifyAllMemberRotation(ctx, node, restrictions[node]); err != nil {
			return err
		}
	}
	return nil
}

func (p *Panel) notifyAllMemberRotation(ctx context.Context, node string, restriction error) error {
	var enabled bool
	var incident string
	err := p.db.QueryRowContext(ctx, `SELECT coalesce((SELECT automatic_enabled FROM rotation_settings WHERE node_id=?),0),coalesce((SELECT incident_id FROM entry_blocked_incidents WHERE node_id=?),'')`, node, node).Scan(&enabled, &incident)
	if err != nil {
		return err
	}
	a, err := p.latestRotation(ctx, node)
	if err != nil {
		return err
	}
	phase, message, action := "disabled", "节点未开启自动换 IP，只按最少保留数摘除并通知", ""
	if a != nil {
		action = a.ID
		switch {
		case a.State == "unconfirmed":
			phase, message = "frozen", "换 IP 结果未确认，节点已冻结，请核对或人工处理"
		case a.State == "cancelled":
			phase, message = "cancelled", "换 IP 失败或取消，保持摘除，请核对或人工处理"
		case a.RecheckStatus == "pending":
			phase, message = "pending", "新 IP 复检待确认，保持摘除，周期自测不能代替本次复检"
		case a.RecheckStatus == "no_improvement":
			phase, message = "no_improvement", "新 IP 复检无改善，保持摘除，继续受共享额度与无改善暂停限制"
		case a.RecheckStatus == "recovered":
			// 上次成功不代表本次被墙的申请未受限；仍需说明当前无法换 IP。
			if restriction == nil || !enabled {
				return nil
			}
			phase, message = "restricted", "全部发布组成员换 IP 受限，保持摘除："+restriction.Error()
		default:
			phase, message = "waiting", "全部发布组成员换 IP 进行中，保持摘除；新地址确认后仅改值，复检通过且连续六次检查成功才加回"
		}
	} else if enabled {
		phase, message = "waiting", "全部发布组成员等待换 IP，摘除未确认时不占用额度"
	}
	if restriction != nil && enabled && (a == nil || a.State == "released" || (a.State == "changed" && a.RecheckStatus == "no_improvement")) {
		// 额度限制与复检结论可同时存在，不能用下一次申请受限覆盖本次无改善。
		if phase == "no_improvement" {
			message += "；再次换 IP 受限：" + restriction.Error()
		} else {
			phase, message = "restricted", "全部发布组成员换 IP 受限，保持摘除："+restriction.Error()
		}
	}
	barriers, err := loadRotationMembers(ctx, p.db, node)
	if err != nil {
		return err
	}
	for _, b := range barriers {
		members, err := loadAllMembers(ctx, p.db, b.group)
		if err != nil {
			return err
		}
		for _, m := range members {
			if m.NodeID != node || m.Record == nil {
				continue
			}
			impact := "；已摘除的记录保持暂停，不因换 IP 改值提前启用"
			if m.ExpectedEnabled && m.Record.CurrentEnabled {
				impact = "；仍发布地址，等待摘除或最少保留数决定，尚未获准换 IP"
				if strings.Contains(m.Reason, "因最少保留数保留") {
					impact = "；因最少保留数仍发布地址，换 IP 期间该组可能中断，新地址确认后记录改值并保持启用"
				}
			}
			tx, err := p.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			err = p.memberDecisionTx(ctx, tx, b.group, m, "entry_member_rotation", fmt.Sprintf("%s:%s:%s:%d:%t", incident, action, phase, b.generation, m.ExpectedEnabled), message+impact, "")
			if err == nil {
				err = tx.Commit()
			}
			tx.Rollback()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
