package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type incidentRotation struct {
	row                              int64
	linked                           bool
	id, node, state, recheck, reason string
	at                               *time.Time
	created                          int64
	paused                           bool
}

// 投影自己的关联表只在展示事务内写入，不依赖中文文案，也不参与业务授权。
func attachRotationIncidentProgress(ctx context.Context, tx *sql.Tx, observations []incidentObservation, view operationsView) ([]incidentObservation, error) {
	byKey := map[string]*incidentObservation{}
	scopeKeys := []string{}
	for i := range observations {
		byKey[observations[i].Key] = &observations[i]
		scopeKeys = append(scopeKeys, observations[i].Key)
	}
	scope, scopeArgs := operationKeys(scopeKeys)
	links := map[string][]string{}
	err := operationRows(ctx, tx, `SELECT rotation_id,source_key FROM (
 SELECT f.rotation_id AS rotation_id,'blocked:'||d.node_id||':'||d.incident_id AS source_key FROM entry_blocked_rotations f JOIN entry_blocked_decisions d ON d.id=f.decision_id WHERE f.rotation_id<>''
 UNION SELECT n.rotation_id,'blocked:'||d.node_id||':'||d.incident_id FROM entry_blocked_rotation_notices n JOIN entry_blocked_decisions d ON d.id=n.decision_id WHERE n.rotation_id<>''
 UNION SELECT rotation_id,source_key FROM operation_rotation_links) WHERE source_key IN (`+scope+`)`, func(rows *sql.Rows) error {
		var id, key string
		if e := rows.Scan(&id, &key); e != nil {
			return e
		}
		links[id] = append(links[id], key)
		return nil
	}, scopeArgs...)
	if err != nil {
		return nil, err
	}
	for id, keys := range links {
		for _, key := range keys {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO operation_rotation_links(rotation_id,source_key) VALUES(?,?)", id, key); err != nil {
				return nil, err
			}
		}
	}
	rotations := map[string]incidentRotation{}
	err = operationRows(ctx, tx, `SELECT a.id,a.node_id,a.state,a.recheck_status,a.reason,a.updated_at,a.created_at,a.rowid,COALESCE(p.paused,0) AND a.id=(SELECT id FROM rotation_actions WHERE node_id=a.node_id ORDER BY created_at DESC,rowid DESC LIMIT 1),EXISTS(SELECT 1 FROM operation_rotation_links l WHERE l.rotation_id=a.id) OR EXISTS(SELECT 1 FROM entry_blocked_rotations f WHERE f.rotation_id=a.id) OR EXISTS(SELECT 1 FROM entry_blocked_rotation_notices n WHERE n.rotation_id=a.id)
 FROM rotation_actions a LEFT JOIN rotation_policy p ON p.node_id=a.node_id
 WHERE a.state NOT IN ('changed','cancelled','released')
 OR a.id IN (SELECT rotation_id FROM operation_rotation_links WHERE source_key IN (`+scope+`))
 OR (NOT EXISTS(SELECT 1 FROM operation_rotation_links l WHERE l.rotation_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM operation_processed m WHERE m.source_key='rotation:'||a.id AND m.ended=1))`, func(rows *sql.Rows) error {
		var r incidentRotation
		var at int64
		if e := rows.Scan(&r.id, &r.node, &r.state, &r.recheck, &r.reason, &at, &r.created, &r.row, &r.paused, &r.linked); e != nil {
			return e
		}
		t := time.Unix(0, at).UTC()
		r.at = &t
		rotations[r.id] = r
		return nil
	}, scopeArgs...)
	if err != nil {
		return nil, err
	}
	ordered := make([]string, 0, len(rotations))
	for id := range rotations {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := rotations[ordered[i]], rotations[ordered[j]]
		if a.created == b.created {
			return a.row < b.row
		}
		return a.created < b.created
	})
	standalone := []incidentObservation{}
	for _, id := range ordered {
		r := rotations[id]
		if r.linked || len(links[id]) != 0 {
			continue
		}
		o := incidentObservation{Key: "rotation:" + id, Kind: "blocked", Title: "换 IP 处理：" + id, Impact: "unknown", Handling: "processing", Refs: nodeIncidentRefs(view, r.node), Reasons: []string{"没有可验证的原故障关联，此动作单独追踪"}}
		applyIncidentRotation(&o, r)
		standalone = append(standalone, o)
	}
	targets := map[string][]*incidentObservation{}
	latest := map[string]string{}
	for _, id := range ordered {
		for _, key := range links[id] {
			latest[key] = id
		}
	}
	currentLinks := map[string]map[string]bool{}
	if err = operationRows(ctx, tx, `SELECT f.rotation_id,'blocked:'||d.node_id||':'||d.incident_id FROM entry_blocked_rotations f JOIN entry_blocked_decisions d ON d.id=f.decision_id WHERE f.rotation_id<>'' AND 'blocked:'||d.node_id||':'||d.incident_id IN (`+scope+`)`, func(rows *sql.Rows) error {
		var id, key string
		if e := rows.Scan(&id, &key); e != nil {
			return e
		}
		if currentLinks[key] == nil {
			currentLinks[key] = map[string]bool{}
		}
		currentLinks[key][id] = true
		return nil
	}, scopeArgs...); err != nil {
		return nil, err
	}
	for _, id := range ordered {
		for _, key := range links[id] {
			if currentLinks[key][id] {
				latest[key] = id
			}
		}
	}
	for _, id := range ordered {
		for _, key := range links[id] {
			if o := byKey[key]; o != nil {
				if latest[key] == id {
					applyIncidentRotation(o, rotations[id])
				} else {
					appendIncidentRotationFact(o, rotations[id])
				}
				targets[id] = append(targets[id], o)
			}
		}
	}
	// append 完成后再取指针，避免切片扩容导致指针指向旧数组。
	for i := range standalone {
		id := strings.TrimPrefix(standalone[i].Key, "rotation:")
		targets[id] = append(targets[id], &standalone[i])
	}
	eventTargets := map[string][]*incidentObservation{}
	for id, list := range targets {
		for _, phase := range []string{"authorized", "started", "observing", "changed", "recovered", "no_improvement", "pending", "unconfirmed", "cancelled", "released", "paused"} {
			eventTargets["rotation-notice:"+id+":"+phase] = list
		}
	}
	// 既有策略通知以决策 ID、动作 ID、phase 形成确定事件键，按完整键关联。
	err = operationRows(ctx, tx, `SELECT n.decision_id,n.rotation_id,n.phase,'blocked:'||d.node_id||':'||d.incident_id FROM entry_blocked_rotation_notices n JOIN entry_blocked_decisions d ON d.id=n.decision_id WHERE 'blocked:'||d.node_id||':'||d.incident_id IN (`+scope+`)`, func(rows *sql.Rows) error {
		var decision, rotation, phase, key string
		if e := rows.Scan(&decision, &rotation, &phase, &key); e != nil {
			return e
		}
		if o := byKey[key]; o != nil {
			id := fmt.Sprintf("%x", sha256.Sum256([]byte(decision+":"+rotation+":"+phase)))
			eventTargets["rotation-notice:"+id+":entry_waiting"] = []*incidentObservation{o}
		}
		return nil
	}, scopeArgs...)
	if err != nil {
		return nil, err
	}
	// 只读本轮相关事件；已结束历史的时间线不会在每次整理时被全量扫描。
	eventKeys := make([]string, 0, len(eventTargets))
	for key := range eventTargets {
		eventKeys = append(eventKeys, key)
	}
	sort.Strings(eventKeys)
	for start := 0; start < len(eventKeys); start += 500 {
		keys, args := operationKeys(eventKeys[start:min(start+500, len(eventKeys))])
		err = operationRows(ctx, tx, `SELECT event_id,type,message,occurred_at FROM events WHERE source='panel' AND event_id IN (`+keys+`) ORDER BY occurred_at,event_id`, func(rows *sql.Rows) error {
			var id, kind, message string
			var at int64
			if e := rows.Scan(&id, &kind, &message, &at); e != nil {
				return e
			}
			t := time.Unix(0, at).UTC()
			for _, o := range eventTargets[id] {
				o.Entries = append(o.Entries, incidentEntry{Key: "event:" + id, Stage: rotationStage(strings.TrimPrefix(kind, "ip_rotation_")), Message: message, Source: "换 IP 事件 " + id, At: &t})
			}
			return nil
		}, args...)
		if err != nil {
			return nil, err
		}
	}
	return append(observations, standalone...), nil
}

func operationKeys(keys []string) (string, []any) {
	marks := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range keys {
		marks[i] = "?"
		args[i] = key
	}
	if len(keys) == 0 {
		return "NULL", nil
	}
	return strings.Join(marks, ","), args
}
func applyIncidentRotation(o *incidentObservation, r incidentRotation) {
	appendIncidentRotationFact(o, r)
	if o.Closure == "管理员操作结束" || o.Closure == "新版本替代" || o.Closure == "配置变更结束" {
		return
	}
	o.Closure = ""
	o.Handling = "processing"
	switch {
	case r.state == "unconfirmed":
		o.Handling = "manual"
		o.Reasons = append(o.Reasons, "换 IP 结果未确认，自动换 IP 已冻结，请到节点页面核对")
	case r.recheck == "pending" || r.recheck == "no_improvement":
		o.Handling = "waiting"
		o.Reasons = append(o.Reasons, "复检"+rotationRecheckLabel(r.recheck)+"；等待既有自动处理条件，具体限制见处理时间线")
	case r.recheck == "recovered":
		o.Handling = "waiting"
		o.Impact = "cleared"
		o.Reasons = append(o.Reasons, "新地址复检已通过，入口组回切及传播仍以各自分支为准")
		if !o.Pending {
			o.Closure = "自动恢复完成"
		}
	case r.state == "cancelled" || r.state == "released":
		o.Handling = "manual"
		if strings.HasPrefix(o.Key, "rotation:") {
			o.Closure = "管理员操作结束"
		}
		o.Reasons = append(o.Reasons, "动作已取消或人工解除冻结；这不代表地址或业务恢复")
	}
	if r.paused {
		o.Handling = "manual"
		o.Closure = ""
		o.Reasons = append(o.Reasons, "连续无改善，现有自动换 IP 已暂停；请到节点页面核对并按原流程人工恢复")
	}
}
func appendIncidentRotationFact(o *incidentObservation, r incidentRotation) {
	o.Entries = append(o.Entries, incidentEntry{Key: "rotation:" + r.id + ":" + r.state + ":" + r.recheck, Stage: rotationStage(r.state), Message: r.reason + "；新地址复检：" + rotationRecheckLabel(r.recheck), Source: "换 IP 动作 " + r.id, At: r.at})
}
func rotationStage(state string) string {
	labels := map[string]string{"authorized": "换 IP 已授权", "started": "换 IP 已开始", "observing": "等待公网 IP 变化", "changed": "公网 IP 已变化", "recovered": "新 IP 复检正常", "unconfirmed": "换 IP 结果未确认", "cancelled": "换 IP 已取消", "released": "换 IP 冻结已解除", "no_improvement": "复检无改善", "pending": "复检结果待确认", "paused": "自动换 IP 已暂停", "entry_waiting": "入口策略等待条件"}
	if label := labels[state]; label != "" {
		return label
	}
	return state
}
func rotationRecheckLabel(state string) string {
	labels := map[string]string{"recovered": "通过", "checking": "进行中", "no_improvement": "无改善", "pending": "待确认"}
	if label := labels[state]; label != "" {
		return label
	}
	return "尚未确认"
}
