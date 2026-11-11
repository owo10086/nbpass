package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

const entryCandidateInterval = 10 * time.Second

type entryStateReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	RuleID  string `json:"rule_id,omitempty"`
}

type entryMemberState struct {
	DesiredRevision  uint64             `json:"desired_revision"`
	AppliedRevision  uint64             `json:"applied_revision"`
	NodeID           string             `json:"node_id"`
	NodeName         string             `json:"node_name"`
	CheckedAt        *time.Time         `json:"checked_at"`
	Ready            bool               `json:"ready"`
	ReadinessStatus  string             `json:"readiness_status"`
	ReadinessReasons []entryStateReason `json:"readiness_reasons"`
	Candidate        bool               `json:"candidate"`
	CandidateStatus  string             `json:"candidate_status"`
	CandidateReasons []entryStateReason `json:"candidate_reasons"`
	DomesticEvidence *domesticEvidence  `json:"domestic_evidence"`
}

// 单节点入口以独立命名空间分桶，既不借用也不污染该节点所属入口组的就绪。
// 同一节点的全部单节点规则共享一个入口，就绪仍逐条检查启用规则。
func ruleEntryKey(r forwarding.Rule) string {
	if r.EntryNodeID != "" {
		return "node:" + r.EntryNodeID
	}
	return r.EntryGroupID
}

// 读取状态不制造候选样本；与入口组页面使用同一新鲜度边界。
func (p *Panel) entryStateView(key, id, name string) entryMemberState {
	state, ok := p.entryChecks[[2]string{key, id}]
	now := p.deps.Clock.Now()
	if !ok {
		return pendingEntryState(id, name, "check_pending", "等待下一次入口检查")
	}
	if state.CheckedAt == nil || now.Before(*state.CheckedAt) || now.Sub(*state.CheckedAt) > entryCandidateInterval {
		checked := state.CheckedAt
		state = pendingEntryState(id, name, "check_stale", "入口检查已过期，等待重新检查")
		state.CheckedAt = checked
	}
	return state
}

func pendingEntryState(id, name, code, message string) entryMemberState {
	reason := []entryStateReason{{Code: code, Message: message}}
	return entryMemberState{NodeID: id, NodeName: name, ReadinessStatus: "unknown", ReadinessReasons: reason, CandidateStatus: "unknown", CandidateReasons: reason}
}

// 页面读取不会制造候选成功样本；后续自动切换/回切复用这份定时采样。
// 调用者持有controlMu，快照一次替换，不能混用不同轮的成员状态。
func (p *Panel) attachEntryStates(groups []entryGroupView, nodes map[string]*syncNode) {
	for i := range groups {
		g := &groups[i]
		g.MemberStates = []entryMemberState{}
		for _, id := range g.Members {
			name := id
			if n := nodes[id]; n != nil {
				name = n.name
			}
			g.MemberStates = append(g.MemberStates, p.entryStateView(g.ID, id, name))
		}
	}
}

func (p *Panel) startEntryCandidates(ctx context.Context) {
	timer := p.deps.Clock.NewTimer(entryCandidateInterval)
	p.background.Add(1)
	go func() {
		defer p.background.Done()
		defer func() { timer.Stop() }()
		var windows map[string]entryFailbackWindow
		var restoreWindows map[[2]string]entryFailbackWindow
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C():
			}
			timer = p.deps.Clock.NewTimer(entryCandidateInterval)
			p.entryMu.Lock()
			p.controlMu.Lock()
			states, err := p.checkEntryCandidates(ctx)
			if err != nil {
				p.entryChecks = nil
				windows = nil
				restoreWindows = nil
			} else {
				p.entryChecks = states
				windows, _ = p.checkEntryFailbacks(ctx, states, windows)
				restoreWindows, _ = p.checkAllRestores(ctx, states, restoreWindows)
			}
			p.controlMu.Unlock()
			p.entryMu.Unlock()
		}
	}()
}

// 调用者持有controlMu，报告、成员变动、快照和本轮证据不会交错。
func (p *Panel) checkEntryCandidates(ctx context.Context) (map[[2]string]entryMemberState, error) {
	nodes, groups, err := p.loadSyncNodes(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := loadRules(ctx, p.db)
	if err != nil {
		return nil, err
	}
	// 空规则组仍保留原有候选语义；单节点入口按实际节点集合追加独立分桶。
	for _, r := range rules {
		if r.EntryNodeID != "" {
			groups[ruleEntryKey(r)] = forwarding.RuleEntryNodes(r, groups)
		}
	}
	cnameGroups := map[string]bool{}
	cnameRows, err := p.db.QueryContext(ctx, "SELECT group_id FROM managed_records WHERE owner_kind='group' AND record_type='CNAME'")
	if err != nil {
		return nil, err
	}
	for cnameRows.Next() {
		var group string
		if err = cnameRows.Scan(&group); err != nil {
			break
		}
		cnameGroups[group] = true
	}
	if err == nil {
		err = cnameRows.Err()
	}
	cnameRows.Close()
	if err != nil {
		return nil, err
	}
	now := p.deps.Clock.Now().UTC()
	states := map[[2]string]entryMemberState{}
	// 一个节点的隧道/国内证据只读取一次，所有入口组共享同一轮物理观测。
	for id, n := range nodes {
		var owned []string
		for group, members := range groups {
			if containsEntryMember(members, id) {
				owned = append(owned, group)
			}
		}
		if len(owned) == 0 {
			continue
		}
		inputs, err := p.loadEntryReadinessInputs(ctx, id, now)
		if err != nil {
			return nil, err
		}
		for _, group := range owned {
			state := entryMemberState{DesiredRevision: n.desired, AppliedRevision: n.reported, NodeID: id, NodeName: n.name, CheckedAt: &now, Ready: true, ReadinessStatus: "ready", ReadinessReasons: []entryStateReason{}, CandidateReasons: []entryStateReason{}, DomesticEvidence: inputs.domestic}
			for _, r := range rules {
				if !r.Enabled || ruleEntryKey(r) != group {
					continue
				}
				result, exists := n.results[r.ID]
				current := n.reported == n.desired && n.reportedDigest == n.digest && n.digest != ""
				switch {
				case current && exists && result.err != "":
					state.ReadinessReasons = append(state.ReadinessReasons, entryStateReason{Code: "rule_apply_failed", RuleID: r.ID, Message: fmt.Sprintf("转发规则“%s”监听/应用失败：%s", r.Name, result.err)})
				case !current || !exists || result.applied != n.desired:
					state.ReadinessReasons = append(state.ReadinessReasons, entryStateReason{Code: "rule_not_applied", RuleID: r.ID, Message: fmt.Sprintf("转发规则“%s”尚未确认应用当前配置与监听", r.Name)})
				}
				if r.Kind == forwarding.Tunnel && !inputs.tunnelReady(r.ID) {
					state.ReadinessReasons = append(state.ReadinessReasons, entryStateReason{Code: "tunnel_handshake_missing", RuleID: r.ID, Message: fmt.Sprintf("隧道规则“%s”没有获授权出口的新鲜成功握手", r.Name)})
				}
			}
			if len(state.ReadinessReasons) > 0 {
				state.Ready = false
				state.ReadinessStatus = "not_ready"
				state.CandidateReasons = append(state.CandidateReasons, entryStateReason{Code: "not_ready", Message: "本入口未就绪，请查看逐规则原因"})
			}
			if !n.connected || !inputs.sessionLive {
				state.CandidateReasons = append(state.CandidateReasons, entryStateReason{Code: "control_offline", Message: "控制离线或连接已中断"})
			}
			if inputs.maintenance {
				state.CandidateReasons = append(state.CandidateReasons, entryStateReason{Code: "maintenance", Message: "节点维护中"})
			}
			if inputs.blocked {
				state.CandidateReasons = append(state.CandidateReasons, entryStateReason{Code: "blocked", Message: "节点已认定被墙"})
			}
			// CNAME 组只能指向已发布、启用且地址就是当前公网地址的节点记录；没有国内证据时只核对前两条。
			if cnameGroups[group] {
				address := ""
				if inputs.domestic != nil {
					address = inputs.domestic.PublicIPv4
				}
				if _, e := cnameTarget(ctx, p.db, id, address, true); e != nil {
					var reason *forwardingError
					if !errors.As(e, &reason) {
						return nil, e
					}
					state.CandidateReasons = append(state.CandidateReasons, entryStateReason{Code: "node_record_unavailable", Message: reason.Error()})
				}
			}
			evidenceStatus := ""
			if inputs.domestic == nil {
				evidenceStatus = "domestic_evidence_missing"
				state.CandidateReasons = append(state.CandidateReasons, entryStateReason{Code: evidenceStatus, Message: "国内可达性未知：没有当前 IP 的正常自测或检测点成功证据"})
			} else if !now.Before(inputs.domestic.ExpiresAt) || now.Before(inputs.domestic.ObservedAt) {
				evidenceStatus = "domestic_evidence_expired"
				state.CandidateReasons = append(state.CandidateReasons, entryStateReason{Code: evidenceStatus, Message: "国内可达性未知：最近成功证据已超过五分钟"})
			}
			state.Candidate = len(state.CandidateReasons) == 0
			state.CandidateStatus = "ineligible"
			if state.Candidate {
				state.CandidateStatus = "candidate"
			} else if evidenceStatus != "" && len(state.CandidateReasons) == 1 {
				state.CandidateStatus = "unknown"
			}
			states[[2]string{group, id}] = state
		}
	}
	return states, nil
}

type entryReadinessInputs struct {
	maintenance, blocked, sessionLive bool
	domestic                          *domesticEvidence
	snapshot                          *controlpb.ConfigSnapshot
	health                            map[string]bool
}

func (p *Panel) loadEntryReadinessInputs(ctx context.Context, id string, now time.Time) (entryReadinessInputs, error) {
	v := entryReadinessInputs{health: map[string]bool{}}
	var maintenance sql.NullInt64
	var instance string
	err := p.db.QueryRowContext(ctx, `SELECT n.maintenance_until,n.process_instance,COALESCE(s.state,'unknown')='blocked' FROM nodes n LEFT JOIN node_self_check s ON s.node_id=n.id WHERE n.id=?`, id).Scan(&maintenance, &instance, &v.blocked)
	if err != nil {
		return v, err
	}
	v.maintenance = maintenance.Valid && now.UnixNano() < maintenance.Int64
	if s := p.controls[id]; s != nil {
		select {
		case <-s.done:
		default:
			v.sessionLive = true
		}
	}
	var lastAbnormal sql.NullInt64
	if v.blocked {
		if err = p.db.QueryRowContext(ctx, "SELECT MAX(finished_at) FROM self_check_rounds WHERE node_id=? AND decision_eligible=1 AND final_result='abnormal'", id).Scan(&lastAbnormal); err != nil {
			return v, err
		}
	}
	var ip, source string
	var at int64
	err = p.db.QueryRowContext(ctx, `SELECT e.public_ipv4,e.observed_at,e.source FROM node_domestic_evidence e JOIN node_regions r ON r.node_id=e.node_id AND r.public_ipv4=e.public_ipv4 WHERE e.node_id=?`, id).Scan(&ip, &at, &source)
	if err == nil {
		observed := time.Unix(0, at).UTC()
		v.domestic = &domesticEvidence{PublicIPv4: ip, ObservedAt: observed, ExpiresAt: observed.Add(domesticEvidenceTTL), Source: source}
		// 新地址正常复检或较新的真实成功能覆盖旧被墙证据；迟到的旧成功不能覆盖后续异常。
		// 这里只计算候选，不借换IP/检测点报告推进原始自测状态。
		if v.blocked && observed.UnixNano() > lastAbnormal.Int64 {
			v.blocked = false
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	var data []byte
	var encrypted bool
	err = p.db.QueryRowContext(ctx, "SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", id).Scan(&data, &encrypted)
	if err == nil {
		v.snapshot = &controlpb.ConfigSnapshot{}
		if err = p.decodeSnapshot(id, data, encrypted, v.snapshot); err != nil {
			return v, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	var sampledInstance string
	var received int64
	err = p.db.QueryRowContext(ctx, "SELECT process_instance,received_at,exit_health_json FROM probe_realtime WHERE node_id=?", id).Scan(&sampledInstance, &received, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if sampledInstance != instance || !v.sessionLive || now.Sub(time.Unix(0, received)) < 0 || now.Sub(time.Unix(0, received)) >= 10*time.Second {
		return v, nil
	}
	var health []*controlpb.ExitHealth
	if err = json.Unmarshal(data, &health); err != nil {
		return v, err
	}
	for _, h := range health {
		// 探测最多5.5秒间隔+3秒握手+5秒采样，加上秒级时间戳取整；未完成和旧成功均不能当新握手。
		age := now.Sub(time.Unix(h.GetCheckedAtUnix(), 0))
		if h.GetLastProbe() == "success" && age >= 0 && age < 15*time.Second {
			v.health[h.ExitId] = true
		}
	}
	return v, nil
}
func (v entryReadinessInputs) tunnelReady(rule string) bool {
	for _, r := range v.snapshot.GetRules() {
		if r.Id != rule {
			continue
		}
		for _, exit := range r.AuthorizedExitIds {
			if v.health[exit] {
				return true
			}
		}
		// 仅兼容既有零代次单出口快照；新版规则必须使用逐规则确认的出口集合。
		if r.AuthorizationGeneration == 0 && len(r.AuthorizedExitIds) == 0 {
			for _, g := range v.snapshot.ExitGroups {
				if g.Id == r.ExitGroupId && len(g.Exits) == 1 && v.health[g.Exits[0].NodeId] {
					return true
				}
			}
		}
	}
	return false
}
