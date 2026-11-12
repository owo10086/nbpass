package forwarding

import (
	"fmt"
	"net/netip"
	"sort"
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
	// 出口可通过独立管理 API 配置端口；同时保留节点参数中的预留端口约束。
	ActiveTunnelListenPort int
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

// ValidateConfiguration 保留单次保存的首个错误契约。
func ValidateConfiguration(nodes []ValidationNode, rules []Rule, members map[string][]string, targets map[string][]netip.Addr) error {
	failures := ConfigurationErrors(nodes, rules, members, targets)
	if len(failures) > 0 {
		return failures[0]
	}
	return nil
}

// ConfigurationErrors 一次收集端口冲突和所有强连通分量中的环路参与者。
// targets 为当前能确认的 A 记录；域名解析失败或未来变化不构成静态证明。
func ConfigurationErrors(nodes []ValidationNode, rules []Rule, members map[string][]string, targets map[string][]netip.Addr, targetDialers ...map[string][]string) []*ValidationError {
	failures := ListenerConflicts(nodes, rules, members)
	invalid := map[string]bool{}
	for _, failure := range failures {
		for _, listener := range failure.Listeners {
			if listener.Kind == "rule" {
				invalid[listener.RuleID] = true
			}
		}
	}
	type endpoint struct {
		ref  ListenerReference
		rule Rule
	}
	byNode := map[string]ValidationNode{}
	var endpoints []endpoint
	for _, n := range nodes {
		byNode[n.ID] = n
	}
	for _, r := range rules {
		if !r.Enabled || invalid[r.ID] {
			continue
		}
		for _, id := range RuleEntryNodes(r, members) {
			n := byNode[id]
			ref := ListenerReference{NodeID: id, NodeName: n.Name, Port: r.ListenPort, Kind: "rule", RuleID: r.ID, RuleName: r.Name}
			endpoints = append(endpoints, endpoint{ref: ref, rule: r})
		}
	}
	byPort := map[int][]int{}
	for i, e := range endpoints {
		byPort[e.ref.Port] = append(byPort[e.ref.Port], i)
	}
	edges := make([][]int, len(endpoints))
	for i, e := range endpoints {
		dialers := []string{e.ref.NodeID}
		if e.rule.Kind == Tunnel && len(targetDialers) > 0 {
			dialers = targetDialers[0][e.rule.ID]
		}
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
				local := false
				for _, dialer := range dialers {
					if dialer == destination.ref.NodeID {
						local = true
						break
					}
				}
				if ip.IsLoopback() && local || !ip.IsLoopback() && ip.String() == byNode[destination.ref.NodeID].ObservedIPv4 {
					edges[i] = append(edges[i], j)
					break
				}
			}
		}
	}
	indices, low := make([]int, len(endpoints)), make([]int, len(endpoints))
	for i := range indices {
		indices[i] = -1
	}
	onStack := make([]bool, len(endpoints))
	stack := []int{}
	next := 0
	var visit func(int)
	visit = func(i int) {
		indices[i], low[i] = next, next
		next++
		stack = append(stack, i)
		onStack[i] = true
		for _, j := range edges[i] {
			if indices[j] == -1 {
				visit(j)
				low[i] = min(low[i], low[j])
			} else if onStack[j] {
				low[i] = min(low[i], indices[j])
			}
		}
		if low[i] != indices[i] {
			return
		}
		component := []int{}
		for {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[j] = false
			component = append(component, j)
			if j == i {
				break
			}
		}
		cyclic := len(component) > 1
		if !cyclic {
			for _, j := range edges[i] {
				cyclic = cyclic || j == i
			}
		}
		if !cyclic {
			return
		}
		sort.Ints(component)
		refs := make([]ListenerReference, 0, len(component))
		for _, j := range component {
			refs = append(refs, endpoints[j].ref)
		}
		failures = append(failures, &ValidationError{Code: "forwarding_loop", Message: validationSummary("配置会形成转发循环", refs), Listeners: refs})
	}
	for i := range endpoints {
		if indices[i] == -1 {
			visit(i)
		}
	}
	return failures
}

// 摘要有界，全部参与者保留在结构化引用中，避免重复拼接长名单。
func validationSummary(label string, refs []ListenerReference) string {
	labels := []string{}
	for _, ref := range refs[:min(2, len(refs))] {
		labels = append(labels, ref.String())
	}
	message := label + "：" + strings.Join(labels, "与")
	if len(refs) > 2 {
		message += fmt.Sprintf("（共 %d 处监听）", len(refs))
	}
	return message
}

// ListenerConflicts 保留每个冲突端点上的全部占用者，供批量预检逐条归属原因。
// 单条保存也使用此结果，避免导入与普通保存产生不同的端口语义。
func ListenerConflicts(nodes []ValidationNode, rules []Rule, members map[string][]string) []*ValidationError {
	type endpoint struct {
		node string
		port int
	}
	occupied := map[endpoint][]ListenerReference{}
	order := []endpoint{}
	reserve := func(ref ListenerReference) {
		key := endpoint{ref.NodeID, ref.Port}
		if len(occupied[key]) == 0 {
			order = append(order, key)
		}
		occupied[key] = append(occupied[key], ref)
	}
	byID := map[string]ValidationNode{}
	for _, n := range nodes {
		byID[n.ID] = n
		reserve(ListenerReference{NodeID: n.ID, NodeName: n.Name, Port: n.TunnelListenPort, Kind: "tunnel"})
		reserve(ListenerReference{NodeID: n.ID, NodeName: n.Name, Port: n.CheckPort, Kind: "check"})
		if n.ActiveTunnelListenPort != 0 && n.ActiveTunnelListenPort != n.TunnelListenPort {
			reserve(ListenerReference{NodeID: n.ID, NodeName: n.Name, Port: n.ActiveTunnelListenPort, Kind: "tunnel"})
		}
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		for _, id := range RuleEntryNodes(rule, members) {
			reserve(ListenerReference{NodeID: id, NodeName: byID[id].Name, Port: rule.ListenPort, Kind: "rule", RuleID: rule.ID, RuleName: rule.Name})
		}
	}
	conflicts := []*ValidationError{}
	for _, key := range order {
		refs := occupied[key]
		if len(refs) < 2 {
			continue
		}
		conflicts = append(conflicts, &ValidationError{Code: "port_conflict", Message: validationSummary("监听端口冲突", refs), Listeners: refs})
	}
	return conflicts
}
