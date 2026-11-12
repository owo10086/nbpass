package panel

import (
	"context"
	"database/sql"
	"errors"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
	"net/http"
	"strings"
)

// 迁移 49，追加在成员地址迁移 48 之后。
const outboundIPv4Migration = `ALTER TABLE nodes ADD COLUMN outbound_ipv4 TEXT NOT NULL DEFAULT '';`

type outboundIPv4Config struct {
	OutboundIPv4 string `json:"outbound_ipv4"`
}

func (p *Panel) getOutboundIPv4(w http.ResponseWriter, r *http.Request) {
	var config outboundIPv4Config
	err := p.db.QueryRowContext(r.Context(), "SELECT outbound_ipv4 FROM nodes WHERE id=?", r.PathValue("id")).Scan(&config.OutboundIPv4)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, config)
}
func (p *Panel) putOutboundIPv4(w http.ResponseWriter, r *http.Request) {
	var config outboundIPv4Config
	if !decode(w, r, &config) {
		return
	}
	config.OutboundIPv4 = strings.TrimSpace(config.OutboundIPv4)
	if err := forwarding.ValidateOutboundIPv4(config.OutboundIPv4); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(r.Context(), "UPDATE nodes SET outbound_ipv4=? WHERE id=?", config.OutboundIPv4, r.PathValue("id"))
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			return &forwardingError{404, "节点不存在"}
		}
		return nil
	}) {
		return
	}
	respond(w, 200, config)
}
func loadOutboundIPv4(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT id,outbound_ipv4 FROM nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, ip string
		if err := rows.Scan(&id, &ip); err != nil {
			return nil, err
		}
		out[id] = ip
	}
	return out, rows.Err()
}

// 单跳出口本机源错误属于出口配置；只投影到引用它的端口，不冒充入口链路故障。
func (p *Panel) attachOutboundSync(ctx context.Context, views []ruleView, nodes map[string]*syncNode, plans map[[2]string]capabilityPlan) error {
	groups, err := loadExitGroups(ctx, p.db)
	if err != nil {
		return err
	}
	byID := map[string]exitGroupView{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	sources, err := loadOutboundIPv4(ctx, p.db)
	if err != nil {
		return err
	}
	for i := range views {
		v := &views[i]
		if !v.Enabled || v.Kind != forwarding.Tunnel {
			continue
		}
		for j := range v.SyncMembers {
			m := &v.SyncMembers[j]
			plan := plans[[2]string{m.NodeID, v.ID}]
			if plan.Blocked {
				continue
			}
			eligible, failed := 0, 0
			reasons := []string{}
			for _, exit := range byID[v.ExitGroupID].Members {
				if !plan.allows(exit) {
					continue
				}
				eligible++
				n := nodes[exit]
				if n == nil || sources[exit] == "" || !n.connected || n.reported != n.desired || n.reportedDigest != n.digest || !strings.Contains(n.applyError, "出站 IP ") {
					continue
				}
				failed++
				reasons = append(reasons, "出口服务器“"+n.name+"”出站 IP 应用失败: "+n.applyError)
			}
			if len(reasons) > 0 {
				m.ApplyError = strings.Join(reasons, "；")
				if failed == eligible && m.Connected {
					m.SyncStatus = "同步失败"
				}
			}
		}
		statuses := []string{}
		for _, m := range v.SyncMembers {
			statuses = append(statuses, m.SyncStatus)
		}
		v.SyncStatus = aggregateSync(statuses)
	}
	return nil
}

// 当前节点单个控制流串行发送配置结果；同流中的真实重新核验结果可撤回
// 完整确认。普通/旧节点仍保留迟到失败保护，旧会话由 validControl 拒绝。
func (p *Panel) outboundRevalidationFailure(ctx context.Context, id string, session *controlSession, report *controlpb.NodeReport, desired uint64, digest, source string) (bool, error) {
	if desired == 0 || digest == "" || source == "" || session.compatibility == nil || !session.compatibility.supports(compatibility.OutboundIPv4) || report.AttemptedRevision != desired || report.AttemptedDigest != digest || !report.RevocationsApplied || report.AppliedRevision != 0 || report.AppliedDigest != "" || !strings.Contains(report.ApplyError, "出站 IP "+source+" ") {
		return false, nil
	}
	var data []byte
	var encrypted bool
	if err := p.db.QueryRowContext(ctx, "SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", id).Scan(&data, &encrypted); err != nil {
		return false, err
	}
	snapshot := &controlpb.ConfigSnapshot{}
	if err := p.decodeSnapshot(id, data, encrypted, snapshot); err != nil {
		return false, err
	}
	return snapshot.OutboundIpv4 == source && snapshot.Revision == desired && snapshot.Digest == digest, nil
}
