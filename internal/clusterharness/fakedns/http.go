package fakedns

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"nbpass/internal/dnsprovider"
)

// ServeHTTP 模拟真实 DNSPod 表单边界；Record.Modify 省略 status 会默认启用。
// 与直接注入的替身共用记录、调用钩子、拒绝码及写入计数。
func (f *DNSPod) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.ParseForm() != nil {
		http.Error(w, "请求无效", 400)
		return
	}
	key := dnsprovider.Key{Domain: r.Form.Get("domain"), RecordID: r.Form.Get("record_id")}
	response := map[string]any{"status": map[string]string{"code": "1"}}
	var err error
	switch r.URL.Path {
	case "/Record.Info":
		var result dnsprovider.ReadResult
		result, err = f.ReadRecord(r.Context(), dnsprovider.Credentials{}, key)
		response["record"] = wireRecord(result.Record)
	case "/Record.List":
		f.mu.Lock()
		records := []any{}
		for k, v := range f.records {
			if k.Domain == key.Domain && v.Name == r.Form.Get("sub_domain") {
				records = append(records, wireRecord(v))
			}
		}
		f.mu.Unlock()
		response["records"] = records
		response["info"] = map[string]int{"record_total": len(records)}
	case "/Record.Modify":
		value := f.Record(key.Domain, key.RecordID)
		value.Name = r.Form.Get("sub_domain")
		value.Type = r.Form.Get("record_type")
		value.LineID = r.Form.Get("record_line_id")
		value.Value = r.Form.Get("value")
		value.TTL, _ = strconv.Atoi(r.Form.Get("ttl"))
		value.Enabled = r.Form.Get("status") != "disable"
		if value.Enabled && f.EnableError != "" {
			err = &dnsprovider.RejectionError{Code: f.EnableError}
		} else {
			err = f.WriteRecord(r.Context(), dnsprovider.Credentials{}, key, value)
		}
	case "/Record.Status":
		err = f.SetRecordEnabled(r.Context(), dnsprovider.Credentials{}, key, r.Form.Get("status") == "enable")
	default:
		http.Error(w, "不支持的 DNSPod 动作", 400)
		return
	}
	if err != nil {
		var rejection *dnsprovider.RejectionError
		if errors.As(err, &rejection) {
			response = map[string]any{"status": map[string]string{"code": rejection.Code}}
		} else {
			http.Error(w, "结果未确认", 504)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func wireRecord(r dnsprovider.Record) map[string]any {
	status := "disable"
	if r.Enabled {
		status = "enable"
	}
	proxy := "no"
	if r.Proxied {
		proxy = "yes"
	}
	return map[string]any{"id": r.ID, "name": r.Name, "type": r.Type, "line": r.Line, "line_id": r.LineID, "value": r.Value, "ttl": r.TTL, "status": status, "use_aqb": proxy}
}
