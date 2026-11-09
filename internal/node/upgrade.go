package node

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"nbpass/internal/nodeupdate"
)

type upgradeRunner struct {
	available atomic.Bool
	mu        sync.Mutex
	progress  nodeupdate.Progress
	wake      chan struct{}
	jobs      chan nodeupdate.Manifest
	done      chan struct{}
	service   nodeupdate.Service
}

func startUpgrades(ctx context.Context, deps Dependencies) *upgradeRunner {
	if deps.Updater == nil {
		return nil
	}
	r := &upgradeRunner{service: deps.Updater, wake: make(chan struct{}, 1), jobs: make(chan nodeupdate.Manifest, 1), done: make(chan struct{})}
	if p, err := r.service.Status(ctx); err == nil {
		r.available.Store(true)
		r.accept(p)
	}
	go func() {
		defer close(r.done)
		for {
			if p, err := r.service.Status(ctx); err == nil {
				r.available.Store(true)
				r.accept(p)
			} else {
				r.available.Store(false)
			}
			timer := deps.Clock.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case m := <-r.jobs:
				timer.Stop()
				p, err := r.service.Submit(ctx, m)
				if err != nil {
					// The service may already have accepted the task. Keep maintenance and
					// reconcile Status instead of manufacturing a failed terminal result.
					r.available.Store(false)
					continue
				}
				r.accept(p)
			case <-timer.C():
			}
		}
	}()
	return r
}
func (r *upgradeRunner) accept(p nodeupdate.Progress) {
	if p.Manifest.TaskID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.Manifest.TaskID == r.progress.Manifest.TaskID && p.Sequence <= r.progress.Sequence {
		return
	}
	r.progress = p
	select {
	case r.wake <- struct{}{}:
	default:
	}
}
func (r *upgradeRunner) snapshot() []byte {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.progress.Manifest.TaskID == "" {
		return nil
	}
	data, _ := json.Marshal(r.progress)
	return data
}
func (r *upgradeRunner) submit(data []byte) {
	if r == nil {
		return
	}
	var m nodeupdate.Manifest
	if json.Unmarshal(data, &m) != nil || m.Validate() != nil {
		return
	}
	select {
	case r.jobs <- m:
	default:
	}
}
