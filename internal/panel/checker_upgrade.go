package panel

import (
	"context"
	"database/sql"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 旧快照中缺省值表示默认18080；只有旧版已保存的非默认端口需要追加配置版本。
func (p *Panel) upgradeCheckPorts(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT n.id,n.check_port,n.desired_revision,s.snapshot,s.encrypted FROM nodes n LEFT JOIN node_snapshots s ON s.node_id=n.id WHERE n.check_port<>18080`)
	if err != nil {
		return err
	}
	type item struct {
		id        string
		port      uint32
		revision  uint64
		data      []byte
		encrypted sql.NullBool
	}
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.port, &v.revision, &v.data, &v.encrypted); err != nil {
			break
		}
		items = append(items, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		s := &controlpb.ConfigSnapshot{}
		if len(v.data) > 0 {
			if err = p.decodeSnapshot(v.id, v.data, v.encrypted.Bool, s); err != nil {
				return err
			}
		}
		if s.CheckPort == v.port {
			continue
		}
		s.CheckPort = v.port
		s.Revision = v.revision + 1
		s.Digest = forwarding.SnapshotDigest(s)
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(s)
		if err != nil {
			return err
		}
		data, err = p.secrets.(*sealedSecrets).seal("node-snapshot:"+v.id, data)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO node_snapshots(node_id,snapshot,digest,encrypted) VALUES(?,?,?,1) ON CONFLICT(node_id) DO UPDATE SET snapshot=excluded.snapshot,digest=excluded.digest,encrypted=1`, v.id, data, s.Digest); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE nodes SET desired_revision=?,attempted_revision=0,apply_error='' WHERE id=?", s.Revision, v.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
