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
	rows, err = tx.QueryContext(ctx, "SELECT id,desired_revision FROM nodes ORDER BY id")
	if err != nil {
		return nil, err
	}
	type nodeRevision struct {
		id       string
		revision uint64
	}
	nodes := []nodeRevision{}
	for rows.Next() {
		var n nodeRevision
		if err := rows.Scan(&n.id, &n.revision); err != nil {
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
	compiled := map[string][]*controlpb.ForwardRule{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, id := range members[r.EntryGroupID] {
			compiled[id] = append(compiled[id], r.Config())
		}
	}
	changed := []string{}
	for _, n := range nodes {
		next := &controlpb.ConfigSnapshot{Rules: compiled[n.id]}
		previous := &controlpb.ConfigSnapshot{}
		var data []byte
		err := tx.QueryRowContext(ctx, "SELECT snapshot FROM node_snapshots WHERE node_id=?", n.id).Scan(&data)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if len(data) > 0 {
			if err := proto.Unmarshal(data, previous); err != nil {
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET snapshot=excluded.snapshot,digest=excluded.digest`, n.id, data, next.Digest); err != nil {
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
	err := p.db.QueryRowContext(ctx, `SELECT n.desired_revision,s.snapshot FROM nodes n LEFT JOIN node_snapshots s ON s.node_id=n.id WHERE n.id=?`, id).Scan(&m.DesiredRevision, &data)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		m.Snapshot = &controlpb.ConfigSnapshot{}
		if err := proto.Unmarshal(data, m.Snapshot); err != nil {
			return nil, err
		}
	}
	m.RotationCommand, err = p.rotationCommand(ctx, id)
	if err != nil {
		return nil, err
	}
	return m, nil
}
