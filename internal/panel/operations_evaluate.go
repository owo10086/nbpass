package panel

import "time"

func evaluateOperationRule(r *operationRule, checks map[[2]string]entryMemberState, nodes map[string]operationNode, now time.Time) {
	if !r.Enabled {
		r.Status = "disabled"
		return
	}
	key := r.GroupID
	if r.NodeID != "" {
		key = "node:" + r.NodeID
	}
	unknown, failed := len(r.Members) == 0, false
	for _, id := range r.Members {
		n := nodes[id]
		s, ok := checks[[2]string{key, id}]
		add := func(code, message, source string, at *time.Time) {
			r.Reasons = append(r.Reasons, operationReason{Code: code, Message: message, Source: source, NodeID: id, At: at})
		}
		if !ok || s.CheckedAt == nil || now.Before(*s.CheckedAt) || now.Sub(*s.CheckedAt) > entryCandidateInterval {
			unknown = true
			add("check_stale", "入口证据待采集或已过期", "入口检查", s.CheckedAt)
			continue
		}
		for _, reason := range s.ReadinessReasons {
			if reason.RuleID != "" && reason.RuleID != r.ID {
				continue
			}
			if reason.Code == "rule_apply_failed" {
				failed = true
			} else {
				unknown = true
			}
			add(reason.Code, reason.Message, "入口检查", s.CheckedAt)
		}
		if !n.Connected {
			unknown = true
			add("control_offline", "控制连接中断，节点可能仍按缓存转发", "控制连接", n.LastSeen)
		}
		if n.Status == "maintenance" {
			unknown = true
			add("maintenance", "节点维护中", "节点状态", s.CheckedAt)
		}
		if n.Blocked == "blocked" {
			unknown = true
			add("blocked", "节点被推定被墙；实际规则影响需结合入口与解析证据", "自测/国内检测点", s.CheckedAt)
		}
	}
	r.Status = "no_known_issue"
	if unknown {
		r.Status = "unknown"
	}
	if failed {
		r.Status = "abnormal"
	}
	if r.GroupID != "" {
		r.Reasons = append(r.Reasons, operationReason{Code: "publication_unknown", Message: "入口组对外使用情况待结合受管记录确认", Source: "入口配置"})
	}
}
