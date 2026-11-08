// Package forwarding 定义直连规则、完整配置快照和入口数据面。
package forwarding

import (
	"errors"
	"net/netip"
	"strings"

	"nbpass/internal/controlpb"
)

type AddressFamily string
type RuleKind string
type TunnelTransport string
type ExitStrategy string
type DNSProvider string

const (
	IPv4          AddressFamily   = "ipv4"
	Direct        RuleKind        = "direct"
	Tunnel        RuleKind        = "tunnel"
	WSS           TunnelTransport = "wss"
	PrimaryBackup ExitStrategy    = "primary_backup"
	DNSPod        DNSProvider     = "dnspod"
)

type Rule struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	EntryGroupID  string        `json:"entry_group_id"`
	Kind          RuleKind      `json:"kind"`
	ExitGroupID   string        `json:"exit_group_id,omitempty"`
	ListenPort    int           `json:"listen_port"`
	TargetAddress string        `json:"target_address"`
	TargetFamily  AddressFamily `json:"target_family"`
	TargetPort    int           `json:"target_port"`
	AllowPrivate  bool          `json:"allow_private"`
	Enabled       bool          `json:"enabled"`
}

func (r *Rule) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	r.TargetAddress = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(r.TargetAddress), "."))
	if r.Name == "" || len(r.Name) > 200 || r.EntryGroupID == "" {
		return errors.New("请指定规则名称和入口组")
	}
	if (r.Kind != Direct && r.Kind != Tunnel) || r.TargetFamily != IPv4 {
		return errors.New("当前仅支持 TCP、IPv4 的直连或隧道规则")
	}
	if r.Kind == Tunnel && r.ExitGroupID == "" || r.Kind == Direct && r.ExitGroupID != "" {
		return errors.New("请为隧道规则指定出口组，直连规则不使用出口组")
	}
	if r.ListenPort < 1 || r.ListenPort > 65535 || r.TargetPort < 1 || r.TargetPort > 65535 {
		return errors.New("端口须为 1–65535")
	}
	if err := ValidateTarget(r.TargetAddress, r.AllowPrivate); err != nil {
		return err
	}
	return nil
}

func ValidateTarget(host string, allowPrivate bool) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.Is4() {
			return errors.New("目标仅支持 IPv4 或域名")
		}
		if !AllowedIP(ip, allowPrivate) {
			return errors.New("私网目标须显式允许；不支持未指定或组播地址")
		}
		return nil
	}
	if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, ":/ \\@") {
		return errors.New("目标地址须为 IPv4 或域名，不含协议与端口")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("目标域名无效")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return errors.New("目标域名无效")
			}
		}
	}
	return nil
}

// 对解析后的具体地址检查，防止域名绕过私网许可或在拨号时被再次解析。
func AllowedIP(ip netip.Addr, allowPrivate bool) bool {
	if !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.MustParseAddr("255.255.255.255") {
		return false
	}
	return allowPrivate || (ip.IsGlobalUnicast() && !ip.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip))
}

func (r Rule) Config() *controlpb.ForwardRule {
	kind := controlpb.RuleKind_RULE_KIND_DIRECT
	if r.Kind == Tunnel {
		kind = controlpb.RuleKind_RULE_KIND_TUNNEL
	}
	return &controlpb.ForwardRule{ExitGroupId: r.ExitGroupID, Id: r.ID, EntryGroupId: r.EntryGroupID, Kind: kind, ListenPort: uint32(r.ListenPort), TargetAddress: r.TargetAddress, TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, TargetPort: uint32(r.TargetPort), AllowPrivate: r.AllowPrivate}
}
