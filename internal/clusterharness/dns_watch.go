package clusterharness

import (
	"context"
	"errors"
	"sync"

	"nbpass/internal/dnswatch"
)

// DNSResolver 只替换外部解析，节点调度、TLS 控制流、比对与存储均使用真实代码。
type DNSResolver struct {
	mu       sync.Mutex
	answers  map[string][]string
	failures map[string]bool
}

func NewDNSResolver() *DNSResolver {
	return &DNSResolver{answers: map[string][]string{}, failures: map[string]bool{}}
}
func (f *DNSResolver) Set(domain, address string, ips ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := domain + "|" + address
	f.answers[key] = append([]string{}, ips...)
	f.failures[key] = false
}
func (f *DNSResolver) Fail(domain, address string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[domain+"|"+address] = true
}
func (f *DNSResolver) Lookup(_ context.Context, domain string, resolver dnswatch.Resolver) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := domain + "|" + resolver.Address
	ips, ok := f.answers[key]
	if !ok || f.failures[key] {
		return nil, errors.New("假解析器采样失败")
	}
	return append([]string{}, ips...), nil
}
