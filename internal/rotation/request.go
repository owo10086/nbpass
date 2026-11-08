// Package rotation 定义管理员授权的 HTTP 换 IP 请求，不执行 Shell。
package rotation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type Config struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	// 精确授权 scheme://host[:port]/path；不接受网段、通配符或查询串。
	PrivateEndpoints []string `json:"private_endpoints"`
}

type Variables struct{ NodeID, NodeName, OldIP, ActionID, Timestamp string }

var variable = regexp.MustCompile(`\{\{([a-z_]+)\}\}`)

func expand(s string, v Variables, encode func(string) string) (string, error) {
	values := map[string]string{"node_id": v.NodeID, "node_name": v.NodeName, "old_ip": v.OldIP, "action_id": v.ActionID, "timestamp": v.Timestamp}
	// 只检查模板本身，不重新解释插入值中的大括号。
	remainder := variable.ReplaceAllString(s, "")
	if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
		return "", errors.New("模板变量无效")
	}
	var bad bool
	result := variable.ReplaceAllStringFunc(s, func(token string) string {
		value, ok := values[token[2:len(token)-2]]
		if !ok {
			bad = true
		}
		return encode(value)
	})
	if bad {
		return "", errors.New("模板变量无效")
	}
	return result, nil
}

func endpoint(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.EscapedPath()
}
func (c Config) allowsPrivate(u *url.URL) bool {
	for _, allowed := range c.PrivateEndpoints {
		if allowed == endpoint(u) {
			return true
		}
	}
	return false
}

func (c Config) Validate() error {
	if len(c.URL) == 0 || len(c.URL) > 4096 || len(c.Body) > 4096 || len(c.Headers) > 32 || len(c.PrivateEndpoints) > 16 {
		return errors.New("换 IP 请求过大或缺少 URL")
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(u.Host, "{}") || u.Opaque != "" {
		return errors.New("换 IP URL 无效，地址中不允许凭据或主机模板")
	}
	if u.Scheme == "http" && !c.allowsPrivate(u) {
		return errors.New("公网换 IP 接口必须使用 HTTPS；内网必须显式授权具体端点")
	}
	for _, allowed := range c.PrivateEndpoints {
		a, e := url.Parse(allowed)
		if e != nil || a.Hostname() == "" || a.User != nil || a.RawQuery != "" || a.Fragment != "" || strings.ContainsAny(allowed, "{}*") || (a.Scheme != "http" && a.Scheme != "https") || endpoint(a) != allowed {
			return errors.New("内网授权须为精确的 scheme://host[:port]/path")
		}
	}
	switch c.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
	default:
		return errors.New("HTTP 方法无效")
	}
	_, err = c.Request(context.Background(), Variables{NodeID: "node", NodeName: "node", OldIP: "8.8.8.8", ActionID: "action", Timestamp: "2030-01-01T00:00:00Z"})
	return err
}

func (c Config) Request(ctx context.Context, v Variables) (*http.Request, error) {
	// URL 权限边界在展开前固定；变量只能出现在路径及查询值中。
	u, err := url.Parse(c.URL)
	if err != nil || strings.ContainsAny(u.Host, "{}") || u.User != nil {
		return nil, errors.New("URL 主机无效")
	}
	path, err := expand(strings.NewReplacer("%7B%7B", "{{", "%7D%7D", "}}").Replace(u.EscapedPath()), v, func(s string) string {
		if s == "." || s == ".." {
			return strings.ReplaceAll(s, ".", "%2E")
		}
		return url.PathEscape(s)
	})
	if err != nil {
		return nil, err
	}
	query, err := expand(u.RawQuery, v, url.QueryEscape)
	if err != nil {
		return nil, err
	}
	u.Path, err = url.PathUnescape(path)
	if err != nil {
		return nil, err
	}
	u.RawPath = path
	u.RawQuery = query
	body := c.Body
	if body != "" {
		var value any
		if err := json.Unmarshal([]byte(body), &value); err != nil {
			return nil, errors.New("请求体必须为 JSON，变量仅可用于字符串值")
		}
		var walk func(any) (any, error)
		walk = func(x any) (any, error) {
			switch val := x.(type) {
			case string:
				return expand(val, v, func(s string) string { return s })
			case []any:
				for i, y := range val {
					next, e := walk(y)
					if e != nil {
						return nil, e
					}
					val[i] = next
				}
			case map[string]any:
				for k, y := range val {
					if strings.ContainsAny(k, "{}") {
						return nil, errors.New("JSON 键不支持模板")
					}
					next, e := walk(y)
					if e != nil {
						return nil, e
					}
					val[k] = next
				}
			}
			return x, nil
		}
		value, err = walk(value)
		if err != nil {
			return nil, err
		}
		data, e := json.Marshal(value)
		if e != nil {
			return nil, e
		}
		body = string(data)
	}
	request, err := http.NewRequestWithContext(ctx, c.Method, u.String(), strings.NewReader(body))
	if err != nil {
		return nil, errors.New("HTTP 请求无效")
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range c.Headers {
		if key == "" || strings.ContainsAny(key, "{} \r\n:") || strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Transfer-Encoding") || strings.EqualFold(key, "Connection") || strings.EqualFold(key, "Proxy-Authorization") {
			return nil, errors.New("请求头名称无效")
		}
		encoded, e := expand(value, v, url.PathEscape)
		if e != nil {
			return nil, e
		}
		if strings.ContainsAny(encoded, "\r\n") {
			return nil, errors.New("请求头值无效")
		}
		request.Header.Set(key, encoded)
	}
	return request, nil
}
