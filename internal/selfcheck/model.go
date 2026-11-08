// Package selfcheck 定义自测配置、原始证据与三态轮结果。
package selfcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"
)

type Settings struct {
	Sites                 []string `json:"sites"`
	IntervalSeconds       int      `json:"interval_seconds"`
	JitterSeconds         int      `json:"jitter_seconds"`
	ConnectTimeoutSeconds int      `json:"connect_timeout_seconds"`
	ResolveTimeoutSeconds int      `json:"resolve_timeout_seconds"`
	ResolveAttempts       int      `json:"resolve_attempts"`
	RoundTimeoutSeconds   int      `json:"round_timeout_seconds"`
}
type Config struct {
	Enabled bool `json:"enabled"`
	Settings
}

func Defaults() Settings {
	return Settings{Sites: []string{"tool.lu", "www.jikexueyuan.com", "www.ui.cn", "www.woshipm.com"}, IntervalSeconds: 120, JitterSeconds: 12, ConnectTimeoutSeconds: 5, ResolveTimeoutSeconds: 10, ResolveAttempts: 2, RoundTimeoutSeconds: 60}
}

var hostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?$`)

func (s Settings) Validate() error {
	if len(s.Sites) != 4 {
		return errors.New("自测须配置四个不同的国内站点域名")
	}
	seen := map[string]bool{}
	for _, host := range s.Sites {
		key := strings.ToLower(host)
		if len(host) > 253 || !hostname.MatchString(host) || !strings.Contains(host, ".") || strings.Contains(host, "..") || net.ParseIP(host) != nil || seen[key] {
			return errors.New("自测站点须为不同的域名，不带协议、路径或端口")
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return errors.New("自测站点域名无效")
			}
		}
		seen[key] = true
	}
	if s.IntervalSeconds < 30 || s.IntervalSeconds > 3600 || s.JitterSeconds < 0 || s.JitterSeconds > 30 || s.JitterSeconds*2 >= s.IntervalSeconds || s.ConnectTimeoutSeconds < 1 || s.ConnectTimeoutSeconds > 5 || s.ResolveTimeoutSeconds < 1 || s.ResolveTimeoutSeconds > 10 || s.ResolveAttempts < 1 || s.ResolveAttempts > 2 || s.RoundTimeoutSeconds < 1 || s.RoundTimeoutSeconds > 60 || s.RoundTimeoutSeconds > s.IntervalSeconds {
		return errors.New("自测参数超出范围：周期 30–3600 秒、抖动 0–30 秒、连接 1–5 秒、解析 1–10 秒且最多两次、单轮 1–60 秒")
	}
	return nil
}

type Connection struct {
	Address    string `json:"address"`
	Connected  bool   `json:"connected"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}
type Site struct {
	Host            string       `json:"host"`
	Addresses       []string     `json:"addresses"`
	ResolveAttempts int          `json:"resolve_attempts"`
	ResolveError    string       `json:"resolve_error,omitempty"`
	Connections     []Connection `json:"connections"`
	Result          string       `json:"result"`
}

func ConfigKey(c Config) string {
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type Round struct {
	ConfigKey      string    `json:"config_key"`
	ID             string    `json:"id"`
	Sequence       uint64    `json:"sequence"`
	ConfigRevision uint64    `json:"config_revision"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
	Source         string    `json:"source"`
	Result         string    `json:"result"`
	Sites          []Site    `json:"sites"`
}

// Result 保守处理解析失败或未完成；执行错误不能当作 TCP 不可达。
func Result(sites []Site) string {
	if len(sites) != 4 {
		return "unknown"
	}
	failed := 0
	for _, s := range sites {
		switch s.Result {
		case "unknown":
			return "unknown"
		case "failed":
			failed++
		case "normal":
		default:
			return "unknown"
		}
	}
	if failed >= 3 {
		return "abnormal"
	}
	return "normal"
}

func (r Round) Validate(now time.Time) error {
	if len(r.ID) != 32 || len(r.ConfigKey) != 64 || r.Sequence == 0 || r.Sequence > 1<<63-1 || r.ConfigRevision == 0 || r.ConfigRevision > 1<<63-1 || r.Source != "self_check" || r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || r.FinishedAt.After(now.Add(5*time.Minute)) || r.FinishedAt.Sub(r.StartedAt) > 60*time.Second || r.Result != Result(r.Sites) {
		return errors.New("自测轮字段无效")
	}
	if _, err := hex.DecodeString(r.ID + r.ConfigKey); err != nil {
		return errors.New("自测标识无效")
	}
	seen := map[string]bool{}
	for _, s := range r.Sites {
		if !hostname.MatchString(s.Host) || len(s.Host) > 253 || seen[s.Host] || s.ResolveAttempts < 0 || s.ResolveAttempts > 2 || len(s.ResolveError) > 200 || len(s.Addresses) > 2 || len(s.Connections) > 2 {
			return errors.New("自测站点证据无效")
		}
		seen[s.Host] = true
		addresses := map[string]bool{}
		for _, ip := range s.Addresses {
			parsed := net.ParseIP(ip)
			if parsed == nil || parsed.To4() == nil {
				return errors.New("自测地址无效")
			}
			addresses[net.JoinHostPort(ip, "443")] = true
		}
		success := false
		for _, c := range s.Connections {
			if !addresses[c.Address] || c.DurationMS < 0 || c.DurationMS > 60000 || len(c.Error) > 200 {
				return errors.New("自测连接证据无效")
			}
			success = success || c.Connected
		}
		if (s.Result == "normal" && !success) || (s.Result == "failed" && (success || len(s.Connections) == 0 || s.ResolveError != "")) || (s.Result == "unknown" && success) {
			return errors.New("自测结果与证据不一致")
		}
	}
	return nil
}
