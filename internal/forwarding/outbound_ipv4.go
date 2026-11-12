package forwarding

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

type outboundBinding struct {
	source string
	err    error
}

func ValidateOutboundIPv4(source string) error {
	if source == "" {
		return nil
	}
	ip, err := netip.ParseAddr(source)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.MustParseAddr("255.255.255.255") {
		return errors.New("出站 IP 须为可用 IPv4 地址或空串")
	}
	return nil
}
func localIPv4Addresses() ([]netip.Addr, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			ips = append(ips, netip.AddrFrom4([4]byte(v4)))
		}
	}
	return ips, nil
}
func (e *Engine) validateOutbound(source string) *outboundBinding {
	b := &outboundBinding{source: source}
	if source == "" {
		return b
	}
	ips, err := e.deps.LocalIPv4()
	if err != nil {
		b.err = fmt.Errorf("出站 IP %s 校验本机 IPv4 失败: %w", source, err)
		return b
	}
	for _, ip := range ips {
		if ip.String() == source {
			return b
		}
	}
	b.err = fmt.Errorf("出站 IP %s 不是本机接口 IPv4 地址", source)
	return b
}
func (e *Engine) outboundBinding() *outboundBinding {
	if b := e.outbound.Load(); b != nil {
		return b
	}
	return &outboundBinding{}
}

// 同摘要缓存也须核对源字段和当前本机地址，不能遗漏补齐或地址消失。
func (e *Engine) OutboundIPv4Applied(source string) bool {
	b := e.outboundBinding()
	return b.source == source && b.err == nil && e.validateOutbound(source).err == nil
}
