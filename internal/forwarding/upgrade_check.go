package forwarding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nbpass/internal/controlpb"
)

// UpgradeReady runs a fresh authorized readiness exchange for every restored tunnel
// rule. The engine owner calls it serially; cached success never proves this process ready.
// This check does not dial rule targets or change the offline forwarding cache.
func (e *Engine) UpgradeReady(ctx context.Context) error {
	if err := e.ApplyError(); err != "" {
		return errors.New(err)
	}
	if !e.checkConfigured {
		return errors.New("本地检测监听尚未就绪")
	}
	for _, r := range e.results {
		if r.ApplyError != "" || r.AppliedRevision != e.highest {
			return errors.New("规则尚未应用当前配置")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pending := map[*tunnelClient][]*controlpb.ForwardRule{}
	ready := map[string]bool{}
	for _, listener := range e.rules {
		if listener.config.Kind != controlpb.RuleKind_RULE_KIND_TUNNEL {
			continue
		}
		selection := listener.selection.Load()
		ready[selection.rule.Id] = false
		for _, client := range selection.clients {
			pending[client] = append(pending[client], selection.rule)
		}
	}
	results := make(chan []string, len(pending))
	for client, rules := range pending {
		go func() {
			successes := []string{}
			for _, rule := range rules {
				if client.ready(ctx, rule) == nil {
					successes = append(successes, rule.Id)
				}
			}
			results <- successes
		}()
	}
	for range pending {
		for _, id := range <-results {
			ready[id] = true
		}
	}
	for id, ok := range ready {
		if !ok {
			return fmt.Errorf("规则 %s 的本进程出口授权握手未就绪", id)
		}
	}
	return nil
}
