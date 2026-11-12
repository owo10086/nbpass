package forwarding

import (
	"context"
	"net"
	"time"

	"nbpass/internal/controlpb"
)

func streamAuthorization(entry string, rule *controlpb.ForwardRule) tunnelRequest {
	return tunnelRequest{ChainID: rule.ChainId, HopIndex: nextChainHop(rule), ChainFingerprint: rule.GetChainAuthorization().GetFingerprint(), RuleID: rule.Id, EntryID: entry, Generation: rule.AuthorizationGeneration, TargetAddress: rule.TargetAddress, TargetPort: rule.TargetPort, TargetFamily: rule.TargetFamily}
}

func (s *tunnelServer) authorizeStream(auth tunnelAuth, request tunnelRequest) *controlpb.ForwardRule {
	rule := s.authorized(auth.EntryID, auth.Credential, request.RuleID)
	return matchStreamAuthorization(rule, auth, request)
}

func matchStreamAuthorization(rule *controlpb.ForwardRule, auth tunnelAuth, request tunnelRequest) *controlpb.ForwardRule {
	if rule == nil || rule.AuthorizationGeneration != request.Generation {
		return nil
	}
	if rule.ChainId != "" {
		if rule.AuthorizationGeneration == 0 || rule.HopIndex == 0 || request.ChainID != rule.ChainId || request.HopIndex != rule.HopIndex || rule.GetChainAuthorization().GetFingerprint() == "" || request.ChainFingerprint != rule.GetChainAuthorization().GetFingerprint() || request.RemainingBudgetMS == 0 || request.RemainingBudgetMS > 10000 {
			return nil
		}
	} else if request.ChainID != "" || request.HopIndex != 0 || request.ChainFingerprint != "" {
		return nil
	}
	if rule.AuthorizationGeneration == 0 && request.EntryID == "" && request.TargetAddress == "" && request.TargetPort == 0 && request.TargetFamily == 0 {
		// 兼容尚未升级的既有零代次授权；新建或变化的授权均大于零。
		return rule
	}
	if request.EntryID != auth.EntryID || request.TargetAddress != rule.TargetAddress || request.TargetPort != rule.TargetPort || request.TargetFamily != rule.TargetFamily {
		return nil
	}
	return rule
}

// 使用节点注入时钟；控制链路失联不停止复查，也不为授权设置离线期限。
func (s *tunnelServer) watchStreamAuthorization(ctx context.Context, cancel context.CancelFunc, stream net.Conn, auth tunnelAuth, request tunnelRequest) func() {
	timer := s.engine.deps.Clock.NewTimer(time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { timer.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C():
				if s.authorizeExistingStream(auth, request) == nil {
					cancel()
					stream.Close()
					return
				}
				timer = s.engine.deps.Clock.NewTimer(time.Second)
			}
		}
	}()
	return func() { cancel(); <-done }
}

func nextChainHop(rule *controlpb.ForwardRule) uint32 {
	if rule.GetChainId() == "" {
		return 0
	}
	return rule.HopIndex + 1
}
