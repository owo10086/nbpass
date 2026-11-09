package node

import (
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"sort"
	"time"
)

// recoveryReport 读取持久动作日志，不推进动作、配置或其他上报的确认。
// 当前动作独立于滚动额度历史，超过24小时也必须能够核对或解除。
func (r *rotationRunner) recoveryReport(highWater uint64, now time.Time) *controlpb.RecoveryReport {
	report := &controlpb.RecoveryReport{HighWater: highWater, ObservedAt: now.UnixNano()}
	if r == nil {
		return report
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	report.HistorySince = r.state.HistorySince
	cutoff := now.Add(-24 * time.Hour).UnixNano()
	for id, at := range r.state.AttemptedAt {
		p := r.state.Results[id]
		if p == nil {
			continue
		}
		record := &controlpb.RecoveryRotation{ActionId: id, AttemptedAt: at, State: p.State, Progress: proto.Clone(p).(*controlpb.RotationProgress)}
		if id == r.state.Current {
			report.Current = record
		} else if at > cutoff {
			report.Actions = append(report.Actions, record)
		}
	}
	if report.Current == nil {
		if current := r.state.Results[r.state.Current]; current != nil {
			report.Current = &controlpb.RecoveryRotation{ActionId: current.ActionId, State: current.State, Progress: proto.Clone(current).(*controlpb.RotationProgress)}
		}
	}
	sort.Slice(report.Actions, func(i, j int) bool { return report.Actions[i].AttemptedAt < report.Actions[j].AttemptedAt })
	// 缺失前缀明确缩小覆盖范围；当前动作始终保留以恢复互斥状态。
	for len(report.Actions) > 128 || proto.Size(report) > 48<<10 && len(report.Actions) > 0 {
		report.HistorySince = max(report.HistorySince, report.Actions[0].AttemptedAt+1)
		report.Actions = report.Actions[1:]
	}
	if report.HistorySince > report.ObservedAt {
		report.HistorySince = 0
	}
	return report
}

// 心跳可能同时补报事件；根据整个消息的剩余空间缩小动作历史覆盖范围。
func limitRecoveryReport(report *controlpb.NodeReport) {
	if report.Recovery == nil {
		return
	}
	for proto.Size(report) > 60<<10 && len(report.Recovery.Actions) > 0 {
		first := report.Recovery.Actions[0]
		report.Recovery.HistorySince = max(report.Recovery.HistorySince, first.AttemptedAt+1)
		report.Recovery.Actions = report.Recovery.Actions[1:]
	}
	if report.Recovery.HistorySince > report.Recovery.ObservedAt {
		report.Recovery.HistorySince = 0
	}
}
