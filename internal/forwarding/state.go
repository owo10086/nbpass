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
	CheckConfigured bool                         `json:"check_configured,omitempty"`
	CheckPort       uint32                       `json:"check_port,omitempty"`
	CheckError      string                       `json:"check_error,omitempty"`
	HighestRevision uint64                       `json:"highest_revision"`
	HighestDigest   string                       `json:"highest_digest"`
	AppliedRevision uint64                       `json:"applied_revision"`
	AppliedDigest   string                       `json:"applied_digest"`
	Rules           []*controlpb.ForwardRule     `json:"rules"`
	Results         []*controlpb.RuleApplyResult `json:"results"`
	ExitGroups      []*controlpb.ExitGroup       `json:"exit_groups,omitempty"`
	TunnelServer    *controlpb.TunnelServer      `json:"tunnel_server,omitempty"`
	TunnelError     string                       `json:"tunnel_error,omitempty"`
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
	s.TunnelError = e.tunnelError
	s.CheckPort, s.CheckError = e.checkPort, e.checkError
	s.CheckConfigured = e.checkConfigured
	if e.tunnelServer != nil {
		s.TunnelServer = proto.Clone(e.tunnelServer.config).(*controlpb.TunnelServer)
	}
	groups := map[string]*controlpb.ExitGroup{}
	for _, r := range e.rules {
		s.Rules = append(s.Rules, proto.Clone(r.config).(*controlpb.ForwardRule))
		if r.exit != nil {
			groups[r.config.ExitGroupId] = &controlpb.ExitGroup{Id: r.config.ExitGroupId, Strategy: controlpb.ExitStrategy_EXIT_STRATEGY_PRIMARY_BACKUP, Exits: []*controlpb.TunnelEndpoint{proto.Clone(r.exit).(*controlpb.TunnelEndpoint)}}
		}
	}
	for _, g := range groups {
		s.ExitGroups = append(s.ExitGroups, g)
	}
	sort.Slice(s.ExitGroups, func(i, j int) bool { return s.ExitGroups[i].Id < s.ExitGroups[j].Id })
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
	snapshot := &controlpb.ConfigSnapshot{Revision: s.HighestRevision, Rules: s.Rules, ExitGroups: s.ExitGroups, TunnelServer: s.TunnelServer, CheckPort: s.CheckPort}
	snapshot.Digest = SnapshotDigest(snapshot)
	if err := ValidateSnapshot(snapshot); err != nil {
		return err
	}
	results := map[string]*controlpb.RuleApplyResult{}
	for _, r := range s.Results {
		if r == nil || r.RuleId == "" || results[r.RuleId] != nil || r.AppliedRevision > s.HighestRevision {
			return errors.New("逐规则缓存结果无效")
		}
		results[r.RuleId] = r
	}
	for _, r := range s.Rules {
		result := results[r.Id]
		if result == nil || result.AppliedRevision == 0 {
			return errors.New("实际规则缺少缓存结果")
		}
	}
	e.highest, e.highestDigest = s.HighestRevision, s.HighestDigest
	e.revision, e.digest = s.AppliedRevision, s.AppliedDigest
	e.results, e.tunnelError = s.Results, s.TunnelError
	port := s.CheckPort
	if !s.CheckConfigured {
		port = e.effectiveCheckPort(port)
		// 旧版自定义检测端口尚未进入缓存；默认端口可能已有合法业务监听。
		for _, rule := range s.Rules {
			if rule.ListenPort == port {
				port = 0
				break
			}
		}
		if s.TunnelServer != nil && s.TunnelServer.ListenPort == port {
			port = 0
		}
	}
	if err := e.ListenCheckPort(port); err != nil {
		e.revision, e.digest = 0, ""
	}
	if s.CheckPort == 0 && s.CheckConfigured && s.CheckError != "" {
		e.checkError = s.CheckError
	}
	if err := e.applyTunnelServer(s.TunnelServer); err != nil {
		e.tunnelError = ErrorSummary(fmt.Sprintf("出口隧道监听失败: %v", err), 1024)
		e.revision, e.digest = 0, ""
	}
	endpoints, groups := tunnelEndpoints(s.ExitGroups)
	e.applyTunnelClients(endpoints)
	for _, r := range s.Rules {
		result := results[r.Id]
		if err := e.startRule(r, groups[r.ExitGroupId]); err != nil {
			result.AppliedRevision, result.ApplyError = 0, ErrorSummary(err.Error(), 1024)
			e.revision, e.digest = 0, ""
		}
	}
	return e.persist(e.State())
}

func (e *Engine) ApplyError() string {
	var failures []error
	if e.checkError != "" {
		failures = append(failures, errors.New(e.checkError))
	}
	if e.tunnelError != "" {
		failures = append(failures, errors.New(e.tunnelError))
	}
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
