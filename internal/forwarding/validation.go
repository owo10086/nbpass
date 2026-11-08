package forwarding

import (
	"fmt"
	"net/netip"
	"strings"
)

// 服务尚未启用时也预留端口，后续隧道与检测执行器复用同一模型。
const (
	DefaultTunnelListenPort = 18443
	DefaultCheckPort        = 18080
)

type NodeListenerPorts struct {
	TunnelListenPort int `json:"tunnel_listen_port"`
	CheckPort        int `json:"check_port"`
}

func (p NodeListenerPorts) Valid() bool {
	return p.TunnelListenPort >= 1 && p.TunnelListenPort <= 65535 && p.CheckPort >= 1 && p.CheckPort <= 65535
}

type ValidationNode struct {
	ID, Name string
	// 仅用于保守识别指回节点的目标，不作为已验证的业务发布地址。
	ObservedIPv4 string
	NodeListenerPorts
}

type ListenerReference struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Port     int    `json:"port"`
	Kind     string `json:"kind"`
	RuleID   string `json:"rule_id,omitempty"`
	RuleName string `json:"rule_name,omitempty"`
}

func (r ListenerReference) String() string {
	owner := map[string]string{"tunnel": "隧道监听", "check": "检测端口", "rule": "规则「" + r.RuleName + "」（" + r.RuleID + "）"}[r.Kind]
	return fmt.Sprintf("节点「%s」（%s）端口 %d 的%s", r.NodeName, r.NodeID, r.Port, owner)
}

type ValidationError struct {
	Code      string              `json:"code"`
	Message   string              `json:"error"`
	Listeners []ListenerReference `json:"listeners"`
}

func (e *ValidationError) Error() string { return e.Message }

// ValidateConfiguration 检查所有成员上的实际启用监听，并在“节点 × 规则”图中找环。
// targets 为当前能确认的 A 记录；域名解析失败或未来 DNS 变化不构成静态证明。
func ValidateConfiguration(nodes []ValidationNode, rules []Rule, members map[string][]string, targets map[string][]netip.Addr) error {
	type endpoint struct {
		ref  ListenerReference
		rule Rule
	}
	byNode := map[string]ValidationNode{}
	occupied := map[string]map[int]ListenerReference{}
	var endpoints []endpoint
	reserve := func(ref ListenerReference) error {
		if previous, ok := occupied[ref.NodeID][ref.Port]; ok {
			return &ValidationError{Code: "port_conflict", Message: "监听端口冲突：" + ref.String() + "与" + previous.String() + "冲突", Listeners: []ListenerReference{previous, ref}}
		}
		occupied[ref.NodeID][ref.Port] = ref
		return nil
	}
	for _, n := range nodes {
		byNode[n.ID] = n
		occupied[n.ID] = map[int]ListenerReference{}
		for _, service := range []struct {
			kind string
			port int
		}{{"tunnel", n.TunnelListenPort}, {"check", n.CheckPort}} {
			if err := reserve(ListenerReference{NodeID: n.ID, NodeName: n.Name, Port: service.port, Kind: service.kind}); err != nil {
				return err
			}
		}
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, id := range members[r.EntryGroupID] {
			n := byNode[id]
			ref := ListenerReference{NodeID: id, NodeName: n.Name, Port: r.ListenPort, Kind: "rule", RuleID: r.ID, RuleName: r.Name}
			if err := reserve(ref); err != nil {
				return err
			}
			endpoints = append(endpoints, endpoint{ref: ref, rule: r})
		}
	}
	byPort := map[int][]int{}
	for i, e := range endpoints {
		byPort[e.ref.Port] = append(byPort[e.ref.Port], i)
	}
	edges := make([][]int, len(endpoints))
	for i, e := range endpoints {
		ips := targets[e.rule.TargetAddress]
		if ip, err := netip.ParseAddr(e.rule.TargetAddress); err == nil {
			ips = []netip.Addr{ip}
		} else if e.rule.TargetAddress == "localhost" {
			ips = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
		}
		for _, j := range byPort[e.rule.TargetPort] {
			destination := endpoints[j]
			for _, ip := range ips {
				ip = ip.Unmap()
				if !AllowedIP(ip, e.rule.AllowPrivate) {
					continue
				}
				if ip.IsLoopback() && e.ref.NodeID == destination.ref.NodeID || ip.String() == byNode[destination.ref.NodeID].ObservedIPv4 {
					edges[i] = append(edges[i], j)
					break
				}
			}
		}
	}
	state := make([]uint8, len(endpoints))
	var stack []int
	var visit func(int) error
	visit = func(i int) error {
		if state[i] == 2 {
			return nil
		}
		if state[i] == 1 {
			start := 0
			for stack[start] != i {
				start++
			}
			path := append(append([]int{}, stack[start:]...), i)
			refs := make([]ListenerReference, 0, len(path))
			labels := make([]string, 0, len(path))
			for _, index := range path {
				refs = append(refs, endpoints[index].ref)
				labels = append(labels, endpoints[index].ref.String())
			}
			return &ValidationError{Code: "forwarding_loop", Message: "配置会形成转发循环：" + strings.Join(labels, " → "), Listeners: refs}
		}
		state[i] = 1
		stack = append(stack, i)
		for _, j := range edges[i] {
			if err := visit(j); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		state[i] = 2
		return nil
	}
	for i := range endpoints {
		if err := visit(i); err != nil {
			return err
		}
	}
	return nil
}
