package panel

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"nbpass/internal/controlpb"
)

// 迁移 25：地址版本与证书轮换版本独立，确认不依赖可清理的探针历史。
const exitBlockedMigration = `ALTER TABLE tunnel_exits ADD COLUMN blocked_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tunnel_exits ADD COLUMN address_version TEXT NOT NULL DEFAULT '';
CREATE TABLE exit_address_progress (
 action_id TEXT NOT NULL REFERENCES rotation_actions(id) ON DELETE CASCADE,
 entry_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 applied_revision INTEGER NOT NULL DEFAULT 0,
 handshaken_at INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(action_id,entry_id)
);`

// 单节点入口、出口和入口组用途只决定申请资格；执行、额度、冻结仍只有节点级一份。
const singleEntryRotationPurpose = `EXISTS(SELECT 1 FROM forward_rules r WHERE r.entry_node_id=c.node_id AND r.enabled=1)`

// 全部发布组成员申请换 IP 的唯一用途判定；票 07 在这里拦截因该组发起的申请。
const allMemberRotationPurpose = `EXISTS(SELECT 1 FROM entry_group_members m JOIN entry_groups g ON g.id=m.group_id WHERE m.node_id=c.node_id AND g.publication_mode='all' AND g.first_published_at IS NOT NULL)`

const automaticRotationRequestedPurpose = `(` + singleEntryRotationPurpose + ` OR ` + allMemberRotationPurpose + ` OR EXISTS(SELECT 1 FROM entry_group_members m JOIN entry_groups g ON g.id=m.group_id WHERE m.node_id=c.node_id AND g.publication_mode='primary_backup' AND g.blocked_policy='rotate_ip') OR EXISTS(SELECT 1 FROM tunnel_exits e WHERE e.node_id=c.node_id AND e.blocked_enabled=1) OR ` + strategyThreeRotationPurpose + `)`

// 任一归属组开启观测即否决所有自动用途，包括出口与单节点入口。
const automaticRotationPurpose = automaticRotationRequestedPurpose + ` AND NOT EXISTS(SELECT 1 FROM entry_group_members om JOIN entry_groups og ON og.id=om.group_id WHERE om.node_id=c.node_id AND og.observation_enabled=1)`

func (p *Panel) exitBlockedRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{id}/exit-blocked", p.getExitBlocked)
	mux.HandleFunc("PUT /api/nodes/{id}/exit-blocked", p.putExitBlocked)
}

func (p *Panel) putExitBlocked(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil {
		problem(w, 400, "请指定出口被墙处理开关")
		return
	}
	p.controlMu.Lock()
	result, err := p.db.ExecContext(r.Context(), "UPDATE tunnel_exits SET blocked_enabled=? WHERE node_id=?", *b.Enabled, r.PathValue("id"))
	if err == nil {
		err = missingResult(result, nil)
	}
	p.controlMu.Unlock()
	if err != nil {
		if !writeConfigurationError(w, err) {
			problem(w, 503, "无法保存出口被墙处理设置")
		}
		return
	}
	respond(w, 200, map[string]bool{"enabled": *b.Enabled})
}

type exitAddressEntry struct {
	NodeID          string     `json:"node_id"`
	Name            string     `json:"name"`
	Connected       bool       `json:"connected"`
	Applied         bool       `json:"applied"`
	AppliedRevision uint64     `json:"applied_revision"`
	Handshaken      bool       `json:"handshaken"`
	HandshakenAt    *time.Time `json:"handshaken_at"`
}

func (p *Panel) getExitBlocked(w http.ResponseWriter, r *http.Request) {
	p.controlMu.Lock()
	var enabled bool
	var address, version string
	err := p.db.QueryRowContext(r.Context(), "SELECT blocked_enabled,address,address_version FROM tunnel_exits WHERE node_id=?", r.PathValue("id")).Scan(&enabled, &address, &version)
	entries := []exitAddressEntry{}
	if err == nil {
		var rows *sql.Rows
		rows, err = p.db.QueryContext(r.Context(), `SELECT n.id,n.name,p.applied_revision,p.handshaken_at FROM exit_address_progress p JOIN nodes n ON n.id=p.entry_id WHERE p.action_id=? ORDER BY n.id`, version)
		if err == nil {
			for rows.Next() {
				var e exitAddressEntry
				var at int64
				if err = rows.Scan(&e.NodeID, &e.Name, &e.AppliedRevision, &at); err != nil {
					break
				}
				e.Applied = e.AppliedRevision > 0
				e.Handshaken = at > 0
				if at > 0 {
					t := time.Unix(0, at).UTC()
					e.HandshakenAt = &t
				}
				if s := p.controls[e.NodeID]; s != nil {
					select {
					case <-s.done:
					default:
						e.Connected = true
					}
				}
				entries = append(entries, e)
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
	}
	p.controlMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "出口未配置")
		return
	}
	if err != nil {
		problem(w, 503, "无法读取出口地址进度")
		return
	}
	respond(w, 200, map[string]any{"enabled": enabled, "address": address, "action_id": version, "entries": entries})
}

// 仅首次确认换成时更新；不要求出口策略仍开启，手动/入口用途也须修正出口地址。
func (p *Panel) rotateExitAddress(ctx context.Context, tx *sql.Tx, a *rotationAction, newIP string) ([]string, error) {
	result, err := tx.ExecContext(ctx, "UPDATE tunnel_exits SET address=?,address_version=? WHERE node_id=?", newIP, a.ID, a.NodeID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return nil, err
	}
	return p.compileSnapshots(ctx, tx)
}

// 在最终入口快照中登记参与者，后续新关联入口也能显示本次地址的确认。
func trackExitAddresses(ctx context.Context, tx *sql.Tx, compiled map[string]*controlpb.ConfigSnapshot) error {
	wanted := map[[2]string]bool{}
	for entry, s := range compiled {
		for _, g := range s.ExitGroups {
			for _, ep := range g.Exits {
				if ep.AddressVersion == "" {
					continue
				}
				wanted[[2]string{ep.AddressVersion, entry}] = true
			}
		}
	}
	// 当前地址已解除关联的入口不再参与；重新关联必须重新取得两类确认。
	rows, err := tx.QueryContext(ctx, `SELECT p.action_id,p.entry_id FROM exit_address_progress p JOIN tunnel_exits e ON e.address_version=p.action_id`)
	if err != nil {
		return err
	}
	var removed [][2]string
	for rows.Next() {
		var key [2]string
		if err = rows.Scan(&key[0], &key[1]); err != nil {
			break
		}
		if !wanted[key] {
			removed = append(removed, key)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range removed {
		if _, err = tx.ExecContext(ctx, "DELETE FROM exit_address_progress WHERE action_id=? AND entry_id=?", key[0], key[1]); err != nil {
			return err
		}
	}
	for key := range wanted {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO exit_address_progress(action_id,entry_id) VALUES(?,?)", key[0], key[1]); err != nil {
			return err
		}
	}
	return nil
}

// 只有当前快照完整落盘成功的配置报告确认应用；探针不能代为确认。
func (p *Panel) confirmExitAddresses(ctx context.Context, tx *sql.Tx, id string, r *controlpb.NodeReport, desired uint64, digest string) error {
	if desired == 0 || digest == "" || r.AttemptedRevision != desired || r.AppliedRevision != desired || r.AttemptedDigest != digest || r.AppliedDigest != digest || r.ApplyError != "" || !r.RevocationsApplied {
		return nil
	}
	var data []byte
	var encrypted bool
	if err := tx.QueryRowContext(ctx, "SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", id).Scan(&data, &encrypted); err != nil {
		return err
	}
	s := &controlpb.ConfigSnapshot{}
	if err := p.decodeSnapshot(id, data, encrypted, s); err != nil {
		return err
	}
	for _, g := range s.ExitGroups {
		for _, ep := range g.Exits {
			if ep.AddressVersion == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE exit_address_progress SET applied_revision=MAX(applied_revision,?) WHERE action_id=? AND entry_id=?", desired, ep.AddressVersion, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// 真实健康握手单独推进本次地址版本；旧地址、失败及无版本旧节点均不确认。
func (p *Panel) confirmExitAddressHandshake(ctx context.Context, tx *sql.Tx, entry string, h *controlpb.ExitHealth) error {
	if h.AddressVersion == "" || h.LastProbe != "success" || h.CheckedAtUnix == 0 {
		return nil
	}
	now := p.deps.Clock.Now()
	at := time.Unix(h.CheckedAtUnix, 0)
	if at.After(now) || now.Sub(at) >= 10*time.Second {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE exit_address_progress SET handshaken_at=MAX(handshaken_at,?) WHERE action_id=? AND entry_id=? AND EXISTS(SELECT 1 FROM tunnel_exits e WHERE e.node_id=? AND e.address_version=action_id)`, at.UnixNano(), h.AddressVersion, entry, h.ExitId)
	return err
}
