package panel

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type incidentDecision struct {
	key, id, group, node, action, outcome, policy, rotation string
	version, expected, at                                   int64
}
type incidentAction struct {
	id, group, node, kind, stage, oldIP, newIP string
	version                                    int64
	created, written                           *time.Time
}

func collectBlockedIncidents(ctx context.Context, tx *sql.Tx, view operationsView) ([]incidentObservation, error) {
	nodes := map[string]operationNode{}
	for _, n := range view.Nodes {
		nodes[n.ID] = n
	}
	type flow struct {
		node, id, ip, recheck string
		at                    int64
	}
	flows := []flow{}
	err := operationRows(ctx, tx, `SELECT d.node_id,d.incident_id,COALESCE(a.new_ip,''),COALESCE(a.recheck_status,''),d.created_at`+strategyThreeFlowFrom, func(rows *sql.Rows) error {
		var f flow
		if e := rows.Scan(&f.node, &f.id, &f.ip, &f.recheck, &f.at); e != nil {
			return e
		}
		flows = append(flows, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	activeFlows := map[string]bool{}
	for _, f := range flows {
		activeFlows["blocked:"+f.node+":"+f.id] = true
	}
	observations := []incidentObservation{}
	err = operationRows(ctx, tx, `SELECT node_id,incident_id,target_ip,occurred_at FROM entry_blocked_incidents`, func(rows *sql.Rows) error {
		var node, id, ip string
		var at int64
		if e := rows.Scan(&node, &id, &ip, &at); e != nil {
			return e
		}
		n, exists := nodes[node]
		root := id
		// 当前被墙地址正是该恢复流程实际换出的地址时，后续失败属于同一原故障。
		for _, f := range flows {
			if f.node == node && f.ip != "" && f.ip == ip && f.recheck != "recovered" {
				root = f.id
				break
			}
		}
		o := incidentObservation{Key: "blocked:" + node + ":" + root, Kind: "blocked", Title: n.Name + "：被墙判定", Impact: "unknown", Handling: "waiting", Refs: nodeIncidentRefs(view, node), Reasons: []string{"被墙是基于自测和国内检测点的推定，不代表已证明封锁原因"}}
		occurred := time.Unix(0, at).UTC()
		o.Entries = []incidentEntry{{Key: "blocked:" + id, Stage: "节点被推定被墙", Message: "地址 " + ip + "；依据既有自测/国内检测点判定", Source: "被墙事故", At: &occurred}}
		if n.Blocked != "blocked" {
			o.Closure = "异常解除"
			o.Impact = "cleared"
			o.Entries = append(o.Entries, incidentEntry{Key: "cleared", Stage: "被墙判定已解除", Message: "采用当前业务状态，未推断目标应用可用", Source: "自测/国内检测点"})
		}
		if !exists {
			o.Closure = "配置变更结束"
			o.Impact = "unknown"
		}
		observations = append(observations, o)
		return nil
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, o := range observations {
		seen[o.Key] = true
	}
	// 保留仍有明确恢复流程的原根，以及供手动版本结束判定读取的活动旧根。
	err = operationRows(ctx, tx, "SELECT "+incidentColumns+" FROM operation_incidents WHERE ended_at IS NULL AND source_key LIKE 'blocked:%'", func(rows *sql.Rows) error {
		old, e := scanIncident(rows)
		if e != nil {
			return e
		}
		if seen[old.sourceKey] {
			return nil
		}
		o := incidentObservation{Key: old.sourceKey, Kind: old.Kind, Title: old.Title, Impact: "unknown", Handling: "waiting", Refs: old.Refs, Reasons: old.Reasons}
		if !activeFlows[o.Key] {
			o.Closure = "新版本替代"
		}
		for _, ref := range old.Refs {
			if ref.Kind != "node" {
				continue
			}
			n, exists := nodes[ref.ID]
			if !exists {
				o.Closure = "配置变更结束"
			} else if n.Blocked != "blocked" {
				o.Closure = "异常解除"
				o.Impact = "cleared"
			}
			break
		}
		observations = append(observations, o)
		seen[o.Key] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, f := range flows {
		key := "blocked:" + f.node + ":" + f.id
		if seen[key] {
			continue
		}
		n := nodes[f.node]
		o := incidentObservation{Key: key, Kind: "blocked", Title: n.Name + "：既有被墙恢复流程", Impact: "unknown", Handling: "waiting", Refs: nodeIncidentRefs(view, f.node), Reasons: []string{"从既有决策恢复关联，早期故障发生时间未知"}}
		at := time.Unix(0, f.at).UTC()
		o.Entries = []incidentEntry{{Key: "existing-flow", Stage: "观察到既有恢复流程", Message: "采用持久入口决策和动作关系，未补造之前的故障过程", Source: "入口决策", At: &at}}
		observations = append(observations, o)
		seen[key] = true
	}
	return observations, nil
}

const incidentBlockedDecisionScope = `EXISTS(SELECT 1 FROM entry_blocked_incidents i WHERE i.node_id=d.node_id AND i.incident_id=d.incident_id) OR EXISTS(SELECT 1 FROM entry_blocked_rotations f JOIN entry_publications s ON s.group_id=d.group_id AND s.desired_version=f.expected_version JOIN entry_groups g ON g.id=d.group_id WHERE f.decision_id=d.id AND g.blocked_policy='switch_then_rotate') OR EXISTS(SELECT 1 FROM operation_incidents o WHERE o.source_key='blocked:'||d.node_id||':'||d.incident_id AND o.ended_at IS NULL)`

func attachEntryIncidentProgress(ctx context.Context, tx *sql.Tx, observations []incidentObservation, view operationsView) error {
	byKey := map[string]*incidentObservation{}
	for i := range observations {
		byKey[observations[i].Key] = &observations[i]
		if observations[i].Cause != "" {
			byKey[observations[i].Cause] = &observations[i]
		}
	}
	actions := map[string]incidentAction{}
	err := operationRows(ctx, tx, `SELECT id,group_id,node_id,kind,stage,old_value,new_value,version,created_at,written_at FROM entry_actions WHERE id IN (SELECT current_action_id FROM entry_publications UNION SELECT d.action_id FROM entry_blocked_decisions d WHERE (`+incidentBlockedDecisionScope+`) UNION SELECT c.action_id FROM entry_offline_checks c JOIN offline_incidents i ON i.id=c.incident_id WHERE i.id=(SELECT MAX(id) FROM offline_incidents WHERE node_id=i.node_id))`, func(rows *sql.Rows) error {
		var a incidentAction
		var created int64
		var written sql.NullInt64
		if e := rows.Scan(&a.id, &a.group, &a.node, &a.kind, &a.stage, &a.oldIP, &a.newIP, &a.version, &created, &written); e != nil {
			return e
		}
		at := time.Unix(0, created).UTC()
		a.created = &at
		a.written = operationTime(written)
		actions[a.id] = a
		return nil
	})
	if err != nil {
		return err
	}
	decisions := []incidentDecision{}
	err = operationRows(ctx, tx, `SELECT 'blocked:'||d.node_id||':'||d.incident_id,d.id,d.group_id,d.node_id,d.action_id,d.outcome,d.policy,COALESCE(f.rotation_id,''),d.desired_version,COALESCE(f.expected_version,0),d.created_at FROM entry_blocked_decisions d LEFT JOIN entry_blocked_rotations f ON f.decision_id=d.id WHERE (`+incidentBlockedDecisionScope+`) UNION ALL SELECT 'control:'||c.node_id||':'||c.incident_id,c.id,c.group_id,c.node_id,c.action_id,c.outcome,'offline','',c.desired_version,0,c.issued_at FROM entry_offline_checks c JOIN offline_incidents i ON i.id=c.incident_id WHERE i.id=(SELECT MAX(id) FROM offline_incidents WHERE node_id=i.node_id)`, func(rows *sql.Rows) error {
		var d incidentDecision
		if e := rows.Scan(&d.key, &d.id, &d.group, &d.node, &d.action, &d.outcome, &d.policy, &d.rotation, &d.version, &d.expected, &d.at); e != nil {
			return e
		}
		decisions = append(decisions, d)
		return nil
	})
	if err != nil {
		return err
	}
	latestDecision := map[string]incidentDecision{}
	for _, d := range decisions {
		key := d.key + ":" + d.group
		old, ok := latestDecision[key]
		if !ok || d.at > old.at || (d.at == old.at && d.version > old.version) {
			latestDecision[key] = d
		}
	}
	branches := map[string]int{}
	replaced := map[string]int{}
	branchPending := map[string]bool{}
	branchEnded := map[string]string{}
	for _, d := range decisions {
		o := byKey[d.key]
		if o == nil {
			continue
		}
		at := time.Unix(0, d.at).UTC()
		o.Entries = append(o.Entries, incidentEntry{Key: "decision:" + d.id + ":" + d.outcome, Stage: "入口组策略决策", Message: fmt.Sprintf("入口组 %s，策略 %s，决策 %s", groupName(view, d.group), d.policy, d.outcome), Source: "入口决策 " + d.id, Version: d.version, At: &at})
		if d.outcome == "no_candidate" || d.outcome == "no_checkers" || d.outcome == "unknown" {
			o.Reasons = append(o.Reasons, groupName(view, d.group)+"："+decisionReason(d.outcome))
		}
		a, ok := actions[d.action]
		if ok {
			appendIncidentAction(o, a)
		}
		if latestDecision[d.key+":"+d.group].id != d.id {
			continue
		}
		branches[o.Key]++
		found := false
		for _, g := range view.Groups {
			if g.ID != d.group {
				continue
			}
			found = true
			if d.policy != "offline" && (g.BlockedPolicy != d.policy || !containsEntryMember(g.Members, d.node)) {
				replaced[o.Key]++
				branchEnded[o.Key] = "配置变更结束"
				o.Entries = append(o.Entries, incidentEntry{Key: "branch-config:" + d.id, Stage: "配置变更结束", Message: g.Name + "：策略或成员配置已改变，原处理分支结束；不代表业务恢复", Source: "入口配置", Version: g.DesiredVersion})
				continue
			}
			version := max(d.version, d.expected, a.version)
			currentFound := false
			for _, current := range actions {
				if current.group != d.group || current.version != g.DesiredVersion {
					continue
				}
				currentFound = true
				if current.version > version && current.kind == "failback" && current.node == d.node {
					o.Entries = append(o.Entries, incidentEntry{Key: current.id + ":failback", Stage: "回切已提交", Message: "原节点回切已由既有协调器提交，写入与传播仍分别核对", Source: "入口动作 " + current.id, Version: current.version, At: current.created})
					appendIncidentAction(o, current)
					appendIncidentPropagation(o, g, current.id)
					branchPending[o.Key] = branchPending[o.Key] || current.written == nil || g.Propagation != "已生效"
				} else if current.version > version && current.kind != "writeback" && current.kind != "address_update" {
					closure := "新版本替代"
					if current.kind == "manual_switch" {
						closure = "管理员操作结束"
					}
					replaced[o.Key]++
					branchEnded[o.Key] = closure
					o.Entries = append(o.Entries, incidentEntry{Key: current.id + ":supersedes:" + d.id, Stage: closure, Message: groupName(view, d.group) + "：新的期望替代了原处理分支，不代表故障恢复", Source: "入口动作 " + current.id, Version: current.version, At: current.created})
				} else {
					appendIncidentPropagation(o, g, current.id)
					branchPending[o.Key] = branchPending[o.Key] || current.written == nil || g.Propagation != "已生效" || (d.policy == "switch_then_rotate" && g.DesiredNode != d.node)
				}
			}
			if !currentFound {
				branchPending[o.Key] = true
			}
		}
		if !found {
			replaced[o.Key]++
			branchEnded[o.Key] = "配置变更结束"
		}
	}
	for i := range observations {
		o := &observations[i]
		o.Pending = branchPending[o.Key]
		if o.Pending {
			o.Closure = ""
			o.Handling = "waiting"
		} else if end := branchEnded[o.Key]; end != "" && replaced[o.Key] == branches[o.Key] {
			o.Closure = end
		}
	}
	return nil
}
func decisionReason(outcome string) string {
	switch outcome {
	case "no_candidate":
		return "没有就绪候选；按现有策略处理（先切后换可能退化为申请换 IP）"
	case "no_checkers":
		return "没有有效国内检测点，业务影响待确认"
	default:
		return "检测证据尚不足以确认"
	}
}
func groupName(view operationsView, id string) string {
	for _, g := range view.Groups {
		if g.ID == id {
			return g.Name
		}
	}
	return id
}
func appendIncidentAction(o *incidentObservation, a incidentAction) {
	o.Entries = append(o.Entries, incidentEntry{Key: a.id + ":desired", Stage: "入口动作已提交", Message: fmt.Sprintf("入口组 %s：%s → %s（%s）", a.group, a.oldIP, a.newIP, a.kind), Source: "入口动作 " + a.id, Version: a.version, At: a.created})
	if a.written != nil {
		o.Entries = append(o.Entries, incidentEntry{Key: a.id + ":written", Stage: "服务商已写入", Message: a.newIP + "；写入确认不代表解析传播完成", Source: "入口动作 " + a.id, Version: a.version, At: a.written})
	} else if a.stage == EntryUnconfirmed {
		o.Entries = append(o.Entries, incidentEntry{Key: a.id + ":unconfirmed", Stage: "写入结果未确认", Message: "等待协调器读回核对，不重复执行外部动作", Source: "入口动作 " + a.id, Version: a.version})
	}
}
func appendIncidentPropagation(o *incidentObservation, g operationGroup, action string) {
	if g.Propagation == "" {
		return
	}
	o.Entries = append(o.Entries, incidentEntry{Key: action + ":propagation:" + g.Propagation, Stage: g.Propagation, Message: groupNameFromGroup(g) + "：采用现有解析传播判定", Source: "解析监控 " + g.WatchID, Version: g.DesiredVersion})
	if g.WatchID != "" {
		o.Refs = appendUniqueIncidentRef(o.Refs, incidentRef{Kind: "watch", ID: g.WatchID, Name: g.Name + " 域名", Exists: true})
	}
}
func groupNameFromGroup(g operationGroup) string {
	if g.Name != "" {
		return g.Name
	}
	return g.ID
}
func appendUniqueIncidentRef(refs []incidentRef, r incidentRef) []incidentRef {
	for _, v := range refs {
		if v.Kind == r.Kind && v.ID == r.ID {
			return refs
		}
	}
	return append(refs, r)
}
