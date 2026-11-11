package dnsprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DNSPod 使用 DNSPod API Token，经 HTTPS 调用记录接口。
// URL 与 Client 仅由核心依赖注入，不能从管理员请求指定任意端点。
type DNSPod struct {
	Client *http.Client
	URL    string
}

var dnsPodClient = &http.Client{
	Transport:     &http.Transport{ForceAttemptHTTP2: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type scalar string

func (s *scalar) UnmarshalJSON(data []byte) error {
	var value string
	if json.Unmarshal(data, &value) == nil {
		*s = scalar(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*s = scalar(number.String())
	return nil
}

type podRecord struct {
	UseAQB       string `json:"use_aqb"`
	ID           scalar `json:"id"`
	Name         string `json:"name"`
	Subdomain    string `json:"sub_domain"`
	Type         string `json:"type"`
	RecordType   string `json:"record_type"`
	Line         string `json:"line"`
	RecordLine   string `json:"record_line"`
	LineID       scalar `json:"line_id"`
	RecordLineID scalar `json:"record_line_id"`
	Value        string `json:"value"`
	TTL          scalar `json:"ttl"`
	Status       string `json:"status"`
	Enabled      scalar `json:"enabled"`
}

func (r podRecord) record() (Record, error) {
	ttl, err := strconv.Atoi(string(r.TTL))
	if err != nil || ttl < 1 {
		return Record{}, errors.New("DNSPod TTL 无效")
	}
	if r.Name == "" {
		r.Name = r.Subdomain
	}
	if r.Type == "" {
		r.Type = r.RecordType
	}
	if r.Line == "" {
		r.Line = r.RecordLine
	}
	if r.LineID == "" {
		r.LineID = r.RecordLineID
	}
	// 旧版 Record.Info 仅返回 record_line，默认线路的 ID 固定为 0。
	if r.LineID == "" && (r.Line == "默认" || r.Line == "default") {
		r.LineID = "0"
	}
	return Record{ID: string(r.ID), Name: r.Name, Type: r.Type, Line: r.Line, LineID: string(r.LineID), Value: r.Value, TTL: ttl, Enabled: r.Status == "enable" || r.Status == "enabled" || r.Enabled == "1", Proxied: r.UseAQB != "" && r.UseAQB != "no"}, nil
}

type podResponse struct {
	Status struct {
		Code scalar `json:"code"`
	} `json:"status"`
	Record  podRecord   `json:"record"`
	Records []podRecord `json:"records"`
	Info    struct {
		Total scalar `json:"record_total"`
	} `json:"info"`
}

func (d DNSPod) call(ctx context.Context, auth Credentials, action string, form url.Values) (podResponse, error) {
	var result podResponse
	endpoint := d.URL
	if endpoint == "" {
		endpoint = "https://dnsapi.cn"
	}
	form.Set("login_token", auth.TokenID+","+auth.Token)
	form.Set("format", "json")
	form.Set("lang", "cn")
	form.Set("error_on_empty", "no")
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(endpoint, "/")+"/"+action, strings.NewReader(form.Encode()))
	if err != nil {
		return result, errors.New("DNSPod 请求配置无效")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
		return result, errors.New("DNSPod 请求未确认")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || resp.StatusCode != 200 {
		return result, errors.New("DNSPod 响应不可用")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || result.Status.Code == "" {
		return result, errors.New("DNSPod 响应无效")
	}
	if result.Status.Code != "1" {
		if _, err := strconv.Atoi(string(result.Status.Code)); err != nil {
			return result, errors.New("DNSPod 响应无效")
		}
		return result, &RejectionError{Code: string(result.Status.Code)}
	}
	return result, nil
}
func (d DNSPod) ReadRecord(ctx context.Context, auth Credentials, key Key) (ReadResult, error) {
	info, err := d.call(ctx, auth, "Record.Info", url.Values{"domain": {key.Domain}, "record_id": {key.RecordID}})
	if err != nil {
		return ReadResult{}, err
	}
	record, err := info.Record.record()
	if err != nil || record.ID != key.RecordID || record.Name == "" {
		return ReadResult{}, errors.New("DNSPod 记录身份无效")
	}
	result := ReadResult{Record: record, Related: []Record{}}
	// 必须读全同名列表；达到上限时拒绝绑定，不能悄悄漏掉多线路或 AAAA。
	offset := 0
	selectedSeen := false
	for range 100 {
		page, err := d.call(ctx, auth, "Record.List", url.Values{"domain": {key.Domain}, "sub_domain": {record.Name}, "offset": {strconv.Itoa(offset)}, "length": {"100"}})
		if err != nil {
			return ReadResult{}, err
		}
		for _, wire := range page.Records {
			r, err := wire.record()
			if err != nil {
				return ReadResult{}, err
			}
			if r.ID == record.ID {
				if r.Name != record.Name || r.Type != record.Type || r.LineID != record.LineID || r.Value != record.Value || r.TTL != record.TTL || r.Enabled != record.Enabled {
					return ReadResult{}, errors.New("DNSPod 记录在读取期间发生变化，请重新核对")
				}
				selectedSeen = true
				result.Record.Proxied = result.Record.Proxied || r.Proxied
			}
			if r.Name == record.Name {
				result.Related = append(result.Related, r)
			}
		}
		offset += len(page.Records)
		total, err := strconv.Atoi(string(page.Info.Total))
		if err != nil || total < 0 {
			return ReadResult{}, errors.New("DNSPod 记录列表总量无效")
		}
		if offset >= total {
			if !selectedSeen {
				return ReadResult{}, errors.New("DNSPod 列表未包含受管记录，等待索引收敛后重试")
			}
			return result, nil
		}
		if len(page.Records) == 0 {
			return ReadResult{}, errors.New("DNSPod 记录列表不完整")
		}
	}
	return ReadResult{}, errors.New("DNSPod 同名记录过多，无法完整核对")
}
func (d DNSPod) WriteRecord(ctx context.Context, auth Credentials, key Key, r Record) error {
	if r.ID != key.RecordID || r.Type != "A" || r.Proxied || r.LineID == "" {
		return errors.New("DNSPod 写入记录不受支持")
	}
	_, err := d.call(ctx, auth, "Record.Modify", url.Values{"domain": {key.Domain}, "record_id": {key.RecordID}, "sub_domain": {r.Name}, "record_type": {"A"}, "record_line_id": {r.LineID}, "value": {r.Value}, "ttl": {strconv.Itoa(r.TTL)}, "status": {recordStatus(r.Enabled)}})
	return err
}

func recordStatus(enabled bool) string {
	if enabled {
		return "enable"
	}
	return "disable"
}

func (d DNSPod) SetRecordEnabled(ctx context.Context, auth Credentials, key Key, enabled bool) error {
	_, err := d.call(ctx, auth, "Record.Status", url.Values{"domain": {key.Domain}, "record_id": {key.RecordID}, "status": {recordStatus(enabled)}})
	return err
}
