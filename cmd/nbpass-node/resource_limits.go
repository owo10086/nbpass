package main

import (
	"flag"
	"time"

	"nbpass/internal/forwarding"
)

func directResourceFlags(fs *flag.FlagSet) *forwarding.ResourceLimits {
	l := &forwarding.ResourceLimits{}
	fs.IntVar(&l.MaxConnections, "direct-max-connections", 20000, "每节点直连业务连接上限（含待拨号，零值使用默认值）")
	fs.IntVar(&l.MaxConnectionsPerRule, "direct-max-connections-per-rule", 5000, "每条直连规则的业务连接上限")
	fs.IntVar(&l.MaxPendingDials, "direct-max-pending-dials", 512, "每节点直连待拨号上限（含域名解析）")
	fs.DurationVar(&l.DialTimeout, "direct-dial-timeout", 10*time.Second, "直连域名解析与候选拨号的总期限")
	fs.DurationVar(&l.IdleTimeout, "direct-idle-timeout", 2*time.Hour, "直连双向均无数据时的空闲期限")
	tunnelResourceFlags(fs, l)
	return l
}
