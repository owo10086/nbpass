package forwarding

import (
	"sync"
	"testing"
	"time"
)

// 默认预算必须真正拦住第 513 次待拨号、第 5001 条规则连接和第 20001 条节点连接。
func TestDefaultResourceBudgetsAndIdempotentRelease(t *testing.T) {
	e := NewEngine(Dependencies{})
	l := e.resources
	var leases []*resourceLease
	for i := 0; i < 512; i++ {
		lease := l.acquire("pending")
		if lease == nil {
			t.Fatalf("第 %d 个拨号被提前拒绝", i+1)
		}
		leases = append(leases, lease)
	}
	if l.acquire("other") != nil {
		t.Fatal("待拨号上限未生效")
	}
	for _, lease := range leases {
		lease.dialDone()
		lease.dialDone()
		lease.release()
		lease.release()
	}
	leases = nil
	for _, rule := range []string{"a", "b", "c", "d"} {
		for i := 0; i < 5000; i++ {
			lease := l.acquire(rule)
			if lease == nil {
				t.Fatalf("规则 %s 第 %d 条连接被提前拒绝", rule, i+1)
			}
			lease.dialDone()
			leases = append(leases, lease)
		}
		if l.acquire(rule) != nil {
			t.Fatal("规则上限未生效")
		}
	}
	if l.acquire("fifth") != nil {
		t.Fatal("节点上限未生效")
	}
	for _, lease := range leases {
		lease.release()
	}
	if l.connections != 0 || l.pending != 0 || len(l.rules) != 0 {
		t.Fatal("资源或规则索引未归零")
	}
}

func TestConcurrentResourceAdmissionIsAtomicAcrossRules(t *testing.T) {
	l := NewEngine(Dependencies{Limits: ResourceLimits{MaxConnections: 7, MaxConnectionsPerRule: 3, MaxPendingDials: 20}}).resources
	var wg sync.WaitGroup
	results := make(chan *resourceLease, 100)
	for i := 0; i < 100; i++ {
		wg.Go(func() { results <- l.acquire("same") })
	}
	wg.Wait()
	close(results)
	var leases []*resourceLease
	for lease := range results {
		if lease != nil {
			leases = append(leases, lease)
		}
	}
	if len(leases) != 3 {
		t.Fatalf("并发规则准入: %d", len(leases))
	}
	for _, rule := range []string{"b", "b", "b", "c"} {
		lease := l.acquire(rule)
		if lease == nil {
			t.Fatal("节点预算未用满就拒绝")
		}
		leases = append(leases, lease)
	}
	if l.acquire("d") != nil {
		t.Fatal("跨规则节点上限被突破")
	}
	for _, lease := range leases {
		wg.Go(func() { lease.release(); lease.dialDone(); lease.release() })
	}
	wg.Wait()
	if l.connections != 0 || l.pending != 0 || len(l.rules) != 0 {
		t.Fatal("并发回收泄漏")
	}
}

func TestResourceLimitsRejectNegativeSettings(t *testing.T) {
	for _, limits := range []ResourceLimits{
		{MaxConnections: -1}, {MaxConnectionsPerRule: -1}, {MaxPendingDials: -1}, {DialTimeout: -time.Second}, {IdleTimeout: -time.Second},
	} {
		if limits.Validate() == nil {
			t.Fatalf("接受负数配置: %+v", limits)
		}
	}
	if (ResourceLimits{}).Validate() != nil {
		t.Fatal("零值应选用安全默认值")
	}
}
