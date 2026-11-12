package forwarding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nbpass/internal/controlpb"
)

// 新增或变化的隧道监听须先确认出口已应用同一授权目标。
// 各出口并行、同出口复用会话，整份快照共享 3 秒预算；一个黑洞出口
// 不能阻塞其他出口的规则。仅收集探测结果，监听器仍由 Apply 串行修改。
// 未变化的监听和离线 Restore 不探测，保留既有转发语义。
func (e *Engine) checkNewTunnelRules(ctx context.Context, rules []*controlpb.ForwardRule, groups map[string]*controlpb.ExitGroup) map[string]error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pending := map[*tunnelClient][]*controlpb.ForwardRule{}
	for _, r := range rules {
		if e.rules[r.Id] == nil && r.Kind == controlpb.RuleKind_RULE_KIND_TUNNEL {
			for _, client := range e.selectTunnel(r, groups[r.ExitGroupId]).clients {
				pending[client] = append(pending[client], r)
			}
		}
	}
	results := make(chan map[string]error, len(pending))
	for client, rules := range pending {
		go func() {
			failures := map[string]error{}
			for _, r := range rules {
				failures[r.Id] = client.ready(ctx, r)
			}
			results <- failures
		}()
	}
	failures := map[string]error{}
	ready := map[string]bool{}
	for _, r := range rules {
		if e.rules[r.Id] == nil && r.Kind == controlpb.RuleKind_RULE_KIND_TUNNEL {
			failures[r.Id] = fmt.Errorf("规则 %s 出口授权尚未就绪", r.Id)
		}
	}
	for range pending {
		for id, err := range <-results {
			if err == nil {
				ready[id] = true
			} else {
				failures[id] = fmt.Errorf("规则 %s 出口授权尚未就绪: %w", id, err)
			}
		}
	}
	for id := range ready {
		delete(failures, id)
	}
	return failures
}

// Each hop checks at most three authorized candidates under the caller's one
// deadline. This observes grant readiness only and never records a business flow.
func (s *tunnelSelection) checkReady(ctx context.Context, reserved bool) error {
	seen := map[string]bool{}
	last := failTunnel(failureConfig, errors.New("没有获授权的后续路径"))
	for _, c := range s.clients {
		if c == nil {
			continue
		}
		ep := c.currentEndpoint()
		if seen[ep.NodeId] {
			continue
		}
		if len(seen) >= ChainMaxCandidates || ctx.Err() != nil {
			break
		}
		seen[ep.NodeId] = true
		err := c.ready(ctx, s.rule, reserved)
		if err == nil {
			return nil
		}
		class := classifyTunnel(err)
		var failure *tunnelFailure
		if !errors.As(err, &failure) || failure.edge == nil {
			err = &tunnelFailure{class: class, cause: err, edge: chainEdgeFailure(s.rule, ep)}
		}
		last = err
		if class == failureLocal || class == failureConfig {
			break
		}
	}
	return last
}
