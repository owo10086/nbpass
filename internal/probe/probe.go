// Package probe 定义节点资源采集边界。缺少的字段表示缺测，不能解释成零。
package probe

import (
	"context"
	"math"
)

const IntervalSeconds = 5

// Values 的速率单位为字节/秒，容量为字节，CPU 为百分比。
// 连接数分别为逻辑业务连接、物理隧道连接和系统 ESTABLISHED TCP 连接。
type Values map[string]float64

type Collector interface {
	Collect(context.Context) (Values, error)
}

// Connections 供转发引擎提供连接数；不从系统 TCP 总数推算业务与隧道数量。
type Connections interface {
	Connections() (business, tunnel uint64)
}

var names = []string{
	"cpu_percent", "memory_used_bytes", "memory_total_bytes", "disk_used_bytes", "disk_total_bytes",
	"network_receive_bytes_per_second", "network_transmit_bytes_per_second",
	"business_connections", "tunnel_connections", "system_tcp_connections", "uptime_seconds",
}

// Clean 丢弃未知、负数、非有限数；坏字段不会使其余指标或控制活性失效。
func Clean(values Values) Values {
	result := Values{}
	for _, name := range names {
		if value, ok := values[name]; ok && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0) && (name != "cpu_percent" || value <= 100) {
			result[name] = value
		}
	}
	return result
}

func Nullable(values Values) map[string]*float64 {
	result := make(map[string]*float64, len(names))
	for _, name := range names {
		result[name] = nil
		if value, ok := values[name]; ok {
			result[name] = &value
		}
	}
	return result
}
