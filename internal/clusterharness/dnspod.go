package clusterharness

import (
	"context"
	"errors"
	"sync"

	"nbpass/internal/dnsprovider"
)

// DNSPod 是可注入的外部服务替身，记录变更不会绕过真实面板核心。
type DNSPod struct {
	mu      sync.Mutex
	records map[dnsprovider.Key]dnsprovider.Record
	calls   []string
	writes  int
	// 钩子模拟响应丢失、读失败、阻塞及崩溃时的外部执行；不持有假服务锁。
	Read  func(context.Context) error
	Write func(context.Context) error
}

func NewDNSPod() *DNSPod { return &DNSPod{records: make(map[dnsprovider.Key]dnsprovider.Record)} }
func (f *DNSPod) Set(domain string, r dnsprovider.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[dnsprovider.Key{Domain: domain, RecordID: r.ID}] = r
}
func (f *DNSPod) Record(domain, id string) dnsprovider.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.records[dnsprovider.Key{Domain: domain, RecordID: id}]
}
func (f *DNSPod) Writes() int { f.mu.Lock(); defer f.mu.Unlock(); return f.writes }
func (f *DNSPod) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
func (f *DNSPod) ReadRecord(ctx context.Context, _ dnsprovider.Credentials, key dnsprovider.Key) (dnsprovider.ReadResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "read")
	f.mu.Unlock()
	if f.Read != nil {
		if err := f.Read(ctx); err != nil {
			return dnsprovider.ReadResult{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.records[key]
	if !ok {
		return dnsprovider.ReadResult{}, errors.New("记录不存在")
	}
	result := dnsprovider.ReadResult{Record: r}
	for k, other := range f.records {
		if k.Domain == key.Domain && other.Name == r.Name {
			result.Related = append(result.Related, other)
		}
	}
	return result, nil
}
func (f *DNSPod) WriteRecord(ctx context.Context, _ dnsprovider.Credentials, key dnsprovider.Key, r dnsprovider.Record) error {
	f.mu.Lock()
	f.calls = append(f.calls, "write")
	f.writes++
	f.records[key] = r
	f.mu.Unlock()
	if f.Write != nil {
		return f.Write(ctx)
	}
	return nil
}
