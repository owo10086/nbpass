package dnsprovider

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DNSPod 使用腾讯云 API 3.0（SecretId/SecretKey，TC3-HMAC-SHA256 签名）调用记录接口。
// URL 与 Client 仅由核心依赖注入，不能从管理员请求指定任意端点。
type DNSPod struct {
	Client *http.Client
	URL    string
}

var dnsPodClient = &http.Client{
	Transport:     &http.Transport{ForceAttemptHTTP2: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const (
	tc3Service     = "dnspod"
	tc3Version     = "2021-03-23"
	tc3ContentType = "application/json; charset=utf-8"
)

// tc3CanonicalRequest 按签名 v3 拼接规范请求；签名头固定为 content-type、host 与 x-tc-action。
func tc3CanonicalRequest(host, contentType, action string, body []byte) string {
	hashed := sha256.Sum256(body)
	return "POST\n/\n\ncontent-type:" + contentType + "\nhost:" + host + "\nx-tc-action:" + strings.ToLower(action) + "\n\ncontent-type;host;x-tc-action\n" + hex.EncodeToString(hashed[:])
}

func tc3Authorization(auth Credentials, service, host, contentType, action string, body []byte, timestamp int64) string {
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	scope := date + "/" + service + "/tc3_request"
	canonical := sha256.Sum256([]byte(tc3CanonicalRequest(host, contentType, action, body)))
	stringToSign := "TC3-HMAC-SHA256\n" + strconv.FormatInt(timestamp, 10) + "\n" + scope + "\n" + hex.EncodeToString(canonical[:])
	key := []byte("TC3" + auth.SecretKey)
	for _, part := range []string{date, service, "tc3_request"} {
		key = hmacSHA256(key, part)
	}
	return "TC3-HMAC-SHA256 Credential=" + auth.SecretID + "/" + scope + ", SignedHeaders=content-type;host;x-tc-action, Signature=" + hex.EncodeToString(hmacSHA256(key, stringToSign))
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

type podRecord struct {
	RecordID uint64 `json:"RecordId"`
	Name     string `json:"Name"`
	Type     string `json:"Type"`
	Line     string `json:"Line"`
	LineID   string `json:"LineId"`
	Value    string `json:"Value"`
	TTL      int    `json:"TTL"`
	Status   string `json:"Status"`
}

func (r podRecord) record() (Record, error) {
	return podValues(r.RecordID, r.Name, r.Type, r.Line, r.LineID, r.Value, r.TTL, r.Status == "ENABLE")
}

// podRecordInfo 是 DescribeRecord 的单条结构，字段名与列表项不同。
type podRecordInfo struct {
	ID           uint64 `json:"Id"`
	SubDomain    string `json:"SubDomain"`
	RecordType   string `json:"RecordType"`
	RecordLine   string `json:"RecordLine"`
	RecordLineID string `json:"RecordLineId"`
	Value        string `json:"Value"`
	TTL          int    `json:"TTL"`
	Enabled      int    `json:"Enabled"`
}

func podValues(id uint64, name, kind, line, lineID, value string, ttl int, enabled bool) (Record, error) {
	if ttl < 1 {
		return Record{}, errors.New("DNSPod TTL 无效")
	}
	// 默认线路的 ID 固定为 0，个别响应省略时补齐。
	if lineID == "" && (line == "默认" || line == "default") {
		lineID = "0"
	}
	return Record{ID: strconv.FormatUint(id, 10), Name: name, Type: kind, Line: line, LineID: lineID, Value: value, TTL: ttl, Enabled: enabled}, nil
}

func (d DNSPod) call(ctx context.Context, auth Credentials, action string, payload map[string]any, out any) error {
	endpoint := d.URL
	if endpoint == "" {
		endpoint = "https://dnspod.tencentcloudapi.com"
	}
	target, err := url.Parse(strings.TrimRight(endpoint, "/") + "/")
	if err != nil || target.Host == "" {
		return errors.New("DNSPod 请求配置无效")
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", target.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("DNSPod 请求配置无效")
	}
	timestamp := time.Now().Unix()
	req.Header.Set("Authorization", tc3Authorization(auth, tc3Service, strings.ToLower(target.Host), tc3ContentType, action, body, timestamp))
	req.Header.Set("Content-Type", tc3ContentType)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-TC-Version", tc3Version)
	req.Header.Set("X-TC-Language", "zh-CN")
	req.Header.Set("User-Agent", "nbpass/0.1.0")
	client := d.Client
	if client == nil {
		client = dnsPodClient
	}
	// 即使注入客户端允许重定向，也不让携带凭据的请求被转发。
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := safeClient.Do(req)
	if err != nil {
		return errors.New("DNSPod 请求未确认")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || resp.StatusCode != 200 {
		return errors.New("DNSPod 响应不可用")
	}
	var envelope struct {
		Response json.RawMessage `json:"Response"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || len(envelope.Response) == 0 || string(envelope.Response) == "null" {
		return errors.New("DNSPod 响应无效")
	}
	var failure struct {
		Error *struct {
			Code string `json:"Code"`
		} `json:"Error"`
	}
	if json.Unmarshal(envelope.Response, &failure) != nil {
		return errors.New("DNSPod 响应无效")
	}
	if failure.Error != nil {
		// 内部错误时服务商可能已执行，不能当成明确拒绝。
		if failure.Error.Code == "" || strings.HasPrefix(failure.Error.Code, "InternalError") {
			return errors.New("DNSPod 服务内部错误，结果未确认")
		}
		return &RejectionError{Code: failure.Error.Code}
	}
	if out != nil && json.Unmarshal(envelope.Response, out) != nil {
		return errors.New("DNSPod 响应无效")
	}
	return nil
}

func recordID(key Key) (uint64, error) {
	id, err := strconv.ParseUint(key.RecordID, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("DNSPod 记录 ID 无效")
	}
	return id, nil
}

// emptyList 表示服务商以“无数据”拒绝列表查询，等同于空列表。
func emptyList(err error) bool {
	var rejection *RejectionError
	return errors.As(err, &rejection) && strings.HasPrefix(rejection.Code, "ResourceNotFound.NoDataOf")
}

type podRecordPage struct {
	RecordCountInfo struct {
		TotalCount int `json:"TotalCount"`
	} `json:"RecordCountInfo"`
	RecordList []podRecord `json:"RecordList"`
}

// listRecordPages 逐页读取记录；读满总数或遇到空页即结束，超过 listPages 页时报错而不是返回残缺列表。
// 文档未说明按主机头过滤时 TotalCount 是否随之缩小，空页兜底两种口径。
func (d DNSPod) listRecordPages(ctx context.Context, auth Credentials, domain, subDomain string, each func(podRecord) error) error {
	offset := 0
	for range listPages {
		payload := map[string]any{"Domain": domain, "Offset": offset, "Limit": listPageLength, "ErrorOnEmpty": "no"}
		if subDomain != "" {
			payload["SubDomain"] = subDomain
		}
		var page podRecordPage
		err := d.call(ctx, auth, "DescribeRecordList", payload, &page)
		if emptyList(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, wire := range page.RecordList {
			if err := each(wire); err != nil {
				return err
			}
		}
		offset += len(page.RecordList)
		if len(page.RecordList) == 0 || offset >= page.RecordCountInfo.TotalCount {
			return nil
		}
	}
	return errors.New("DNSPod 记录过多，无法完整列出")
}

func (d DNSPod) ReadRecord(ctx context.Context, auth Credentials, key Key) (ReadResult, error) {
	id, err := recordID(key)
	if err != nil {
		return ReadResult{}, err
	}
	var info struct {
		RecordInfo podRecordInfo `json:"RecordInfo"`
	}
	if err := d.call(ctx, auth, "DescribeRecord", map[string]any{"Domain": key.Domain, "RecordId": id}, &info); err != nil {
		return ReadResult{}, err
	}
	wire := info.RecordInfo
	record, err := podValues(wire.ID, wire.SubDomain, wire.RecordType, wire.RecordLine, wire.RecordLineID, wire.Value, wire.TTL, wire.Enabled == 1)
	if err != nil || record.ID != key.RecordID || record.Name == "" {
		return ReadResult{}, errors.New("DNSPod 记录身份无效")
	}
	result := ReadResult{Record: record, Related: []Record{}}
	// 必须读全同名列表；不能悄悄漏掉多线路或 AAAA。
	selectedSeen := false
	err = d.listRecordPages(ctx, auth, key.Domain, record.Name, func(wire podRecord) error {
		r, err := wire.record()
		if err != nil {
			return err
		}
		if r.ID == record.ID {
			if r.Name != record.Name || r.Type != record.Type || r.LineID != record.LineID || r.Value != record.Value || r.TTL != record.TTL || r.Enabled != record.Enabled {
				return errors.New("DNSPod 记录在读取期间发生变化，请重新核对")
			}
			selectedSeen = true
		}
		if r.Name == record.Name {
			result.Related = append(result.Related, r)
		}
		return nil
	})
	if err != nil {
		return ReadResult{}, err
	}
	if !selectedSeen {
		return ReadResult{}, errors.New("DNSPod 列表未包含受管记录，等待索引收敛后重试")
	}
	return result, nil
}

// listPageLength 是 DNSPod 单页上限；列表最多读 listPages 页，超出时报错而不是返回残缺列表。
const (
	listPageLength = 3000
	listPages      = 10
)

func (d DNSPod) ListDomains(ctx context.Context, auth Credentials) ([]Domain, error) {
	domains := []Domain{}
	for range listPages {
		var page struct {
			DomainCountInfo struct {
				DomainTotal int `json:"DomainTotal"`
			} `json:"DomainCountInfo"`
			DomainList []struct {
				Name     string `json:"Name"`
				Punycode string `json:"Punycode"`
				Status   string `json:"Status"`
			} `json:"DomainList"`
		}
		err := d.call(ctx, auth, "DescribeDomainList", map[string]any{"Type": "ALL", "Offset": len(domains), "Limit": listPageLength}, &page)
		if emptyList(err) {
			return domains, nil
		}
		if err != nil {
			return nil, err
		}
		for _, wire := range page.DomainList {
			name := wire.Punycode
			if name == "" {
				name = wire.Name
			}
			domains = append(domains, Domain{Name: name, Status: strings.ToLower(wire.Status)})
		}
		if len(page.DomainList) == 0 || len(domains) >= page.DomainCountInfo.DomainTotal {
			return domains, nil
		}
	}
	return nil, errors.New("DNSPod 域名过多，无法完整列出")
}

func (d DNSPod) ListRecords(ctx context.Context, auth Credentials, domain string) ([]Record, error) {
	records := []Record{}
	err := d.listRecordPages(ctx, auth, domain, "", func(wire podRecord) error {
		// 个别字段异常的记录本就无法绑定，跳过即可；绑定时仍会完整读回同名记录。
		if r, err := wire.record(); err == nil {
			records = append(records, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

func (d DNSPod) FindRecords(ctx context.Context, auth Credentials, domain, name string) ([]Record, error) {
	if name == "" {
		return nil, errors.New("DNSPod 主机记录名无效")
	}
	records := []Record{}
	err := d.listRecordPages(ctx, auth, domain, name, func(wire podRecord) error {
		// SubDomain 过滤按服务商口径，仍逐条核对名字，避免模糊匹配混入其他记录。
		if r, err := wire.record(); err == nil && strings.EqualFold(r.Name, name) {
			records = append(records, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return records, nil
}

// writableType 是面板允许写入的记录类型。
func writableType(r Record) bool {
	return (r.Type == "A" || r.Type == "CNAME") && !r.Proxied && r.Name != "" && r.Line != "" && r.LineID != "" && r.TTL > 0
}

func (d DNSPod) CreateRecord(ctx context.Context, auth Credentials, domain string, r Record) (string, error) {
	if !writableType(r) || r.ID != "" {
		return "", errors.New("DNSPod 新建记录不受支持")
	}
	var created struct {
		RecordID uint64 `json:"RecordId"`
	}
	if err := d.call(ctx, auth, "CreateRecord", map[string]any{"Domain": domain, "SubDomain": r.Name, "RecordType": r.Type, "RecordLine": r.Line, "RecordLineId": r.LineID, "Value": r.Value, "TTL": r.TTL, "Status": recordStatus(r.Enabled)}, &created); err != nil {
		return "", err
	}
	if created.RecordID == 0 {
		return "", errors.New("DNSPod 新建结果未确认")
	}
	return strconv.FormatUint(created.RecordID, 10), nil
}

func (d DNSPod) WriteRecord(ctx context.Context, auth Credentials, key Key, r Record) error {
	id, err := recordID(key)
	if err != nil || r.ID != key.RecordID || !writableType(r) {
		return errors.New("DNSPod 写入记录不受支持")
	}
	return d.call(ctx, auth, "ModifyRecord", map[string]any{"Domain": key.Domain, "RecordId": id, "SubDomain": r.Name, "RecordType": r.Type, "RecordLine": r.Line, "RecordLineId": r.LineID, "Value": r.Value, "TTL": r.TTL, "Status": recordStatus(r.Enabled)}, nil)
}

func recordStatus(enabled bool) string {
	if enabled {
		return "ENABLE"
	}
	return "DISABLE"
}

func (d DNSPod) SetRecordEnabled(ctx context.Context, auth Credentials, key Key, enabled bool) error {
	id, err := recordID(key)
	if err != nil {
		return err
	}
	return d.call(ctx, auth, "ModifyRecordStatus", map[string]any{"Domain": key.Domain, "RecordId": id, "Status": recordStatus(enabled)}, nil)
}
