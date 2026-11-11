package fakedns

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"nbpass/internal/dnsprovider"
)

// ServeHTTP 模拟腾讯云 API 3.0 的 DNSPod 边界；ModifyRecord 省略 Status 会默认启用。
// 与直接注入的替身共用记录、调用钩子、拒绝码及写入计数。
func (f *DNSPod) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain     string
		RecordId   json.Number
		SubDomain  string
		RecordType string
		RecordLine string
		LineID     string `json:"RecordLineId"`
		Value      string
		TTL        int
		Status     string
		Offset     int
		Limit      int
	}
	if r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=") || json.NewDecoder(r.Body).Decode(&body) != nil {
		http.Error(w, "请求无效", 400)
		return
	}
	key := dnsprovider.Key{Domain: body.Domain, RecordID: body.RecordId.String()}
	response := map[string]any{"RequestId": "fake"}
	var err error
	switch r.Header.Get("X-TC-Action") {
	case "DescribeRecord":
		var result dnsprovider.ReadResult
		result, err = f.ReadRecord(r.Context(), dnsprovider.Credentials{}, key)
		v := result.Record
		response["RecordInfo"] = map[string]any{"Id": json.Number(v.ID), "SubDomain": v.Name, "RecordType": v.Type, "RecordLine": v.Line, "RecordLineId": v.LineID, "Value": v.Value, "TTL": v.TTL, "Enabled": map[bool]int{false: 0, true: 1}[v.Enabled]}
	case "DescribeRecordList":
		f.mu.Lock()
		all := []dnsprovider.Record{}
		for k, v := range f.records {
			if k.Domain == key.Domain && (body.SubDomain == "" || v.Name == body.SubDomain) {
				all = append(all, v)
			}
		}
		f.mu.Unlock()
		slices.SortFunc(all, func(a, b dnsprovider.Record) int { return strings.Compare(a.ID, b.ID) })
		records := []any{}
		for _, v := range page(all, body.Offset, body.Limit) {
			records = append(records, wireRecord(v))
		}
		response["RecordList"] = records
		response["RecordCountInfo"] = map[string]int{"TotalCount": len(all), "ListCount": len(records)}
	case "DescribeDomainList":
		var domains []dnsprovider.Domain
		domains, err = f.ListDomains(r.Context(), dnsprovider.Credentials{})
		wire := []any{}
		for _, d := range page(domains, body.Offset, body.Limit) {
			wire = append(wire, map[string]any{"Name": d.Name, "Punycode": d.Name, "Status": strings.ToUpper(d.Status)})
		}
		response["DomainList"] = wire
		response["DomainCountInfo"] = map[string]int{"DomainTotal": len(domains)}
	case "ModifyRecord":
		value := f.Record(key.Domain, key.RecordID)
		value.Name = body.SubDomain
		value.Type = body.RecordType
		value.Line = body.RecordLine
		value.LineID = body.LineID
		value.Value = body.Value
		value.TTL = body.TTL
		value.Enabled = body.Status != "DISABLE"
		if value.Enabled && f.EnableError != "" {
			err = &dnsprovider.RejectionError{Code: f.EnableError}
		} else {
			err = f.WriteRecord(r.Context(), dnsprovider.Credentials{}, key, value)
		}
	case "CreateRecord":
		var id string
		id, err = f.CreateRecord(r.Context(), dnsprovider.Credentials{}, body.Domain, dnsprovider.Record{Name: body.SubDomain, Type: body.RecordType, Line: body.RecordLine, LineID: body.LineID, Value: body.Value, TTL: body.TTL, Enabled: body.Status != "DISABLE"})
		response["RecordId"] = json.Number(id)
	case "ModifyRecordStatus":
		err = f.SetRecordEnabled(r.Context(), dnsprovider.Credentials{}, key, body.Status == "ENABLE")
	default:
		http.Error(w, "不支持的 DNSPod 动作", 400)
		return
	}
	if err != nil {
		var rejection *dnsprovider.RejectionError
		if errors.As(err, &rejection) {
			response = map[string]any{"RequestId": "fake", "Error": map[string]string{"Code": rejection.Code, "Message": "服务商原文"}}
		} else {
			http.Error(w, "结果未确认", 504)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"Response": response})
}

func page[T any](items []T, offset, limit int) []T {
	if limit <= 0 {
		limit = 100
	}
	if offset >= len(items) {
		return nil
	}
	return items[offset:min(len(items), offset+limit)]
}

func wireRecord(r dnsprovider.Record) map[string]any {
	status := "DISABLE"
	if r.Enabled {
		status = "ENABLE"
	}
	return map[string]any{"RecordId": json.Number(r.ID), "Name": r.Name, "Type": r.Type, "Line": r.Line, "LineId": r.LineID, "Value": r.Value, "TTL": r.TTL, "Status": status}
}
