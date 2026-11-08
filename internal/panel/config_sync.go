package panel

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"nbpass/internal/forwarding"
)

type ruleSyncMember struct {
	NodeID          string `json:"node_id"`
	NodeName        string `json:"node_name"`
	Connected       bool   `json:"connected"`
	ListenPort      int    `json:"listen_port"`
	DesiredRevision uint64 `json:"desired_revision"`
	AppliedRevision uint64 `json:"applied_revision"`
	SyncStatus      string `json:"sync_status"`
	ApplyError      string `json:"apply_error"`
}

type ruleView struct {
	forwarding.Rule
	SyncStatus  string           `json:"sync_status"`
	SyncMembers []ruleSyncMember `json:"sync_members"`
}

type groupRuleSync struct {
	RuleID   string `json:"rule_id"`
	RuleName string `json:"rule_name"`
	ruleSyncMember
}

type groupSyncMember struct {
	NodeID     string          `json:"node_id"`
	NodeName   string          `json:"node_name"`
	Connected  bool            `json:"connected"`
	SyncStatus string          `json:"sync_status"`
	Rules      []groupRuleSync `json:"rules"`
}

type syncNode struct {
	name                   string
	connected              bool
	desired, reported      uint64
	digest, reportedDigest string
	results                map[string]ruleResult
	revocationsApplied     bool
	applyError             string
}
type ruleResult struct {
	applied uint64
	err     string
	group   string
	port    int
}

// 调用方持有 controlMu，只在构造内存视图期间持锁，不在 HTTP 写出期间持锁。
func (p *Panel) loadSyncNodes(ctx context.Context) (map[string]*syncNode, map[string][]string, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT n.id,n.name,n.last_seen_at,n.desired_revision,COALESCE(s.digest,''),COALESCE(r.revision,0),COALESCE(r.digest,''),COALESCE(r.revocations_applied,0),n.apply_error
 FROM nodes n LEFT JOIN node_snapshots s ON s.node_id=n.id LEFT JOIN node_rule_reports r ON r.node_id=n.id`)
	if err != nil {
		return nil, nil, err
	}
	nodes := map[string]*syncNode{}
	for rows.Next() {
		var id string
		var last sql.NullInt64
		n := &syncNode{results: map[string]ruleResult{}}
		if err := rows.Scan(&id, &n.name, &last, &n.desired, &n.digest, &n.reported, &n.reportedDigest, &n.revocationsApplied, &n.applyError); err != nil {
			rows.Close()
			return nil, nil, err
		}
		n.connected = p.controls[id] != nil && last.Valid && p.deps.Clock.Now().Sub(time.Unix(0, last.Int64)) < 90*time.Second
		nodes[id] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = p.db.QueryContext(ctx, "SELECT node_id,rule_id,entry_group_id,listen_port,applied_revision,apply_error FROM node_rule_results")
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, rule string
		var result ruleResult
		if err := rows.Scan(&id, &rule, &result.group, &result.port, &result.applied, &result.err); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if n := nodes[id]; n != nil {
			n.results[rule] = result
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = p.db.QueryContext(ctx, "SELECT group_id,node_id FROM entry_group_members ORDER BY position,node_id")
	if err != nil {
		return nil, nil, err
	}
	groups := map[string][]string{}
	for rows.Next() {
		var group, id string
		if err := rows.Scan(&group, &id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		groups[group] = append(groups[group], id)
	}
	err = rows.Err()
	rows.Close()
	return nodes, groups, err
}

func aggregateSync(statuses []string) string {
	result := "正常"
	priority := map[string]int{"正常": 0, "未同步": 1, "待确认": 2, "同步失败": 3}
	for _, status := range statuses {
		if priority[status] > priority[result] {
			result = status
		}
	}
	if len(statuses) == 0 {
		return "未同步"
	}
	return result
}

func buildRuleViews(rules []forwarding.Rule, nodes map[string]*syncNode, groups map[string][]string) []ruleView {
	views := make([]ruleView, 0, len(rules))
	for _, r := range rules {
		view := ruleView{Rule: r, SyncMembers: []ruleSyncMember{}}
		statuses := []string{}
		for _, id := range groups[r.EntryGroupID] {
			n := nodes[id]
			if n == nil {
				continue
			}
			member := ruleSyncMember{NodeID: id, NodeName: n.name, Connected: n.connected, ListenPort: r.ListenPort, DesiredRevision: n.desired, SyncStatus: "未同步"}
			result, exists := n.results[r.ID]
			member.AppliedRevision, member.ApplyError = result.applied, result.err
			current := n.reported == n.desired && n.reportedDigest == n.digest && n.digest != ""
			if current {
				if r.Enabled && exists {
					if result.err != "" {
						member.SyncStatus = "同步失败"
					} else if result.applied == n.desired {
						member.SyncStatus = "正常"
					}
				} else if !r.Enabled && !exists && n.revocationsApplied {
					member.AppliedRevision, member.ApplyError, member.SyncStatus = n.reported, "", "正常"
				} else if !r.Enabled && !n.revocationsApplied && n.applyError != "" {
					member.ApplyError, member.SyncStatus = n.applyError, "同步失败"
				}
			}
			// 从未启用的停用规则不需要改变节点快照。
			if !r.Enabled && n.desired == 0 && !exists {
				member.SyncStatus = "正常"
			}
			if !n.connected {
				member.SyncStatus = "待确认"
			}
			statuses = append(statuses, member.SyncStatus)
			view.SyncMembers = append(view.SyncMembers, member)
		}
		view.SyncStatus = aggregateSync(statuses)
		views = append(views, view)
	}
	return views
}

func attachGroupSync(groups []entryGroupView, rules []ruleView, nodes map[string]*syncNode) {
	for i := range groups {
		g := &groups[i]
		statuses := []string{}
		g.SyncMembers = []groupSyncMember{}
		for _, id := range g.Members {
			n := nodes[id]
			if n == nil {
				continue
			}
			member := groupSyncMember{NodeID: id, NodeName: n.name, Connected: n.connected, SyncStatus: "正常", Rules: []groupRuleSync{}}
			memberStatuses := []string{}
			present := map[string]bool{}
			for _, r := range rules {
				if r.EntryGroupID != g.ID {
					continue
				}
				present[r.ID] = true
				for _, state := range r.SyncMembers {
					if state.NodeID == id {
						member.Rules = append(member.Rules, groupRuleSync{RuleID: r.ID, RuleName: r.Name, ruleSyncMember: state})
						memberStatuses = append(memberStatuses, state.SyncStatus)
					}
				}
			}
			// 规则从管理列表删除或移组后，旧监听在撤销确认前仍属于本组的同步明细。
			for ruleID, result := range n.results {
				if result.group != g.ID || present[ruleID] {
					continue
				}
				state := ruleSyncMember{NodeID: id, NodeName: n.name, Connected: n.connected, ListenPort: result.port, DesiredRevision: n.desired, AppliedRevision: result.applied, SyncStatus: "未同步"}
				if n.reported == n.desired && !n.revocationsApplied && n.applyError != "" {
					state.SyncStatus, state.ApplyError = "同步失败", n.applyError
				}
				if !n.connected {
					state.SyncStatus = "待确认"
				}
				member.Rules = append(member.Rules, groupRuleSync{RuleID: ruleID, RuleName: "已删除或移出的转发规则（等待撤销）", ruleSyncMember: state})
				memberStatuses = append(memberStatuses, state.SyncStatus)
			}
			sort.Slice(member.Rules, func(i, j int) bool { return member.Rules[i].RuleID < member.Rules[j].RuleID })
			if len(memberStatuses) > 0 {
				member.SyncStatus = aggregateSync(memberStatuses)
			}
			if !n.connected {
				member.SyncStatus = "待确认"
			}
			g.SyncMembers = append(g.SyncMembers, member)
			statuses = append(statuses, member.SyncStatus)
		}
		g.SyncStatus = aggregateSync(statuses)
	}
}
