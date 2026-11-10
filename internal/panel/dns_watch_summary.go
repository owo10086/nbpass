package panel

import "time"

// 页面与故障投影共用同一证据口径；不运行采样，不推进传播状态。
func summarizeDNSWatch(v *dnsWatchView, checkers map[string]string, now time.Time) {
	v.Status = "等待采样"
	v.CoverageLabel = "国内覆盖缺失"
	matched, bad, failed, sampled := false, false, false, false
	for i := range v.Paths {
		path := &v.Paths[i]
		valid := path.NodeID == "" || checkers[path.NodeID] != ""
		if !valid {
			path.Status = "覆盖缺失"
		} else if path.ExpectedVersion != v.ExpectedVersion || path.Round != v.Round || (path.SampledAt != nil && now.Before(*path.SampledAt)) {
			path.Status = "等待采样"
		} else if path.SampledAt != nil && now.Sub(*path.SampledAt) > 5*time.Minute+30*time.Second {
			path.Status = "采样失败"
			path.ConsecutiveMatches = 0
			path.ConsecutiveMismatches = 0
		}
		if !valid {
			continue
		}
		if path.NodeID != "" && v.CoverageLabel == "国内覆盖缺失" {
			v.CoverageLabel = "国内等待采样"
		}
		matched = matched || path.Status == "符合期望"
		bad = bad || path.Status == "期望不符"
		failed = failed || path.Status == "采样失败"
		usable := path.SampledAt != nil && path.Status != "等待采样" && path.Status != "采样失败"
		sampled = sampled || usable
		if path.NodeID != "" && usable {
			v.CoverageLabel = "国内已有采样"
		}
	}
	if sampled {
		v.Status = "观察中"
	}
	if failed {
		v.Status = "采样失败"
	}
	if matched && !failed {
		v.Status = "正常"
	}
	if bad {
		v.Status = "期望不符"
	}
	if bad && matched {
		v.Status = "疑似污染"
	}
}
