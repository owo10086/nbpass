package panel

import (
	"context"
	"database/sql"
	"fmt"
)

func collectRuleIncidents(view operationsView) []incidentObservation {
	result := []incidentObservation{}
	for _, r := range view.Rules {
		o := incidentObservation{Key: "rule:" + r.ID + ":" + r.Configuration, Kind: "rule", Title: r.Name + "：配置、监听或隧道证据", Impact: r.Status, Handling: "waiting", ExistingOnly: true, Refs: []incidentRef{{Kind: "rule", ID: r.ID, Name: r.Name, Exists: true}}}
		if r.GroupID != "" {
			o.Refs = append(o.Refs, incidentRef{Kind: "group", ID: r.GroupID, Name: groupName(view, r.GroupID), Exists: true})
		}
		for _, id := range r.Members {
			for _, n := range view.Nodes {
				if n.ID == id {
					o.Refs = append(o.Refs, incidentRef{Kind: "node", ID: id, Name: n.Name, Exists: true})
				}
			}
		}
		for _, reason := range r.Reasons {
			if reason.Code != "rule_apply_failed" && reason.Code != "rule_not_applied" && reason.Code != "tunnel_handshake_missing" {
				continue
			}
			o.ExistingOnly = false
			message := reason.Message
			// 仅保留现有节点规则摘要，限制单条历史大小；不保存第三方响应。
			if len([]rune(message)) > 500 {
				message = string([]rune(message)[:500]) + "…"
			}
			o.Reasons = append(o.Reasons, message)
			o.Entries = append(o.Entries, incidentEntry{Key: reason.NodeID + ":" + reason.Code, Stage: "规则证据异常", Message: message, Source: reason.Source, Version: int64(r.Revision), At: reason.At})
		}
		for _, e := range r.Evidence {
			o.Entries = append(o.Entries, incidentEntry{Key: fmt.Sprintf("config:%s:%d:%d", e.NodeID, e.Desired, e.Applied), Stage: "配置版本证据", Message: fmt.Sprintf("节点 %s，期望 %d，报告 %d；出口组 %s", e.NodeID, e.Desired, e.Applied, r.ExitGroupID), Source: "入口检查", Version: int64(e.Desired), At: e.At})
		}
		if !r.Enabled {
			o.Closure = "配置变更结束"
			o.Impact = "unknown"
		} else if r.Status == "no_known_issue" {
			o.Closure = "异常解除"
			o.Impact = "cleared"
		}
		if o.Closure != "" {
			o.Entries = append(o.Entries, incidentEntry{Key: "end:" + o.Closure, Stage: o.Closure, Message: "当前配置和新鲜证据已更新；不推断目标应用是否正常", Source: "规则配置/入口检查"})
		}
		result = append(result, o)
	}
	return result
}

// 仅对完整读取后确认被删除或被新配置替代的来源收尾；读取失败不等于恢复。
func reconcileIncidentSources(ctx context.Context, tx *sql.Tx, observations []incidentObservation, view operationsView) ([]incidentObservation, error) {
	current := map[string]bool{}
	for _, o := range observations {
		current[o.Key] = true
	}
	err := operationRows(ctx, tx, "SELECT "+incidentColumns+" FROM operation_incidents WHERE ended_at IS NULL", func(rows *sql.Rows) error {
		old, err := scanIncident(rows)
		if err != nil {
			return err
		}
		if current[old.sourceKey] {
			return nil
		}
		closure := "配置变更结束"
		if old.Kind == "rule" || old.Kind == "dns" || old.Kind == "blocked" {
			closure = "新版本替代"
		}
		if old.Kind == "rule" {
			for _, ref := range old.Refs {
				if ref.Kind != "rule" {
					continue
				}
				found := false
				for _, rule := range view.Rules {
					if rule.ID == ref.ID {
						found = true
						if !rule.Enabled {
							closure = "配置变更结束"
						}
					}
				}
				if !found {
					closure = "配置变更结束"
				}
			}
		}
		observations = append(observations, incidentObservation{Key: old.sourceKey, Kind: old.Kind, Title: old.Title, Impact: "unknown", Closure: closure, Refs: old.Refs, Reasons: []string{"原来源已删除或被新的配置/事故标识替代；这不代表业务恢复"}, Entries: []incidentEntry{{Key: "source-ended", Stage: closure, Message: "原来源不再属于当前完整配置与事故集合，保留历史证据", Source: "配置/事故标识"}}})
		return nil
	})
	return observations, err
}
