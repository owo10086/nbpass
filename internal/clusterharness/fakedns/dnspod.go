package fakedns

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
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
	Read        func(context.Context) error
	Write       func(context.Context) error
	Status      func(context.Context) error
	Create      func(context.Context) error
	EnableError string
	ModifyError string
	CreateError string
	nextID      int
}

func NewDNSPod() *DNSPod { return &DNSPod{records: make(map[dnsprovider.Key]dnsprovider.Record)} }

func (f *DNSPod) SetRecordEnabled(ctx context.Context, _ dnsprovider.Credentials, key dnsprovider.Key, enabled bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "status")
	f.writes++
	if enabled && f.EnableError != "" {
		code := f.EnableError
		f.mu.Unlock()
		return &dnsprovider.RejectionError{Code: code}
	}
	r, ok := f.records[key]
	if !ok {
		f.mu.Unlock()
		return &dnsprovider.RejectionError{Code: "8"}
	}
	r.Enabled = enabled
	f.records[key] = r
	f.mu.Unlock()
	if f.Status != nil {
		return f.Status(ctx)
	}
	return nil
}
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

// ListDomains 由已有记录推导账号下的域名；与读取共用 Read 钩子以模拟服务商故障。
func (f *DNSPod) ListDomains(ctx context.Context, _ dnsprovider.Credentials) ([]dnsprovider.Domain, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "list")
	f.mu.Unlock()
	if f.Read != nil {
		if err := f.Read(ctx); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	names := []string{}
	for k := range f.records {
		if !slices.Contains(names, k.Domain) {
			names = append(names, k.Domain)
		}
	}
	slices.Sort(names)
	domains := []dnsprovider.Domain{}
	for _, name := range names {
		domains = append(domains, dnsprovider.Domain{Name: name, Status: "enable"})
	}
	return domains, nil
}
func (f *DNSPod) ListRecords(ctx context.Context, _ dnsprovider.Credentials, domain string) ([]dnsprovider.Record, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "list")
	f.mu.Unlock()
	if f.Read != nil {
		if err := f.Read(ctx); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	records := []dnsprovider.Record{}
	for k, r := range f.records {
		if k.Domain == domain {
			records = append(records, r)
		}
	}
	slices.SortFunc(records, func(a, b dnsprovider.Record) int { return strings.Compare(a.ID, b.ID) })
	return records, nil
}
func (f *DNSPod) FindRecords(ctx context.Context, auth dnsprovider.Credentials, domain, name string) ([]dnsprovider.Record, error) {
	all, err := f.ListRecords(ctx, auth, domain)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(r dnsprovider.Record) bool { return !strings.EqualFold(r.Name, name) }), nil
}

// CreateRecord 分配递增的数字 ID；Create 钩子在记录落库后执行，可模拟"已建成但回包丢失"。
// 同名同线路下 CNAME 与其他记录互斥，按 DNSPod 规则明确拒绝。
func (f *DNSPod) CreateRecord(ctx context.Context, _ dnsprovider.Credentials, domain string, r dnsprovider.Record) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "create")
	f.writes++
	if f.CreateError != "" {
		code := f.CreateError
		f.mu.Unlock()
		return "", &dnsprovider.RejectionError{Code: code}
	}
	for k, other := range f.records {
		if k.Domain == domain && strings.EqualFold(other.Name, r.Name) && other.LineID == r.LineID && (other.Type == "CNAME" || r.Type == "CNAME") {
			f.mu.Unlock()
			return "", &dnsprovider.RejectionError{Code: "InvalidParameter.DomainRecordExist"}
		}
	}
	f.nextID++
	r.ID = strconv.Itoa(9000 + f.nextID)
	f.records[dnsprovider.Key{Domain: domain, RecordID: r.ID}] = r
	f.mu.Unlock()
	if f.Create != nil {
		if err := f.Create(ctx); err != nil {
			return "", err
		}
	}
	return r.ID, nil
}

func (f *DNSPod) WriteRecord(ctx context.Context, _ dnsprovider.Credentials, key dnsprovider.Key, r dnsprovider.Record) error {
	f.mu.Lock()
	f.calls = append(f.calls, "write")
	f.writes++
	if f.ModifyError != "" {
		code := f.ModifyError
		f.mu.Unlock()
		return &dnsprovider.RejectionError{Code: code}
	}
	f.records[key] = r
	f.mu.Unlock()
	if f.Write != nil {
		return f.Write(ctx)
	}
	return nil
}
