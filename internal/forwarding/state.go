package forwarding

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// State 只缓存实际生效的规则；高水位独立于最后完整成功版本。
// Persist 必须先原子保存撤销检查点，再保存实际应用结果。
type State struct {
	HighestRevision uint64                       `json:"highest_revision"`
	HighestDigest   string                       `json:"highest_digest"`
	AppliedRevision uint64                       `json:"applied_revision"`
	AppliedDigest   string                       `json:"applied_digest"`
	Rules           []*controlpb.ForwardRule     `json:"rules"`
	Results         []*controlpb.RuleApplyResult `json:"results"`
}

func ErrorSummary(message string, limit int) string {
	message = strings.ToValidUTF8(message, "�")
	if len(message) > limit {
		message = message[:limit]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return message
}

func (e *Engine) State() State {
	s := State{HighestRevision: e.highest, HighestDigest: e.highestDigest, AppliedRevision: e.revision, AppliedDigest: e.digest}
	for _, r := range e.rules {
		s.Rules = append(s.Rules, proto.Clone(r.config).(*controlpb.ForwardRule))
	}
	sort.Slice(s.Rules, func(i, j int) bool { return s.Rules[i].Id < s.Rules[j].Id })
	for _, r := range e.results {
		s.Results = append(s.Results, proto.Clone(r).(*controlpb.RuleApplyResult))
	}
	return s
}

func (e *Engine) persist(s State) error {
	if e.deps.Persist == nil {
		return nil
	}
	if err := e.deps.Persist(s); err != nil {
		return fmt.Errorf("配置缓存持久化失败: %w", err)
	}
	return nil
}

// Restore 在连接面板前重新 bind 实际规则；失败的规则只保留错误，不重试旧期望。
func (e *Engine) Restore(s State) error {
	if s.HighestRevision == 0 || len(s.HighestDigest) != 64 || s.AppliedRevision > s.HighestRevision || s.AppliedRevision > 0 && len(s.AppliedDigest) != 64 {
		return errors.New("配置缓存版本无效")
	}
	snapshot := &controlpb.ConfigSnapshot{Revision: s.HighestRevision, Rules: s.Rules}
	snapshot.Digest = SnapshotDigest(snapshot)
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	e.highest, e.highestDigest = s.HighestRevision, s.HighestDigest
	e.revision, e.digest = s.AppliedRevision, s.AppliedDigest
	e.results = s.Results
	results := map[string]*controlpb.RuleApplyResult{}
	for _, r := range e.results {
		if r == nil || r.RuleId == "" || results[r.RuleId] != nil || r.AppliedRevision > e.highest {
			return errors.New("逐规则缓存结果无效")
		}
		results[r.RuleId] = r
	}
	for _, r := range s.Rules {
		result := results[r.Id]
		if result == nil || result.AppliedRevision == 0 {
			return errors.New("实际规则缺少缓存结果")
		}
		if err := e.startRule(r); err != nil {
			result.AppliedRevision, result.ApplyError = 0, ErrorSummary(err.Error(), 1024)
			e.revision, e.digest = 0, ""
		}
	}
	return e.persist(e.State())
}

func (e *Engine) ApplyError() string {
	var failures []error
	for _, r := range e.results {
		if r.ApplyError != "" {
			failures = append(failures, errors.New(r.ApplyError))
		}
	}
	if err := errors.Join(failures...); err != nil {
		return ErrorSummary(err.Error(), 1024)
	}
	return ""
}
