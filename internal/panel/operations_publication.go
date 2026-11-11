package panel

import (
	"context"
	"database/sql"
	"time"
)

func attachOperationPublications(ctx context.Context, tx *sql.Tx, view *operationsView, checks map[[2]string]entryMemberState, nodes map[string]operationNode, now time.Time) error {
	groups := map[string]*operationGroup{}
	for i := range view.Groups {
		groups[view.Groups[i].ID] = &view.Groups[i]
	}
	// 按记录实际写入值找最近确认的入口，CNAME 组的记录值是节点记录域名。
	err := operationRows(ctx, tx, `SELECT g.id,COALESCE(s.active_node_id,''),COALESCE(s.desired_version,0),COALESCE(s.expected_value,''),COALESCE(m.current_value,''),COALESCE(a.stage,'未发布'),COALESCE(w.propagation_status,''),COALESCE(w.id,''),m.group_id,COALESCE(c.node_id,''),c.written_at FROM entry_groups g LEFT JOIN entry_publications s ON s.group_id=g.id LEFT JOIN managed_records m ON m.group_id=g.id LEFT JOIN entry_actions a ON a.id=s.current_action_id LEFT JOIN dns_watches w ON w.entry_group_id=g.id LEFT JOIN entry_actions c ON c.id=(SELECT x.id FROM entry_actions x JOIN entry_action_records r ON r.action_id=x.id AND r.managed_record_id=m.id WHERE x.group_id=g.id AND x.written_at IS NOT NULL AND r.new_value=m.current_value ORDER BY x.version DESC LIMIT 1)`, func(rows *sql.Rows) error {
		var id string
		var g operationGroup
		var bound sql.NullString
		var confirmed sql.NullInt64
		if err := rows.Scan(&id, &g.DesiredNode, &g.DesiredVersion, &g.ExpectedIP, &g.ConfirmedIP, &g.Stage, &g.Propagation, &g.WatchID, &bound, &g.ConfirmedNode, &confirmed); err != nil {
			return err
		}
		target := groups[id]
		target.DesiredNode = g.DesiredNode
		target.DesiredVersion = g.DesiredVersion
		target.ExpectedIP = g.ExpectedIP
		target.ConfirmedIP = g.ConfirmedIP
		target.Stage = g.Stage
		target.Propagation = g.Propagation
		target.WatchID = g.WatchID
		target.Bound = bound.Valid
		target.ConfirmedNode = g.ConfirmedNode
		target.ConfirmedAt = operationTime(confirmed)
		return nil
	})
	if err != nil {
		return err
	}
	for i := range view.Groups {
		g := &view.Groups[i]
		g.Redundancy = "待确认"
		if len(g.Members) == 1 {
			g.Redundancy = "单成员入口组"
			continue
		}
		state, ok := checks[[2]string{g.ID, g.ConfirmedNode}]
		fresh := func(s entryMemberState) bool {
			return s.DesiredRevision == nodes[s.NodeID].Revision && s.CheckedAt != nil && !now.Before(*s.CheckedAt) && now.Sub(*s.CheckedAt) <= entryCandidateInterval
		}
		if !g.Bound || g.ConfirmedNode == "" || !ok || !fresh(state) || !state.Ready || !nodes[g.ConfirmedNode].Connected {
			continue
		}
		standby := false
		for _, id := range g.Members {
			if id == g.ConfirmedNode {
				continue
			}
			s := checks[[2]string{g.ID, id}]
			if fresh(s) && s.Candidate && nodes[id].Connected {
				standby = true
			}
			if !s.Candidate {
				for _, r := range s.CandidateReasons {
					g.Reasons = append(g.Reasons, operationReason{Code: r.Code, Message: nodes[id].Name + "：" + r.Message, Source: "候选检查", NodeID: id, At: s.CheckedAt})
				}
			}
		}
		g.Redundancy = "有就绪备用"
		if !standby {
			g.Redundancy = "冗余下降"
			view.Counts.DegradedGroups++
		}
	}
	return nil
}

func scopeOperationRule(r *operationRule, groups []operationGroup) {
	if !r.Enabled || r.GroupID == "" {
		return
	}
	for _, g := range groups {
		if g.ID != r.GroupID {
			continue
		}
		if !g.Bound || g.ConfirmedNode == "" {
			if r.Status != "abnormal" {
				r.Status = "unknown"
			}
			return
		}
		// 未被发布的备用失败归入冗余，不扩大到对外规则异常。传播旧值仍需单独提示。
		reasons := r.Reasons[:0]
		failed, unknown := false, !containsEntryMember(r.Members, g.ConfirmedNode)
		if unknown {
			reasons = append(reasons, operationReason{Code: "published_member_removed", Message: "最后确认发布的节点已不在当前规则成员中，影响待确认", Source: "入口配置"})
		}
		for _, reason := range r.Reasons {
			if reason.NodeID != "" && reason.NodeID != g.ConfirmedNode {
				continue
			}
			if reason.Code == "publication_unknown" {
				continue
			}
			reasons = append(reasons, reason)
			if reason.Code == "rule_apply_failed" {
				failed = true
			} else {
				unknown = true
			}
		}
		r.Reasons = reasons
		if g.Propagation != "已生效" || g.ExpectedIP != g.ConfirmedIP {
			unknown = true
			r.Reasons = append(r.Reasons, operationReason{Code: "dns_pending", Message: "期望、服务商确认与解析传播尚未全部确认一致；已有连接状态未知", Source: "受管记录/解析监控", At: g.ConfirmedAt})
		}
		r.Status = "no_known_issue"
		if unknown {
			r.Status = "unknown"
		}
		if failed {
			r.Status = "abnormal"
		}
		return
	}
}
