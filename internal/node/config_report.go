package node

import (
	"errors"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 汇总与逐规则错误共享消息预算，不能因多条 bind 失败丢掉整份状态报告。
func limitConfigReport(report *controlpb.NodeReport) error {
	failed := []*string{}
	messages := []string{}
	for _, r := range report.RuleResults {
		if r.ApplyError != "" {
			failed = append(failed, &r.ApplyError)
			messages = append(messages, r.ApplyError)
			r.ApplyError = ""
		}
	}
	for _, r := range report.TunnelGrantResults {
		if r.ApplyError != "" {
			failed = append(failed, &r.ApplyError)
			messages = append(messages, r.ApplyError)
			r.ApplyError = ""
		}
	}
	if len(failed) > 0 {
		budget := ((60<<10)-proto.Size(report))/len(failed) - 4
		if budget < 6 {
			return errors.New("逐规则报告超过控制消息上限")
		}
		for i, r := range failed {
			*r = forwarding.ErrorSummary(messages[i], min(budget, 1024))
		}
	}
	if proto.Size(report) > 64<<10 {
		return errors.New("配置报告超过控制消息上限")
	}
	return nil
}

// Always report the attempted snapshot's complete grant identities. Missing
// engine results mean application/persistence failed, never an inferred success.
func grantReportResults(s *controlpb.ConfigSnapshot, actual []*controlpb.TunnelGrantApplyResult) []*controlpb.TunnelGrantApplyResult {
	results := []*controlpb.TunnelGrantApplyResult{}
	for _, g := range s.GetTunnelServer().GetGrants() {
		r := &controlpb.TunnelGrantApplyResult{EntryId: g.EntryId, RuleId: g.Rule.Id, Generation: g.Rule.AuthorizationGeneration, ApplyError: "授权未持久应用"}
		for _, a := range actual {
			if a.EntryId == r.EntryId && a.RuleId == r.RuleId && a.Generation == r.Generation && a.AppliedRevision == s.Revision {
				r = proto.Clone(a).(*controlpb.TunnelGrantApplyResult)
				break
			}
		}
		results = append(results, r)
	}
	return results
}
