package panel

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"

	"nbpass/internal/forwarding"
)

const listenerPortsMigration = `ALTER TABLE nodes ADD COLUMN tunnel_listen_port INTEGER NOT NULL DEFAULT 18443 CHECK(tunnel_listen_port BETWEEN 1 AND 65535);
ALTER TABLE nodes ADD COLUMN check_port INTEGER NOT NULL DEFAULT 18080 CHECK(check_port BETWEEN 1 AND 65535);`

func loadValidationNodes(ctx context.Context, tx *sql.Tx) ([]forwarding.ValidationNode, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.id,n.name,n.tunnel_listen_port,n.check_port,COALESCE(r.public_ipv4,''),COALESCE(t.listen_port,0) FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id LEFT JOIN tunnel_exits t ON t.node_id=n.id ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	nodes := []forwarding.ValidationNode{}
	for rows.Next() {
		var n forwarding.ValidationNode
		if err := rows.Scan(&n.ID, &n.Name, &n.TunnelListenPort, &n.CheckPort, &n.ObservedIPv4, &n.ActiveTunnelListenPort); err != nil {
			rows.Close()
			return nil, err
		}
		nodes = append(nodes, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return nodes, nil
}

func (p *Panel) validateConfiguration(ctx context.Context, tx *sql.Tx, rules []forwarding.Rule, members map[string][]string) error {
	failures, err := p.configurationErrors(ctx, tx, rules, members, false)
	if err != nil {
		return err
	}
	if len(failures) > 0 {
		return failures[0]
	}
	return nil
}

func (p *Panel) configurationErrors(ctx context.Context, tx *sql.Tx, rules []forwarding.Rule, members map[string][]string, all bool) ([]*forwarding.ValidationError, error) {
	nodes, err := loadValidationNodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	chains, err := loadForwardChains(ctx, tx)
	if err != nil {
		return nil, err
	}
	groups, err := loadChainGroups(ctx, tx)
	if err != nil {
		return nil, err
	}
	dialers := map[string][]string{}
	for _, r := range rules {
		if r.Kind == forwarding.Tunnel {
			hops, err := rulePathHops(r, chains, groups)
			if err != nil {
				return nil, err
			}
			for _, m := range hops[len(hops)-1].Members {
				dialers[r.ID] = append(dialers[r.ID], m.NodeID)
			}
		}
	}
	// 先做无需 DNS 的检查；冲突时不依赖外部解析服务。
	if !all {
		if failures := forwarding.ConfigurationErrors(nodes, rules, members, nil, dialers); len(failures) > 0 {
			return failures, nil
		}
	}
	ports := map[int]bool{}
	for _, r := range rules {
		if r.Enabled {
			ports[r.ListenPort] = true
		}
	}
	lookup := p.deps.LookupIPv4
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		}
	}
	// 整次编译共享解析预算；解析仅用于静态环路证明，不替代入口的目标解析。
	resolveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	targets := map[string][]netip.Addr{}
	for _, r := range rules {
		if !r.Enabled || !ports[r.TargetPort] || r.TargetAddress == "localhost" {
			continue
		}
		if _, err := netip.ParseAddr(r.TargetAddress); err == nil {
			continue
		}
		if _, ok := targets[r.TargetAddress]; ok {
			continue
		}
		targets[r.TargetAddress] = nil
		if resolveCtx.Err() == nil {
			ips, err := lookup(resolveCtx, r.TargetAddress)
			if err == nil {
				targets[r.TargetAddress] = ips
			}
		}
	}
	return forwarding.ConfigurationErrors(nodes, rules, members, targets, dialers), nil
}

func writeConfigurationError(w http.ResponseWriter, err error) bool {
	var validation *forwarding.ValidationError
	if errors.As(err, &validation) {
		respond(w, http.StatusConflict, validation)
		return true
	}
	var api *forwardingError
	if errors.As(err, &api) {
		problem(w, api.status, api.message)
		return true
	}
	return false
}

func (p *Panel) getNodeListenerPorts(w http.ResponseWriter, r *http.Request) {
	var ports forwarding.NodeListenerPorts
	err := p.db.QueryRowContext(r.Context(), "SELECT tunnel_listen_port,check_port FROM nodes WHERE id=?", r.PathValue("id")).Scan(&ports.TunnelListenPort, &ports.CheckPort)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
	} else if err != nil {
		problem(w, 503, "存储暂不可用")
	} else {
		respond(w, 200, ports)
	}
}

func (p *Panel) setNodeListenerPorts(w http.ResponseWriter, r *http.Request) {
	var ports forwarding.NodeListenerPorts
	if !decode(w, r, &ports) {
		return
	}
	if !ports.Valid() {
		problem(w, 400, "隧道监听端口和检测端口均须为 1–65535")
		return
	}
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		return missingResult(tx.ExecContext(r.Context(), "UPDATE nodes SET tunnel_listen_port=?,check_port=? WHERE id=?", ports.TunnelListenPort, ports.CheckPort, r.PathValue("id")))
	}) {
		return
	}
	respond(w, 200, ports)
}
