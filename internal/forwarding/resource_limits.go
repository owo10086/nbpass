package forwarding

import (
	"errors"
	"sync"
	"time"
)

// ResourceLimits 为直连路径的节点级预算；零值使用默认值，不表示无限制。
type ResourceLimits struct {
	MaxConnections        int
	MaxConnectionsPerRule int
	MaxPendingDials       int
	DialTimeout           time.Duration
	IdleTimeout           time.Duration
}

func (l ResourceLimits) Validate() error {
	if l.MaxConnections < 0 || l.MaxConnectionsPerRule < 0 || l.MaxPendingDials < 0 || l.DialTimeout < 0 || l.IdleTimeout < 0 {
		return errors.New("直连资源上限和超时不能为负数；零值使用默认值")
	}
	return nil
}

func (l ResourceLimits) defaults() ResourceLimits {
	if l.MaxConnections <= 0 {
		l.MaxConnections = 20000
	}
	if l.MaxConnectionsPerRule <= 0 {
		l.MaxConnectionsPerRule = 5000
	}
	if l.MaxPendingDials <= 0 {
		l.MaxPendingDials = 512
	}
	if l.DialTimeout <= 0 {
		l.DialTimeout = 10 * time.Second
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = 2 * time.Hour
	}
	return l
}

// resourceLimiter 与具体传输、探针计量分离，票 11 可复用同一预算管理器。
// 准入同时预占业务和待拨号槽，不排队、不为被拒连接创建协程。
type resourceLimiter struct {
	mu                   sync.Mutex
	limits               ResourceLimits
	connections, pending int
	rules                map[string]int
}

type resourceLease struct {
	owner    *resourceLimiter
	rule     string
	pending  bool
	released bool
}

func (l *resourceLimiter) acquire(rule string) *resourceLease {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.connections >= l.limits.MaxConnections || l.rules[rule] >= l.limits.MaxConnectionsPerRule || l.pending >= l.limits.MaxPendingDials {
		return nil
	}
	l.connections++
	l.rules[rule]++
	l.pending++
	return &resourceLease{owner: l, rule: rule, pending: true}
}

// dialDone 仅释放拨号槽；连接总槽直到双向复制都退出才释放。
func (l *resourceLease) dialDone() {
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	if l.pending {
		l.owner.pending--
		l.pending = false
	}
}

func (l *resourceLease) release() {
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	if l.pending {
		l.owner.pending--
		l.pending = false
	}
	l.owner.connections--
	l.owner.rules[l.rule]--
	if l.owner.rules[l.rule] == 0 {
		delete(l.owner.rules, l.rule)
	}
}
