package node

import (
	"context"
	"sync"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/dnswatch"
)

// 每条控制流最多四个并行采样 worker，断流即取消，结果只回到原流。
func startDNSWatchRunner(parent context.Context, deps Dependencies) (chan<- *controlpb.DNSWatchTask, <-chan *controlpb.DNSWatchResult, func()) {
	ctx, cancel := context.WithCancel(parent)
	jobs := make(chan *controlpb.DNSWatchTask, 16)
	results := make(chan *controlpb.DNSWatchResult, 16)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case task := <-jobs:
					remaining := time.Unix(0, task.ExpiresAt).Sub(deps.Clock.Now())
					if remaining <= 0 {
						continue
					}
					if remaining > 10*time.Second {
						remaining = 10 * time.Second
					}
					query, stop := timeout(ctx, deps.Clock, remaining)
					ips, err := deps.DNSWatchLookup(query, task.Domain, dnswatch.Resolver{Kind: "dns", Address: task.Resolver})
					ips, normalizeErr := dnswatch.Addresses(ips)
					result := &controlpb.DNSWatchResult{TaskId: task.TaskId, Ips: ips, Failed: err != nil || normalizeErr != nil || query.Err() != nil || len(ips) == 0}
					stop()
					select {
					case results <- result:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	return jobs, results, func() { cancel(); wg.Wait() }
}
