package panel

import (
	"context"
	"database/sql"

	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 仅保留升级前已经同时下发双端、授权内容未变化的零代次规则。
// 删除、停用或修改授权后，下一份完整快照不再包含它，不能借兼容路径复活。
func (p *Panel) legacyTunnelAuthorizations(ctx context.Context, tx *sql.Tx, rules []forwarding.Rule) (map[[3]string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT node_id,snapshot,encrypted FROM node_snapshots
 WHERE node_id IN (SELECT node_id FROM tunnel_exits UNION SELECT node_id FROM entry_group_members)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	snapshots := map[string]*controlpb.ConfigSnapshot{}
	for rows.Next() {
		var id string
		var data []byte
		var encrypted bool
		if err := rows.Scan(&id, &data, &encrypted); err != nil {
			return nil, err
		}
		snapshot := &controlpb.ConfigSnapshot{}
		if err := p.decodeSnapshot(id, data, encrypted, snapshot); err != nil {
			return nil, err
		}
		snapshots[id] = snapshot
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	desired := map[string]*controlpb.ForwardRule{}
	for _, r := range rules {
		if r.Enabled && r.Kind == forwarding.Tunnel {
			desired[r.ID] = r.Config()
		}
	}
	legacy := map[[3]string]bool{}
	for exit, snapshot := range snapshots {
		for _, grant := range snapshot.GetTunnelServer().GetGrants() {
			if !sameLegacyAuthorization(grant.Rule, desired[grant.Rule.GetId()]) {
				continue
			}
			for _, r := range snapshots[grant.EntryId].GetRules() {
				if sameLegacyAuthorization(r, grant.Rule) {
					legacy[[3]string{exit, grant.EntryId, r.Id}] = true
					break
				}
			}
		}
	}
	return legacy, nil
}

func sameLegacyAuthorization(a, b *controlpb.ForwardRule) bool {
	return a != nil && b != nil && a.AuthorizationGeneration == 0 && b.AuthorizationGeneration == 0 &&
		a.Id == b.Id && a.EntryGroupId == b.EntryGroupId && a.ExitGroupId == b.ExitGroupId && a.Kind == b.Kind &&
		a.TargetAddress == b.TargetAddress && a.TargetFamily == b.TargetFamily && a.TargetPort == b.TargetPort && a.AllowPrivate == b.AllowPrivate
}
