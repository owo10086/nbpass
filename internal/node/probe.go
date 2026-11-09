package node

import (
	"context"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/probe"
)

type probeUpdate struct {
	at     time.Time
	sample *controlpb.ProbeSample
}

// 单槽只保留最新样本：断线期间仍采集，但不会积压并补报历史曲线。
func sampleProbe(ctx context.Context, deps Dependencies, started time.Time, updates chan probeUpdate) {
	collector := deps.Probe
	if collector == nil {
		collector = probe.NewSystem(deps.Clock)
	}
	var sequence uint64
	for {
		now := deps.Clock.Now()
		timer := deps.Clock.NewTimer(probe.IntervalSeconds * time.Second)
		values, err := collector.Collect(ctx)
		if err != nil {
			values = nil
		}
		values = probe.Clean(values)
		values["uptime_seconds"] = max(0, now.Sub(started).Seconds())
		var business, tunnel uint64
		if deps.Connections != nil {
			business, tunnel = deps.Connections.Connections()
		}
		values["business_connections"], values["tunnel_connections"] = float64(business), float64(tunnel)
		sequence++
		sample := &controlpb.ProbeSample{Sequence: sequence, Values: values}
		if deps.exitHealth != nil {
			sample.ExitHealth = deps.exitHealth()
		}
		update := probeUpdate{at: now, sample: sample}
		select {
		case <-updates:
		default:
		}
		updates <- update
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}
	}
}
