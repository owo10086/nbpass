package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// 记录类型在绑定时定下；只有主备入口组的记录可以是 CNAME（ADR 0008）。
const recordTypeMigration = `ALTER TABLE managed_records ADD COLUMN record_type TEXT NOT NULL DEFAULT 'A' CHECK(record_type IN ('A','CNAME'));`

// cnameTarget 返回 CNAME 组记录应指向的节点受管记录域名。
// strict 用于切换、回切和首次发布：节点记录须已首次发布、启用，且记录地址就是这次发布的地址；
// 换 IP 与偏差写回只要求节点记录存在，因为节点记录另由节点地址同步更新。ip 为空时不比较地址。
func cnameTarget(ctx context.Context, q recoveryQuerier, nodeID, ip string, strict bool) (string, error) {
	var name, domain, value string
	var enabled, published bool
	err := q.QueryRowContext(ctx, `SELECT m.name,m.domain,m.current_value,m.current_enabled,coalesce(s.published,0) FROM managed_records m LEFT JOIN node_publications s ON s.node_id=m.node_id WHERE m.owner_kind='node' AND m.node_id=?`, nodeID).Scan(&name, &domain, &value, &enabled, &published)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &forwardingError{409, "该成员没有绑定节点记录，CNAME 组无法指向它"}
	}
	if err != nil {
		return "", err
	}
	if strict {
		switch {
		case !published:
			return "", &forwardingError{409, "该成员的节点记录尚未首次发布，CNAME 组无法指向它"}
		case !enabled:
			return "", &forwardingError{409, "该成员的节点记录已暂停，CNAME 组无法指向它"}
		case ip != "" && value != ip:
			return "", &forwardingError{409, "该成员的节点记录地址（" + value + "）与发布地址（" + ip + "）不一致，请先同步节点地址"}
		}
	}
	return recordHostname(name, domain), nil
}

func recordHostname(name, domain string) string {
	if name == "@" || name == "" {
		return domain
	}
	return name + "." + domain
}

// strictCNAMEKind 是会改变活动入口的动作，须按 cnameTarget 的严格条件核对目标。
func strictCNAMEKind(kind string) bool {
	return kind == "first_publish" || kind == "manual_switch" || kind == "switch" || kind == "failback"
}

// protectActiveCNAMENode 拒绝解绑或更换 CNAME 组活动入口所在节点的节点记录，组记录还指向它。
func protectActiveCNAMENode(ctx context.Context, q recoveryQuerier, nodeID string) error {
	var active string
	err := q.QueryRowContext(ctx, `SELECT g.name FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id JOIN managed_records m ON m.group_id=s.group_id AND m.owner_kind='group' WHERE s.active_node_id=? AND s.desired_version>0 AND m.record_type='CNAME' LIMIT 1`, nodeID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &forwardingError{409, "该节点是 CNAME 入口组“" + active + "”的活动入口，组记录指向这条节点记录；请先把入口组切走"}
}

// fillCNAMEAddress 在 CNAME 组未填地址时取目标节点记录的当前地址，作为期望解析结果；A 组保持原样。
func (p *Panel) fillCNAMEAddress(w http.ResponseWriter, r *http.Request, nodeID string, ipv4 *string) bool {
	if *ipv4 != "" {
		return true
	}
	err := p.db.QueryRowContext(r.Context(), `SELECT coalesce((SELECT n.current_value FROM managed_records n WHERE n.owner_kind='node' AND n.node_id=? AND EXISTS(SELECT 1 FROM managed_records g WHERE g.owner_kind='group' AND g.group_id=? AND g.record_type='CNAME')),'')`, nodeID, r.PathValue("id")).Scan(ipv4)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return false
	}
	return true
}
