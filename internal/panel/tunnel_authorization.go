package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 迁移 14；授权代次独立于节点 revision，停用/改为直连后仍保留高水位。
const tunnelAuthorizationMigration = `ALTER TABLE nodes ADD COLUMN tunnel_authorization_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE rule_authorizations (
 rule_id TEXT PRIMARY KEY REFERENCES forward_rules(id) ON DELETE CASCADE,
 generation INTEGER NOT NULL CHECK(generation>0),
 fingerprint TEXT NOT NULL
);
CREATE TABLE tunnel_authorization_confirmations (
 exit_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 entry_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 rule_id TEXT NOT NULL REFERENCES forward_rules(id) ON DELETE CASCADE,
 generation INTEGER NOT NULL CHECK(generation>0),
 PRIMARY KEY(exit_id,entry_id,rule_id)
);`

func authorizationFingerprint(r forwarding.Rule, entries, exits []string) string {
	entries, exits = append([]string{}, entries...), append([]string{}, exits...)
	sort.Strings(entries)
	sort.Strings(exits)
	data, _ := json.Marshal(struct {
		Kind                          forwarding.RuleKind
		Enabled                       bool
		EntryGroup, ExitGroup, Target string
		Family                        forwarding.AddressFamily
		Port                          int
		AllowPrivate                  bool
		Entries, Exits                []string
		EntryNode                     string `json:",omitempty"`
	}{r.Kind, r.Enabled, r.EntryGroupID, r.ExitGroupID, r.TargetAddress, r.TargetFamily, r.TargetPort, r.AllowPrivate, entries, exits, r.EntryNodeID})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func updateAuthorizationGeneration(ctx context.Context, tx *sql.Tx, r forwarding.Rule, entries, exits []string) (uint64, error) {
	fingerprint := authorizationFingerprint(r, entries, exits)
	if r.ChainID != "" {
		var err error
		fingerprint, err = chainAuthorizationFingerprint(ctx, tx, r, entries)
		if err != nil {
			return 0, err
		}
	}
	var generation uint64
	var previous string
	err := tx.QueryRowContext(ctx, "SELECT generation,fingerprint FROM rule_authorizations WHERE rule_id=?", r.ID).Scan(&generation, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if errors.Is(err, sql.ErrNoRows) && r.Kind != forwarding.Tunnel {
		return 0, nil
	}
	if previous != fingerprint {
		generation++
		if _, err := tx.ExecContext(ctx, `INSERT INTO rule_authorizations(rule_id,generation,fingerprint) VALUES(?,?,?)
 ON CONFLICT(rule_id) DO UPDATE SET generation=excluded.generation,fingerprint=excluded.fingerprint`, r.ID, generation, fingerprint); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM tunnel_authorization_confirmations WHERE rule_id=?", r.ID); err != nil {
			return 0, err
		}
	}
	return generation, nil
}

func authorizationCapabilities(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,tunnel_authorization_version FROM nodes")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	capable := map[string]bool{}
	for rows.Next() {
		var id string
		var version uint32
		if err := rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		capable[id] = version == 1
	}
	return capable, rows.Err()
}

// 单成员出口组：未确认的规则不下发入口监听。出口确认后才生成含规则的新入口快照。
// 后续多出口票可按同一确认表过滤每条规则的候选，不能按整个出口组共享确认。
func gateTunnelRules(ctx context.Context, tx *sql.Tx, compiled map[string]*controlpb.ConfigSnapshot, capable map[string]bool, generations map[string]uint64, groups map[string]exitGroupView, legacyReady map[[2]string]bool, plans map[[2]string]capabilityPlan) error {
	rows, err := tx.QueryContext(ctx, "SELECT exit_id,entry_id,rule_id,generation FROM tunnel_authorization_confirmations")
	if err != nil {
		return err
	}
	confirmed := map[[3]string]uint64{}
	for rows.Next() {
		var exit, entry, rule string
		var generation uint64
		if err := rows.Scan(&exit, &entry, &rule, &generation); err != nil {
			rows.Close()
			return err
		}
		confirmed[[3]string{exit, entry, rule}] = generation
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for entry, snapshot := range compiled {
		var ready []*controlpb.ForwardRule
		for _, r := range snapshot.Rules {
			if r.Kind != controlpb.RuleKind_RULE_KIND_TUNNEL {
				ready = append(ready, r)
				continue
			}
			// Chain candidates have passed recursive durable grant confirmation.
			if r.ChainId != "" {
				if len(r.AuthorizedExitIds) > 0 {
					ready = append(ready, r)
				}
				continue
			}
			if legacyReady[[2]string{entry, r.Id}] {
				ready = append(ready, r)
				continue
			}
			r.AuthorizationGeneration = generations[r.Id]
			for _, exit := range groups[r.ExitGroupId].Members {
				if !plans[[2]string{entry, r.Id}].allows(exit) {
					continue
				}
				if capable[entry] && capable[exit] && confirmed[[3]string{exit, entry, r.Id}] == r.AuthorizationGeneration {
					r.AuthorizedExitIds = append(r.AuthorizedExitIds, exit)
				}
			}
			if len(r.AuthorizedExitIds) > 0 {
				ready = append(ready, r)
			}
		}
		snapshot.Rules = ready
	}
	return nil
}

// 仅当前会话的独立配置报告可确认；发送成功、探针、事件、活性均不构成确认。
// 完整成功意味着出口监听、授权及缓存都已应用。其他规则部分失败时保守等待完整确认。
func (p *Panel) confirmTunnelAuthorizations(ctx context.Context, tx *sql.Tx, id string, report *controlpb.NodeReport, desired uint64, digest string) ([]string, error) {
	chainChanged, err := p.confirmChainGrants(ctx, tx, id, report, desired, digest)
	if err != nil {
		return nil, err
	}
	var chainChanges []string
	if chainChanged {
		chainChanges, err = p.compileSnapshots(ctx, tx)
		if err != nil {
			return nil, err
		}
	}
	if desired == 0 || digest == "" || report.AttemptedRevision != desired || report.AttemptedDigest != digest || report.AppliedRevision != desired || report.AppliedDigest != digest || report.ApplyError != "" || !report.RevocationsApplied {
		return chainChanges, nil
	}
	var version uint32
	if err := tx.QueryRowContext(ctx, "SELECT tunnel_authorization_version FROM nodes WHERE id=?", id).Scan(&version); err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, nil
	}
	var data []byte
	var encrypted bool
	if err := tx.QueryRowContext(ctx, "SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", id).Scan(&data, &encrypted); err != nil {
		return nil, err
	}
	s := &controlpb.ConfigSnapshot{}
	if err := p.decodeSnapshot(id, data, encrypted, s); err != nil {
		return nil, err
	}
	changed := false
	for _, grant := range s.GetTunnelServer().GetGrants() {
		r := grant.Rule
		if r.GetAuthorizationGeneration() == 0 || r.GetChainId() != "" {
			continue
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO tunnel_authorization_confirmations(exit_id,entry_id,rule_id,generation) VALUES(?,?,?,?)
 ON CONFLICT(exit_id,entry_id,rule_id) DO UPDATE SET generation=excluded.generation WHERE generation<>excluded.generation`, id, grant.EntryId, r.Id, r.AuthorizationGeneration)
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		changed = changed || n > 0
	}
	if !changed {
		return chainChanges, nil
	}
	// 确认和入口快照在同一事务提交，不会留下“确认已存、入口永远未启用”的崩溃窗口。
	changes, err := p.compileSnapshots(ctx, tx)
	return append(chainChanges, changes...), err
}

func (p *Panel) notifyConfiguration(changed []string) {
	for _, id := range changed {
		if session := p.controls[id]; session != nil {
			select {
			case session.changed <- struct{}{}:
			default:
			}
		}
	}
}

func (p *Panel) admitTunnelAuthorization(ctx context.Context, tx *sql.Tx, id string, newInstance bool, version uint32, otherCapabilityChanged ...bool) ([]string, error) {
	var previous uint32
	if err := tx.QueryRowContext(ctx, "SELECT tunnel_authorization_version FROM nodes WHERE id=?", id).Scan(&previous); err != nil {
		return nil, err
	}
	force := len(otherCapabilityChanged) > 0 && otherCapabilityChanged[0]
	if !newInstance && previous == version && !force {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE nodes SET tunnel_authorization_version=? WHERE id=?", version, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM node_tunnel_grant_results WHERE node_id=?", id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tunnel_authorization_confirmations WHERE exit_id=?", id); err != nil {
		return nil, err
	}
	if previous != version || force {
		if _, err := tx.ExecContext(ctx, "DELETE FROM node_tunnel_grant_results WHERE entry_id=?", id); err != nil {
			return nil, err
		}
		// 入口降级会撤掉其他出口上的 grants；再次升级不能复用撤销前的确认。
		if _, err := tx.ExecContext(ctx, "DELETE FROM tunnel_authorization_confirmations WHERE entry_id=?", id); err != nil {
			return nil, err
		}
		// A capability transition needs a new durable grant even when its rule
		// content is unchanged. Otherwise the successor would never receive a
		// changed snapshot and could reuse an old acknowledgement after upgrade.
		// Only this node's predecessor role needs a new remote grant. Its
		// successor role already receives a changed snapshot when becoming
		// eligible; invalidating the whole rule there would revoke healthy peers.
		// Chain consumers extend predecessor references for ordered hops.
		if _, err := tx.ExecContext(ctx, `UPDATE rule_authorizations SET fingerprint='' WHERE rule_id IN (
 SELECT r.id FROM forward_rules r WHERE r.entry_node_id=?
 OR EXISTS(SELECT 1 FROM entry_group_members m WHERE m.group_id=r.entry_group_id AND m.node_id=?)
 OR EXISTS(SELECT 1 FROM forward_chain_hops h JOIN exit_group_members m ON m.group_id=h.exit_group_id WHERE h.chain_id=r.chain_id AND m.node_id=?))`, id, id, id); err != nil {
			return nil, err
		}
	}
	var tunnels int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tunnel_exits").Scan(&tunnels); err != nil {
		return nil, err
	}
	if tunnels == 0 && !force {
		return nil, nil
	}
	return p.compileSnapshots(ctx, tx)
}
