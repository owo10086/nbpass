package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"nbpass/internal/controlpb"
)

// 迁移 19：轮换材料独立保存，完成前不覆盖当前证书或节点对凭据。
const tunnelRotationMigration = `ALTER TABLE nodes ADD COLUMN tunnel_rotation_version INTEGER NOT NULL DEFAULT 0;
CREATE TABLE tunnel_rotations (
 exit_id TEXT PRIMARY KEY REFERENCES tunnel_exits(node_id) ON DELETE CASCADE,
 id TEXT NOT NULL, kind TEXT NOT NULL, phase TEXT NOT NULL,
 started_at INTEGER NOT NULL, certificate_pem BLOB NOT NULL, private_key_ciphertext BLOB NOT NULL
);
CREATE TABLE tunnel_rotation_pairs (
 exit_id TEXT NOT NULL REFERENCES tunnel_rotations(exit_id) ON DELETE CASCADE,
 entry_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 credential_ciphertext BLOB NOT NULL, confirmed INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(exit_id,entry_id)
);`

type tunnelRotation struct {
	ExitID           string `json:"-"`
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Phase            string `json:"phase"`
	StartedAt        int64  `json:"-"`
	Certificate, Key []byte
}

func loadTunnelRotations(ctx context.Context, tx *sql.Tx) ([]tunnelRotation, error) {
	rows, err := tx.QueryContext(ctx, "SELECT exit_id,id,kind,phase,started_at,certificate_pem,private_key_ciphertext FROM tunnel_rotations WHERE phase<>'complete' ORDER BY exit_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rotations []tunnelRotation
	for rows.Next() {
		var v tunnelRotation
		if err := rows.Scan(&v.ExitID, &v.ID, &v.Kind, &v.Phase, &v.StartedAt, &v.Certificate, &v.Key); err != nil {
			return nil, err
		}
		rotations = append(rotations, v)
	}
	return rotations, rows.Err()
}

func (p *Panel) startTunnelRotation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Kind != "certificate" && body.Kind != "credentials" && body.Kind != "both" {
		problem(w, 400, "请选择 certificate、credentials 或 both")
		return
	}
	exit := r.PathValue("id")
	if !p.mutateForwarding(w, r, func(tx *sql.Tx) error {
		var name string
		var version int
		err := tx.QueryRowContext(r.Context(), "SELECT e.server_name,n.tunnel_rotation_version FROM tunnel_exits e JOIN nodes n ON n.id=e.node_id WHERE e.node_id=?", exit).Scan(&name, &version)
		if errors.Is(err, sql.ErrNoRows) {
			return &forwardingError{404, "出口不存在"}
		}
		if err != nil {
			return err
		}
		if version != 1 {
			return &forwardingError{409, "请先升级出口节点以支持无中断轮换"}
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), "SELECT count(*) FROM tunnel_rotations WHERE exit_id=? AND phase<>'complete'", exit).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return &forwardingError{409, "该出口已有进行中的轮换"}
		}
		// 使用当前关联规则，历史上已解除关联的节点对不参与确认。
		if err := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM nodes n WHERE n.tunnel_rotation_version<>1 AND n.id IN (
   SELECT em.node_id FROM entry_group_members em JOIN forward_rules r ON r.entry_group_id=em.group_id JOIN exit_group_members xm ON xm.group_id=r.exit_group_id
   WHERE r.enabled=1 AND r.kind='tunnel' AND xm.node_id=?)`, exit).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return &forwardingError{409, "请先升级所有相关入口节点以支持无中断轮换"}
		}
		id, _, err := p.randomCredential()
		if err != nil {
			return err
		}
		cert, key := []byte{}, []byte{}
		if body.Kind != "credentials" {
			cert, key, err = p.newExitCertificate(name)
			if err != nil {
				return err
			}
			key, err = p.secrets.(*sealedSecrets).seal("tunnel-rotation-key:"+exit+":"+id, key)
			if err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM tunnel_rotations WHERE exit_id=?", exit); err != nil {
			return err
		}
		_, err = tx.ExecContext(r.Context(), "INSERT INTO tunnel_rotations(exit_id,id,kind,phase,started_at,certificate_pem,private_key_ciphertext) VALUES(?,?,?,'preparing',?,?,?)", exit, id, body.Kind, p.deps.Clock.Now().UnixNano(), cert, key)
		return err
	}) {
		return
	}
	respond(w, 202, map[string]bool{"started": true})
}

func (p *Panel) getTunnelRotation(w http.ResponseWriter, r *http.Request) {
	// 控制锁内读取一致的进度，释放锁后才写响应。
	p.controlMu.Lock()
	v, err := p.tunnelRotationView(r.Context(), r.PathValue("id"))
	p.controlMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "出口不存在")
		return
	}
	if err != nil {
		problem(w, 503, "无法读取轮换进度")
		return
	}
	respond(w, 200, v)
}
func (p *Panel) tunnelRotationView(ctx context.Context, exit string) (map[string]any, error) {
	var exists int
	if err := p.db.QueryRowContext(ctx, "SELECT count(*) FROM tunnel_exits WHERE node_id=?", exit).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, sql.ErrNoRows
	}
	var phase, kind, id string
	var started int64
	err := p.db.QueryRowContext(ctx, "SELECT id,kind,phase,started_at FROM tunnel_rotations WHERE exit_id=?", exit).Scan(&id, &kind, &phase, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"phase": "idle", "entries": []any{}, "overdue": false}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := p.db.QueryContext(ctx, "SELECT p.entry_id,n.name,p.confirmed FROM tunnel_rotation_pairs p JOIN nodes n ON n.id=p.entry_id WHERE p.exit_id=? ORDER BY p.entry_id", exit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []any{}
	pending := 0
	for rows.Next() {
		var entry, name string
		var confirmed bool
		if err := rows.Scan(&entry, &name, &confirmed); err != nil {
			return nil, err
		}
		if !confirmed {
			pending++
		}
		entries = append(entries, map[string]any{"node_id": entry, "name": name, "confirmed": confirmed, "connected": p.controls[entry] != nil})
	}
	overdue := phase != "complete" && !p.deps.Clock.Now().Before(time.Unix(0, started).Add(7*24*time.Hour))
	warning := ""
	if overdue {
		warning = "轮换已满 7 天仍未完成，旧证书和旧凭据继续保留，请恢复未确认节点的连接"
		if phase == "cleaning" {
			warning = "出口已撤下旧材料，仍有入口未确认清理旧证书信任，请恢复其控制连接"
		}
	}
	return map[string]any{"id": id, "kind": kind, "phase": phase, "started_at": time.Unix(0, started).UTC().Format(time.RFC3339), "deadline": time.Unix(0, started).Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339), "entries": entries, "pending": pending, "overdue": overdue, "warning": warning, "old_materials_retired": phase == "cleaning" || phase == "complete"}, rows.Err()
}

// 在同一配置编译事务内追加重叠材料；入口的主凭据只在出口准备确认后切换。
func (p *Panel) compileTunnelRotations(ctx context.Context, tx *sql.Tx, compiled map[string]*controlpb.ConfigSnapshot) error {
	rotations, err := loadTunnelRotations(ctx, tx)
	if err != nil {
		return err
	}
	for _, v := range rotations {
		server := compiled[v.ExitID].TunnelServer
		if server == nil {
			continue
		}
		// 当前已解除关联的入口不再参与确认，包括旧信任清理阶段。
		if _, err := tx.ExecContext(ctx, `DELETE FROM tunnel_rotation_pairs WHERE exit_id=? AND entry_id NOT IN (
         SELECT em.node_id FROM entry_group_members em JOIN forward_rules r ON r.entry_group_id=em.group_id
         JOIN exit_group_members xm ON xm.group_id=r.exit_group_id WHERE r.enabled=1 AND r.kind='tunnel' AND xm.node_id=?)`, v.ExitID, v.ExitID); err != nil {
			return err
		}
		if v.Phase == "cleaning" {
			for _, peer := range server.Peers {
				var count int
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tunnel_rotation_pairs WHERE exit_id=? AND entry_id=?", v.ExitID, peer.EntryId).Scan(&count); err != nil {
					return err
				}
				if count == 0 {
					return &forwardingError{409, "请等待轮换清理完成后再新增关联入口"}
				}
				for _, g := range compiled[peer.EntryId].ExitGroups {
					for _, ep := range g.Exits {
						if ep.NodeId == v.ExitID {
							ep.RotationId = v.ID + ":cleanup"
						}
					}
				}
			}
			continue
		}
		server.RotationId = v.ID + ":" + v.Phase
		if v.Phase == "activating" && len(v.Certificate) > 0 {
			key, err := p.secrets.(*sealedSecrets).open("tunnel-rotation-key:"+v.ExitID+":"+v.ID, v.Key)
			if err != nil {
				return err
			}
			server.CertificatePem = v.Certificate
			server.PrivateKeyPem = key
		}
		for _, peer := range server.Peers {
			var sealed []byte
			err := tx.QueryRowContext(ctx, "SELECT credential_ciphertext FROM tunnel_rotation_pairs WHERE exit_id=? AND entry_id=?", v.ExitID, peer.EntryId).Scan(&sealed)
			if errors.Is(err, sql.ErrNoRows) {
				if v.Phase == "activating" {
					return &forwardingError{409, "出口正在撤下旧材料，请等待轮换完成后再新增关联入口"}
				}
				var version int
				if err := tx.QueryRowContext(ctx, "SELECT tunnel_rotation_version FROM nodes WHERE id=?", peer.EntryId).Scan(&version); err != nil {
					return err
				}
				if version != 1 {
					return &forwardingError{409, "请先升级新增入口节点以支持无中断轮换"}
				}
				token := peer.Credential
				if v.Kind != "certificate" {
					token, _, err = p.randomCredential()
					if err != nil {
						return err
					}
				}
				sealed, err = p.secrets.(*sealedSecrets).seal("tunnel-rotation-pair:"+v.ID+":"+peer.EntryId, []byte(token))
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT INTO tunnel_rotation_pairs(exit_id,entry_id,credential_ciphertext) VALUES(?,?,?)", v.ExitID, peer.EntryId, sealed); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			token, err := p.secrets.(*sealedSecrets).open("tunnel-rotation-pair:"+v.ID+":"+peer.EntryId, sealed)
			if err != nil {
				return err
			}
			if v.Phase == "preparing" || v.Phase == "distributing" {
				if peer.Credential != string(token) {
					peer.AdditionalCredentials = []string{string(token)}
				}
			}
			if v.Phase == "activating" {
				peer.Credential = string(token)
			}
			if v.Phase != "preparing" {
				for _, g := range compiled[peer.EntryId].ExitGroups {
					for _, ep := range g.Exits {
						if ep.NodeId == v.ExitID {
							ep.RotationId = v.ID
							ep.Credential = string(token)
							if len(v.Certificate) > 0 {
								ep.TrustedCertificatesPem = [][]byte{ep.CertificatePem}
								ep.CertificatePem = v.Certificate
							}
						}
					}
				}
			}
		}

	}
	return nil
}

// 只有经过 acceptReport 身份、会话与独立报告分流检查的完整落盘成功才推进。
func (p *Panel) confirmTunnelRotation(ctx context.Context, tx *sql.Tx, id string, report *controlpb.NodeReport, desired uint64, digest string) ([]string, error) {
	if desired == 0 || digest == "" || report.AttemptedRevision != desired || report.AppliedRevision != desired || report.AttemptedDigest != digest || report.AppliedDigest != digest || report.ApplyError != "" || !report.RevocationsApplied {
		return nil, nil
	}
	var data []byte
	var encrypted bool
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT s.snapshot,s.encrypted,n.tunnel_rotation_version FROM node_snapshots s JOIN nodes n ON n.id=s.node_id WHERE s.node_id=?", id).Scan(&data, &encrypted, &version); err != nil {
		return nil, err
	}
	if version != 1 {
		return nil, nil
	}
	snapshot := &controlpb.ConfigSnapshot{}
	if err := p.decodeSnapshot(id, data, encrypted, snapshot); err != nil {
		return nil, err
	}
	if snapshot.Revision != desired || snapshot.Digest != digest {
		return nil, nil
	}
	rotations, err := loadTunnelRotations(ctx, tx)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, v := range rotations {
		next := v.Phase
		if v.ExitID == id && v.Phase == "preparing" {
			next = "distributing"
		}
		if v.Phase == "distributing" || v.Phase == "cleaning" {
			for _, g := range snapshot.ExitGroups {
				for _, ep := range g.Exits {
					if ep.NodeId == v.ExitID && (v.Phase == "distributing" && ep.RotationId == v.ID || v.Phase == "cleaning" && ep.RotationId == v.ID+":cleanup") {
						if _, err := tx.ExecContext(ctx, "UPDATE tunnel_rotation_pairs SET confirmed=1 WHERE exit_id=? AND entry_id=?", v.ExitID, id); err != nil {
							return nil, err
						}
					}
				}
			}
		}
		if next == "distributing" || next == "cleaning" {
			var pending int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tunnel_rotation_pairs WHERE exit_id=? AND confirmed=0", v.ExitID).Scan(&pending); err != nil {
				return nil, err
			}
			if pending == 0 {
				if next == "cleaning" {
					next = "complete"
				} else {
					next = "activating"
				}
			}
		}
		if v.ExitID == id && v.Phase == "activating" {
			next = "cleaning"
			if err := p.activateTunnelRotation(ctx, tx, v); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE tunnel_rotation_pairs SET confirmed=0 WHERE exit_id=?", v.ExitID); err != nil {
				return nil, err
			}
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tunnel_rotation_pairs WHERE exit_id=?", v.ExitID).Scan(&count); err != nil {
				return nil, err
			}
			if count == 0 {
				next = "complete"
			}
		}
		if next != v.Phase {
			changed = true
			if _, err := tx.ExecContext(ctx, "UPDATE tunnel_rotations SET phase=? WHERE exit_id=?", next, v.ExitID); err != nil {
				return nil, err
			}
			if next == "complete" {
				if _, err := tx.ExecContext(ctx, "UPDATE tunnel_rotations SET certificate_pem=x'',private_key_ciphertext=x'' WHERE exit_id=?", v.ExitID); err != nil {
					return nil, err
				}
				if _, err := tx.ExecContext(ctx, "UPDATE tunnel_rotation_pairs SET credential_ciphertext=x'' WHERE exit_id=?", v.ExitID); err != nil {
					return nil, err
				}
			}
		}
	}
	if !changed {
		return nil, nil
	}
	return p.compileSnapshots(ctx, tx)
}
func (p *Panel) activateTunnelRotation(ctx context.Context, tx *sql.Tx, v tunnelRotation) error {
	if len(v.Certificate) > 0 {
		key, err := p.secrets.(*sealedSecrets).open("tunnel-rotation-key:"+v.ExitID+":"+v.ID, v.Key)
		if err != nil {
			return err
		}
		key, err = p.secrets.(*sealedSecrets).seal("tunnel-key:"+v.ExitID, key)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tunnel_exits SET certificate_pem=?,private_key_ciphertext=? WHERE node_id=?", v.Certificate, key, v.ExitID); err != nil {
			return err
		}
	}
	// 不关联的历史节点对不参加确认，但不能在重新关联时复活已撤下的旧凭据。
	if v.Kind != "certificate" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM tunnel_pairs WHERE exit_id=? AND entry_id NOT IN (SELECT entry_id FROM tunnel_rotation_pairs WHERE exit_id=?)", v.ExitID, v.ExitID); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT entry_id,credential_ciphertext FROM tunnel_rotation_pairs WHERE exit_id=?", v.ExitID)
	if err != nil {
		return err
	}
	type pair struct {
		entry string
		data  []byte
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.entry, &p.data); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, pair := range pairs {
		token, err := p.secrets.(*sealedSecrets).open("tunnel-rotation-pair:"+v.ID+":"+pair.entry, pair.data)
		if err != nil {
			return err
		}
		sealed, err := p.secrets.(*sealedSecrets).seal("tunnel-pair:"+pair.entry+":"+v.ExitID, token)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE tunnel_pairs SET credential_ciphertext=? WHERE exit_id=? AND entry_id=?", sealed, v.ExitID, pair.entry); err != nil {
			return err
		}
	}
	return nil
}
