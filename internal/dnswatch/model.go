// Package dnswatch 提供与域名来源、通知和 DNS 写入无关的解析监控模型。
package dnswatch

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

type Resolver struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
}

func (r Resolver) Key() string { return r.Kind + ":" + r.Address }
func (r Resolver) Validate() error {
	switch r.Kind {
	case "dns":
		host, port, err := net.SplitHostPort(r.Address)
		ip, ipErr := netip.ParseAddr(host)
		if err != nil || ipErr != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || port != "53" {
			return errors.New("国内 DNS 须为公共 IPv4 地址及 53 端口")
		}
	case "doh":
		u, err := url.Parse(r.Address)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || len(r.Address) > 500 {
			return errors.New("DoH 须为不带凭据、查询参数的 HTTPS 地址")
		}
	default:
		return errors.New("解析器类型须为 dns 或 doh")
	}
	return nil
}

func Domain(value string) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil || len(ascii) > 253 || net.ParseIP(ascii) != nil {
		return "", errors.New("域名无效")
	}
	for _, label := range strings.Split(ascii, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("域名无效")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("域名无效")
			}
		}
	}
	return ascii, nil
}

func Addresses(values []string) ([]string, error) {
	if len(values) > 64 {
		return nil, errors.New("IPv4 地址最多 64 个")
	}
	result := []string{}
	for _, value := range values {
		ip, err := netip.ParseAddr(value)
		if err != nil || !ip.Is4() {
			return nil, errors.New("仅支持 IPv4 地址")
		}
		result = append(result, ip.String())
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

// Expectation 的版本由调用方拥有；入口组可直接复用其发布期望版本和旧 TTL。
type Expectation struct {
	Version             int64
	IPs                 []string
	PreviousIPs         []string
	IgnorePreviousUntil time.Time
}
type Progress struct {
	ExpectedVersion       int64 `json:"expected_version"`
	ConsecutiveMismatches int   `json:"consecutive_mismatches"`
	ConsecutiveMatches    int   `json:"consecutive_matches"`
}

// Observe 只计算证据，不执行动作。失败和 TTL 内的旧值均中断连续异常。
func (p *Progress) Observe(e Expectation, ips []string, failed bool, now time.Time) string {
	if p.ExpectedVersion != e.Version {
		*p = Progress{ExpectedVersion: e.Version}
	}
	if failed {
		p.ConsecutiveMatches, p.ConsecutiveMismatches = 0, 0
		return "采样失败"
	}
	if len(e.IPs) == 0 {
		p.ConsecutiveMatches, p.ConsecutiveMismatches = 0, 0
		return "观察中"
	}
	match := len(ips) > 0
	for _, ip := range ips {
		match = match && slices.Contains(e.IPs, ip)
	}
	if match {
		p.ConsecutiveMismatches = 0
		p.ConsecutiveMatches++
		return "符合期望"
	}
	if now.Before(e.IgnorePreviousUntil) && slices.Equal(ips, e.PreviousIPs) {
		p.ConsecutiveMatches, p.ConsecutiveMismatches = 0, 0
		return "传播中"
	}
	p.ConsecutiveMatches = 0
	p.ConsecutiveMismatches++
	if p.ConsecutiveMismatches >= 3 {
		return "期望不符"
	}
	return "待确认"
}
