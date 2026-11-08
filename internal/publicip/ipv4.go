// Package publicip 校验节点上报的单个出站公网 IPv4。
package publicip

import (
	"errors"
	"net/netip"
	"strings"
)

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func Parse(value string) (string, error) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "", errors.New("需要单个公网 IPv4")
	}
	for _, prefix := range reserved {
		if prefix.Contains(ip) {
			return "", errors.New("不支持保留 IPv4 地址")
		}
	}
	return ip.String(), nil
}
