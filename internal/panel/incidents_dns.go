package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func collectDNSIncidents(ctx context.Context, tx *sql.Tx, observations []incidentObservation, view operationsView, checkers map[string]string) ([]incidentObservation, error) {
	watches := []dnsWatchView{}
	actions := map[string]string{}
	err := operationRows(ctx, tx, `SELECT w.id,w.domain,w.expected_ips,w.expected_version,w.round,COALESCE(w.entry_group_id,''),w.propagation_status,COALESCE(s.current_action_id,'') FROM dns_watches w LEFT JOIN entry_publications s ON s.group_id=w.entry_group_id AND s.desired_version=w.expected_version ORDER BY w.id`, func(rows *sql.Rows) error {
		var w dnsWatchView
		var encoded, action string
		if e := rows.Scan(&w.ID, &w.Domain, &encoded, &w.ExpectedVersion, &w.Round, &w.EntryGroupID, &w.PropagationStatus, &action); e != nil {
			return e
		}
		if e := json.Unmarshal([]byte(encoded), &w.ExpectedIPs); e != nil {
			return e
		}
		w.Paths = []dnsWatchPath{}
		watches = append(watches, w)
		actions[w.ID] = action
		return nil
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]*dnsWatchView{}
	for i := range watches {
		byID[watches[i].ID] = &watches[i]
	}
	err = operationRows(ctx, tx, `SELECT watch_id,path_id,node_id,kind,resolver,expected_version,round,ips,status,sampled_at FROM dns_watch_paths WHERE active=1 ORDER BY watch_id,path_id`, func(rows *sql.Rows) error {
		var id, encoded string
		var path dnsWatchPath
		var at sql.NullInt64
		if e := rows.Scan(&id, &path.PathID, &path.NodeID, &path.Kind, &path.Resolver, &path.ExpectedVersion, &path.Round, &encoded, &path.Status, &at); e != nil {
			return e
		}
		if e := json.Unmarshal([]byte(encoded), &path.IPs); e != nil {
			return e
		}
		path.SampledAt = operationTime(at)
		if w := byID[id]; w != nil {
			w.Paths = append(w.Paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 用当前动作 ID 和版本查找原故障；同域名或同节点不构成因果关联。
	actionRoots := map[string][]int{}
	for i, o := range observations {
		for _, e := range o.Entries {
			if e.Stage == "入口动作已提交" {
				actionRoots[e.Source] = append(actionRoots[e.Source], i)
			}
		}
	}
	standalone := []incidentObservation{}
	for _, w := range watches {
		summarizeDNSWatch(&w, checkers, view.GeneratedAt)
		o := incidentObservation{Key: fmt.Sprintf("dns:%s:%d", w.ID, w.ExpectedVersion), Kind: "dns", Title: w.Domain + "：解析证据异常", Impact: "unknown", Handling: "waiting", ExistingOnly: true, Refs: []incidentRef{{Kind: "watch", ID: w.ID, Name: w.Domain, Exists: true}}, Reasons: []string{w.Status + "；" + w.CoverageLabel + "；期望地址 " + watchJSON(w.ExpectedIPs) + "；解析状态不能证明目标应用是否可用"}}
		if w.EntryGroupID != "" {
			o.Refs = append(o.Refs, incidentRef{Kind: "group", ID: w.EntryGroupID, Name: groupName(view, w.EntryGroupID), Exists: true})
			for _, r := range view.Rules {
				if r.Enabled && r.GroupID == w.EntryGroupID {
					o.Refs = append(o.Refs, incidentRef{Kind: "rule", ID: r.ID, Name: r.Name, Exists: true})
				}
			}
		}
		problem := w.Status == "采样失败" || w.Status == "期望不符" || w.Status == "疑似污染" || w.PropagationStatus == "传播未确认"
		o.ExistingOnly = !problem
		if w.Status == "期望不符" || w.Status == "疑似污染" {
			o.Handling = "manual"
		}
		if !problem && (w.Status == "正常" || (len(w.ExpectedIPs) == 0 && w.Status == "观察中")) && (w.EntryGroupID == "" || w.PropagationStatus == "已生效") {
			o.Closure = "异常解除"
			o.Impact = "cleared"
		}
		o.Entries = append(o.Entries, incidentEntry{Key: fmt.Sprintf("watch:%d:%s:%s", w.ExpectedVersion, w.Status, w.PropagationStatus), Stage: w.Status, Message: w.CoverageLabel + "；传播：" + w.PropagationStatus + "；期望 " + watchJSON(w.ExpectedIPs), Source: "解析监控 " + w.ID, Version: w.ExpectedVersion})
		for _, path := range w.Paths {
			o.Entries = append(o.Entries, incidentEntry{Key: fmt.Sprintf("path:%d:%d:%s:%s", w.ExpectedVersion, w.Round, path.PathID, path.Status), Stage: path.Status, Message: fmt.Sprintf("%s %s；地址 %s；采样期望版本 %d；当前版本 %d；旧采样不能覆盖新期望", path.Kind, path.Resolver, watchJSON(path.IPs), path.ExpectedVersion, w.ExpectedVersion), Source: "DNS 路径 " + path.PathID, Version: w.ExpectedVersion, At: path.SampledAt})
		}
		if indices := actionRoots["入口动作 "+actions[w.ID]]; actions[w.ID] != "" && len(indices) > 0 {
			for _, index := range indices {
				root := &observations[index]
				root.Entries = append(root.Entries, o.Entries...)
				root.Refs = mergeIncidentRefs(root.Refs, o.Refs)
				if problem {
					root.Reasons = append(root.Reasons, o.Reasons...)
					root.Closure = ""
					if o.Handling == "manual" {
						root.Handling = "manual"
					}
				}
			}
		} else {
			standalone = append(standalone, o)
		}
	}
	return append(observations, standalone...), nil
}

func mergeIncidentRefs(current, extra []incidentRef) []incidentRef {
	seen := map[string]bool{}
	for _, r := range current {
		seen[r.Kind+":"+r.ID] = true
	}
	for _, r := range extra {
		if !seen[r.Kind+":"+r.ID] {
			current = append(current, r)
			seen[r.Kind+":"+r.ID] = true
		}
	}
	return current
}
