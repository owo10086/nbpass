package main

import (
	"flag"
	"time"

	"nbpass/internal/forwarding"
)

// 保留票 07 的 direct-* 参数兼容；同一字段的通用名称用于全部业务路径。
func tunnelResourceFlags(fs *flag.FlagSet, l *forwarding.ResourceLimits) {
	fs.IntVar(&l.MaxConnections, "max-connections", 20000, "每节点直连与隧道共享的业务连接上限（含待拨号）")
	fs.IntVar(&l.MaxConnectionsPerRule, "max-connections-per-rule", 5000, "每规则业务连接上限（兼任入口/出口只计一次）")
	fs.IntVar(&l.MaxPendingDials, "max-pending-dials", 512, "每节点待拨号上限（含隧道握手与域名解析）")
	fs.DurationVar(&l.DialTimeout, "dial-timeout", 10*time.Second, "业务连接从握手到目标拨号的总期限")
	fs.DurationVar(&l.IdleTimeout, "idle-timeout", 2*time.Hour, "业务连接双向均无数据时的空闲期限")
	fs.IntVar(&l.MaxTunnelStreams, "tunnel-max-streams", 1024, "每个 yamux 会话的并发流上限")
	fs.DurationVar(&l.TunnelHandshakeTimeout, "tunnel-handshake-timeout", 3*time.Second, "隧道 TLS/WSS/认证与逐流请求握手期限")
}
