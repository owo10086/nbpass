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

type Credentials struct{ TokenID, Token string }
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

type Provider interface {
	ReadRecord(context.Context, Credentials, Key) (ReadResult, error)
	WriteRecord(context.Context, Credentials, Key, Record) error
	SetRecordEnabled(context.Context, Credentials, Key, bool) error
}
