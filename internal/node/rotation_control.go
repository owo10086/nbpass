package node

import (
	"context"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

// 调用时持有 r.mu；所有路径都释放锁。管理命令只能处理本机已存的同一动作。
func (r *rotationRunner) submitControlLocked(online context.Context, c *controlpb.RotationCommand) {
	p := r.state.Results[r.state.Current]
	if online.Err() != nil || p == nil || p.ActionId != c.ActionId || p.OldIp != c.OldIp || (c.Operation != "reconcile" && c.Operation != "release") || c.ControlSequence == 0 {
		r.mu.Unlock()
		return
	}
	if c.ControlSequence <= p.ControlSequence {
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default:
		}
		return
	}
	if p.State != "unconfirmed" || p.Attempts != 5 || c.ControlSequence != p.ControlSequence+1 {
		r.mu.Unlock()
		return
	}
	copy := proto.Clone(p).(*controlpb.RotationProgress)
	copy.ControlSequence = c.ControlSequence
	copy.ObservationComplete = true
	r.running = true
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.finished()
		if c.Operation == "release" {
			copy.State, copy.Reason = "released", "manual_release"
		} else {
			ip, unsafe, err := r.query(r.ctx)
			switch {
			case unsafe:
				copy.Reason = "observation_inconsistent"
			case err != nil:
				copy.Reason = "observation_query_failed"
			case ip == copy.OldIp:
				copy.Reason = "reconcile_unchanged"
			default:
				copy.State, copy.Reason, copy.NewIp = "changed", "changed", ip
			}
		}
		// 重试仅写盘；管理命令重投也永远不进入 execute。
		if r.persist(copy, 0) == nil && copy.State == "changed" {
			r.recheck(copy)
		}
	}()
}
