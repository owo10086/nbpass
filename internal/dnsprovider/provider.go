// Package dnsprovider 隔离受管记录的服务商读取与写入。
package dnsprovider

import (
	"context"
	"fmt"
)

// RejectionError 仅表示服务商返回有效失败码，不能用于网络超时或损坏响应。
// 不保存服务商原始消息，避免凭据或其他敏感内容进入事件与通知。
type RejectionError struct{ Code string }

func (e *RejectionError) Error() string {
	return fmt.Sprintf("DNSPod 明确拒绝请求（代码 %s）", e.Code)
}

// Credentials 是腾讯云 API 密钥。
type Credentials struct{ SecretID, SecretKey string }
type Key struct{ Domain, RecordID string }
type Record struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Line    string `json:"line"`
	LineID  string `json:"line_id"`
	Value   string `json:"value"`
	TTL     int    `json:"ttl"`
	Enabled bool   `json:"enabled"`
	Proxied bool   `json:"proxied"`
}

// Related 包含同名记录，供单线路校验及非受管 A/AAAA 提示；不授予修改权限。
type ReadResult struct {
	Record  Record
	Related []Record
}

// Domain 是账号下可浏览的主域名；Name 为 ASCII 形式，可直接用于绑定。
type Domain struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Provider interface {
	// ListDomains 与 ListRecords 仅供管理员绑定时挑选记录，绑定仍以 ReadRecord 读回为准。
	ListDomains(context.Context, Credentials) ([]Domain, error)
	ListRecords(context.Context, Credentials, string) ([]Record, error)
	ReadRecord(context.Context, Credentials, Key) (ReadResult, error)
	// FindRecords 列出主域名下某个主机记录名的全部记录（各类型、各线路），供按名字绑定。
	FindRecords(ctx context.Context, auth Credentials, domain, name string) ([]Record, error)
	// CreateRecord 新建记录并返回记录 ID；Enabled 决定初始状态，面板新建时总是暂停。
	CreateRecord(ctx context.Context, auth Credentials, domain string, r Record) (string, error)
	// WriteRecord 改值；类型可在 A 与 CNAME 之间改变，用于经管理员确认的改类型接管。
	WriteRecord(context.Context, Credentials, Key, Record) error
	SetRecordEnabled(context.Context, Credentials, Key, bool) error
}
