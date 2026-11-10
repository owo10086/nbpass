package panel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"math/big"
	"nbpass/internal/forwarding"
)

// 在创建事务内对实际入口的配置占用取并集，规则只保存一个具体端口。
// 面板无法预知节点上其他进程的 bind 竞争，实际监听失败仍由节点逐规则上报。
func (p *Panel) randomRulePort(ctx context.Context, tx *sql.Tx, rule forwarding.Rule) (int, error) {
	occupied := make([]bool, 65536)
	members, err := loadEntryGroupMembers(ctx, tx)
	if err != nil {
		return 0, err
	}
	requested := map[string]bool{}
	for _, id := range forwarding.RuleEntryNodes(rule, members) {
		requested[id] = true
		var tunnel, check, active int
		if err := tx.QueryRowContext(ctx, `SELECT n.tunnel_listen_port,n.check_port,coalesce(t.listen_port,0) FROM nodes n LEFT JOIN tunnel_exits t ON t.node_id=n.id WHERE n.id=?`, id).Scan(&tunnel, &check, &active); err != nil {
			return 0, err
		}
		occupied[tunnel], occupied[check], occupied[active] = true, true, true
	}
	if len(requested) == 0 {
		return 0, &forwardingError{400, "入口没有节点，无法选择随机端口"}
	}
	rules, err := loadRules(ctx, tx)
	if err != nil {
		return 0, err
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, id := range forwarding.RuleEntryNodes(r, members) {
			if requested[id] {
				occupied[r.ListenPort] = true
				break
			}
		}
	}
	available := make([]int, 0, 65535-1024+1)
	for port := 1024; port <= 65535; port++ {
		if !occupied[port] {
			available = append(available, port)
		}
	}
	if len(available) == 0 {
		return 0, &forwardingError{409, "实际入口节点没有共同空闲的随机端口（1024–65535）"}
	}
	index, err := rand.Int(p.deps.Random, big.NewInt(int64(len(available))))
	if err != nil {
		return 0, err
	}
	return available[index.Int64()], nil
}
