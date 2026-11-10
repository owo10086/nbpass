package panel

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type incidentRotation struct {
	id, node, state, recheck, reason string
	at                               *time.Time
}

func attachRotationIncidentProgress(ctx context.Context, tx *sql.Tx, observations []incidentObservation, view operationsView) ([]incidentObservation, error) {
	byKey := map[string]*incidentObservation{}
	for i := range observations {
		byKey[observations[i].Key] = &observations[i]
	}
	rotations := map[string]incidentRotation{}
	err := operationRows(ctx, tx, `SELECT id,node_id,state,recheck_status,reason,updated_at FROM rotation_actions`, func(rows *sql.Rows) error {
		var r incidentRotation
		var at int64
		if e := rows.Scan(&r.id, &r.node, &r.state, &r.recheck, &r.reason, &at); e != nil {
			return e
		}
		t := time.Unix(0, at).UTC()
		r.at = &t
		rotations[r.id] = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	links := map[string][]string{}
	err = operationRows(ctx, tx, `SELECT f.rotation_id,'blocked:'||d.node_id||':'||d.incident_id FROM entry_blocked_rotations f JOIN entry_blocked_decisions d ON d.id=f.decision_id WHERE f.rotation_id<>'' UNION SELECT n.rotation_id,'blocked:'||d.node_id||':'||d.incident_id FROM entry_blocked_rotation_notices n JOIN entry_blocked_decisions d ON d.id=n.decision_id UNION SELECT substr(t.source,9),i.source_key FROM operation_timeline t JOIN operation_incidents i ON i.sequence=t.incident_id WHERE t.source LIKE '换 IP 动作 %'`, func(rows *sql.Rows) error {
		var id, key string
		if e := rows.Scan(&id, &key); e != nil {
			return e
		}
		links[id] = append(links[id], key)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// 没有持久因果关系的换 IP 动作独立显示，不凭节点名或时间猜测属于哪次被墙。
	standalone := []incidentObservation{}
	for id, r := range rotations {
		if len(links[id]) != 0 {
			continue
		}
		key := "rotation:" + id
		o := incidentObservation{Key: key, Kind: "blocked", Title: "换 IP 处理：" + id, Impact: "unknown", Handling: "processing", Refs: nodeIncidentRefs(view, r.node), Reasons: []string{"没有可验证的原故障关联，此动作单独追踪"}}
		applyIncidentRotation(&o, r)
		standalone = append(standalone, o)
	}
	// standalone 单独收集，避免 append 扩容使 byKey 中的指针失效。
	targets := map[string][]*incidentObservation{}
	for id, keys := range links {
		for _, key := range keys {
			if o := byKey[key]; o != nil {
				applyIncidentRotation(o, rotations[id])
				targets[id] = append(targets[id], o)
			}
		}
	}
	for i := range standalone {
		id := strings.TrimPrefix(standalone[i].Key, "rotation:")
		targets[id] = append(targets[id], &standalone[i])
	}
	// 事件 ID 是既有持久契约，按完整 ID 连接，不解析展示消息。
	eventTargets := map[string][]*incidentObservation{}
	for id, list := range targets {
		for _, phase := range []string{"authorized", "started", "observing", "changed", "recovered", "no_improvement", "pending", "unconfirmed", "cancelled", "released"} {
			eventTargets["rotation-notice:"+id+":"+phase] = list
		}
	}
	err = operationRows(ctx, tx, `SELECT event_id,type,message,occurred_at FROM events WHERE source='panel' AND type LIKE 'ip_rotation_%'`, func(rows *sql.Rows) error {
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
	})
	if err != nil {
		return nil, err
	}
	return append(observations, standalone...), nil
}
func applyIncidentRotation(o *incidentObservation, r incidentRotation) {
	if r.id == "" {
		return
	}
	o.Entries = append(o.Entries, incidentEntry{Key: "rotation:" + r.id + ":" + r.state + ":" + r.recheck, Stage: rotationStage(r.state), Message: r.reason + "；新地址复检：" + rotationRecheckLabel(r.recheck), Source: "换 IP 动作 " + r.id, At: r.at})
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
		o.Reasons = append(o.Reasons, "复检"+rotationRecheckLabel(r.recheck)+"；等待既有额度、暂停及候选条件")
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
}
func rotationStage(state string) string {
	switch state {
	case "authorized":
		return "换 IP 已授权"
	case "started":
		return "换 IP 已开始"
	case "observing":
		return "等待公网 IP 变化"
	case "changed":
		return "公网 IP 已变化"
	case "recovered":
		return "新 IP 复检正常"
	case "unconfirmed":
		return "换 IP 结果未确认"
	case "cancelled":
		return "换 IP 已取消"
	case "released":
		return "换 IP 冻结已解除"
	case "no_improvement":
		return "复检无改善"
	case "pending":
		return "复检结果待确认"
	}
	return state
}
func rotationRecheckLabel(state string) string {
	switch state {
	case "recovered":
		return "通过"
	case "checking":
		return "进行中"
	case "no_improvement":
		return "无改善"
	case "pending":
		return "待确认"
	}
	return "尚未确认"
}
