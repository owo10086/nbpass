package panel

import (
	"context"
	"crypto/rand"
	"database/sql"
	"math/big"
)

// 在创建事务内对所有成员的配置占用取并集，规则只保存一个具体端口。
// 面板无法预知节点上其他进程的 bind 竞争，实际监听失败仍由节点逐规则上报。
func (p *Panel) randomRulePort(ctx context.Context, tx *sql.Tx, group string) (int, error) {
	occupied := make([]bool, 65536)
	rows, err := tx.QueryContext(ctx, `SELECT n.tunnel_listen_port,n.check_port,coalesce(t.listen_port,0)
		FROM entry_group_members m JOIN nodes n ON n.id=m.node_id
		LEFT JOIN tunnel_exits t ON t.node_id=n.id WHERE m.group_id=?`, group)
	if err != nil {
		return 0, err
	}
	members := 0
	for rows.Next() {
		var tunnel, check, active int
		if err := rows.Scan(&tunnel, &check, &active); err != nil {
			rows.Close()
			return 0, err
		}
		occupied[tunnel], occupied[check], occupied[active] = true, true, true
		members++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if members == 0 {
		return 0, &forwardingError{400, "入口组没有成员，无法选择随机端口"}
	}
	rows, err = tx.QueryContext(ctx, `SELECT r.listen_port FROM forward_rules r
		JOIN entry_group_members owner ON owner.group_id=r.entry_group_id
		JOIN entry_group_members requested ON requested.node_id=owner.node_id
		WHERE requested.group_id=? AND r.enabled=1`, group)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			rows.Close()
			return 0, err
		}
		occupied[port] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	available := make([]int, 0, 65535-1024+1)
	for port := 1024; port <= 65535; port++ {
		if !occupied[port] {
			available = append(available, port)
		}
	}
	if len(available) == 0 {
		return 0, &forwardingError{409, "入口组所有成员没有共同空闲的随机端口（1024–65535）"}
	}
	index, err := rand.Int(p.deps.Random, big.NewInt(int64(len(available))))
	if err != nil {
		return 0, err
	}
	return available[index.Int64()], nil
}
