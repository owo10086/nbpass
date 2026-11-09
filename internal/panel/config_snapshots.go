package panel

import (
	"context"
	"database/sql"
	"errors"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// compileSnapshots 在同一个业务事务内编译；只对实际配置内容改变的节点递增版本。
// 后续票把出口授权、组和节点参数加入 ConfigSnapshot，复用这一提交边界。
func (p *Panel) compileSnapshots(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rules, err := loadRules(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT group_id,node_id FROM entry_group_members ORDER BY position")
	if err != nil {
		return nil, err
	}
	members := map[string][]string{}
	for rows.Next() {
		var group, id string
		if err := rows.Scan(&group, &id); err != nil {
			rows.Close()
			return nil, err
		}
		members[group] = append(members[group], id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := p.validateConfiguration(ctx, tx, rules, members); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT id,desired_revision,check_port FROM nodes ORDER BY id")
	if err != nil {
		return nil, err
	}
	type nodeRevision struct {
		id        string
		revision  uint64
		checkPort uint32
	}
	nodes := []nodeRevision{}
	for rows.Next() {
		var n nodeRevision
		if err := rows.Scan(&n.id, &n.revision, &n.checkPort); err != nil {
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
	compiled := map[string]*controlpb.ConfigSnapshot{}
	for _, n := range nodes {
		compiled[n.id] = &controlpb.ConfigSnapshot{CheckPort: n.checkPort}
		if n.checkPort == 18080 {
			compiled[n.id].CheckPort = 0
		}
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, id := range members[r.EntryGroupID] {
			compiled[id].Rules = append(compiled[id].Rules, r.Config())
		}
	}
	if err := p.compileTunnels(ctx, tx, rules, members, compiled); err != nil {
		return nil, err
	}
	if err := p.compileSelfChecks(ctx, tx, compiled); err != nil {
		return nil, err
	}
	if err := trackExitAddresses(ctx, tx, compiled); err != nil {
		return nil, err
	}
	changed := []string{}
	for _, n := range nodes {
		next := compiled[n.id]
		previous := &controlpb.ConfigSnapshot{}
		var data []byte
		var encrypted bool
		err := tx.QueryRowContext(ctx, "SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", n.id).Scan(&data, &encrypted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if len(data) > 0 {
			if err := p.decodeSnapshot(n.id, data, encrypted, previous); err != nil {
				return nil, err
			}
		}
		previous.Revision = 0
		previous.Digest = ""
		if proto.Equal(previous, next) {
			continue
		}
		next.Revision = n.revision + 1
		next.Digest = forwarding.SnapshotDigest(next)
		data, err = proto.MarshalOptions{Deterministic: true}.Marshal(next)
		if err != nil {
			return nil, err
		}
		// 与控制消息上限一致；拒绝过大的事务，避免保存后永远无法同步。
		if len(data) > 60<<10 {
			return nil, errors.New("节点配置快照超过控制消息上限")
		}
		data, err = p.secrets.(*sealedSecrets).seal("node-snapshot:"+n.id, data)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO node_snapshots(node_id,snapshot,digest,encrypted) VALUES(?,?,?,1) ON CONFLICT(node_id) DO UPDATE SET snapshot=excluded.snapshot,digest=excluded.digest,encrypted=1`, n.id, data, next.Digest); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE nodes SET desired_revision=?,attempted_revision=0,apply_error='' WHERE id=?", next.Revision, n.id); err != nil {
			return nil, err
		}
		changed = append(changed, n.id)
	}
	return changed, nil
}
func (p *Panel) controlMessage(ctx context.Context, id string) (*controlpb.PanelMessage, error) {
	m := &controlpb.PanelMessage{ProtocolVersion: 1}
	var data []byte
	var encrypted sql.NullBool
	err := p.db.QueryRowContext(ctx, `SELECT n.desired_revision,s.snapshot,s.encrypted FROM nodes n LEFT JOIN node_snapshots s ON s.node_id=n.id WHERE n.id=?`, id).Scan(&m.DesiredRevision, &data, &encrypted)
	if err != nil {
		return nil, err
	}
	var pending int
	if err := p.db.QueryRowContext(ctx, "SELECT count(*) FROM recovery_nodes WHERE node_id=? AND high_water IS NULL", id).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		data = nil
	}
	if len(data) > 0 {
		m.Snapshot = &controlpb.ConfigSnapshot{}
		if err := p.decodeSnapshot(id, data, encrypted.Bool, m.Snapshot); err != nil {
			return nil, err
		}
	}
	if session := p.controls[id]; session != nil && session.dnsWatchCapable {
		m.DnsWatchTasks, err = p.dnsWatchCommands(ctx, id, session.instance)
		if err != nil {
			return nil, err
		}
	}
	m.UpgradeManifest, err = p.upgradeCommand(ctx, id)
	if err != nil {
		return nil, err
	}
	m.RotationCommand, err = p.rotationCommand(ctx, id)
	if err != nil {
		return nil, err
	}
	m.CheckerTasks, err = p.checkerTasks(ctx, id)
	if err != nil {
		return nil, err
	}
	return m, nil
}
