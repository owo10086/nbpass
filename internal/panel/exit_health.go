package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 迁移 21，追加于 main 的 entryManualSwitchMigration(20) 之后。
const exitHealthMigration = `ALTER TABLE nodes ADD COLUMN exit_failover_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE probe_realtime ADD COLUMN exit_health_json BLOB NOT NULL DEFAULT '[]';`

// 与探针进程/序号去重共用事务；不会推进活性或授权确认。
func (p *Panel) storeExitHealth(ctx context.Context, tx *sql.Tx, id string, health []*controlpb.ExitHealth) error {
	snapshot, err := p.exitHealthSnapshot(ctx, tx, id)
	if err != nil {
		return err
	}
	valid := make([]*controlpb.ExitHealth, 0, len(health))
	seen := map[[2]string]bool{}
	for _, h := range health {
		key := [2]string{h.GetExitId(), forwarding.NormalizeAddressID(h.GetAddressId())}
		if h == nil || h.ExitId == "" || seen[key] || !matchesEndpointHealth(snapshot, h) {
			continue
		}
		if h.State != "unknown" && h.State != "healthy" && h.State != "unhealthy" && h.State != "recovering" {
			continue
		}
		var n int
		// 不能把无关节点伪造成当前入口的出口证据。
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tunnel_pairs WHERE entry_id=? AND exit_id=?`, id, h.ExitId).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		seen[key] = true
		if err := p.confirmExitAddressHandshake(ctx, tx, id, h); err != nil {
			return err
		}
		valid = append(valid, h)
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE probe_realtime SET exit_health_json=? WHERE node_id=?", raw, id)
	return err
}

func (p *Panel) getExitHealth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p.controlMu.Lock()
	var instance string
	err := p.db.QueryRowContext(r.Context(), "SELECT process_instance FROM nodes WHERE id=?", id).Scan(&instance)
	session := p.controls[id]
	connected := session != nil
	if connected {
		select {
		case <-session.done:
			connected = false
		default:
		}
	}
	var sampleInstance string
	var received int64
	var raw []byte
	sampleErr := p.db.QueryRowContext(r.Context(), "SELECT process_instance,received_at,exit_health_json FROM probe_realtime WHERE node_id=?", id).Scan(&sampleInstance, &received, &raw)
	snapshot, snapshotErr := p.exitHealthSnapshot(r.Context(), p.db, id)
	p.controlMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err != nil || snapshotErr != nil || sampleErr != nil && !errors.Is(sampleErr, sql.ErrNoRows) {
		problem(w, 503, "存储暂不可用")
		return
	}
	health := []*controlpb.ExitHealth{}
	var sampled *time.Time
	fresh := false
	if sampleErr == nil {
		if json.Unmarshal(raw, &health) != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
		matching := make([]*controlpb.ExitHealth, 0, len(health))
		for _, h := range health {
			if matchesEndpointHealth(snapshot, h) {
				view := proto.Clone(h).(*controlpb.ExitHealth)
				view.AddressId = forwarding.NormalizeAddressID(view.AddressId)
				matching = append(matching, view)
			}
		}
		health = matching
		at := time.Unix(0, received).UTC()
		sampled = &at
		fresh = connected && sampleInstance == instance && p.deps.Clock.Now().Sub(at) < 10*time.Second
	}
	respond(w, 200, map[string]any{"node_id": id, "fresh": fresh, "sampled_at": sampled, "exits": health})
}

// 能力降级可能撤掉远端 grants，再升级必须重新收到应用确认。
func (p *Panel) admitExitFailover(ctx context.Context, tx *sql.Tx, id string, version uint32) (bool, error) {
	var previous uint32
	if err := tx.QueryRowContext(ctx, "SELECT exit_failover_version FROM nodes WHERE id=?", id).Scan(&previous); err != nil {
		return false, err
	}
	if previous == version {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE nodes SET exit_failover_version=? WHERE id=?", version, id); err != nil {
		return false, err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM tunnel_authorization_confirmations WHERE entry_id=?", id)
	return true, err
}

// Health is evidence for the endpoint in the current desired snapshot, not for a
// server ID alone. Empty digests retain only the unchanged legacy IPv4 contract.
func matchesEndpointHealth(snapshot *controlpb.ConfigSnapshot, h *controlpb.ExitHealth) bool {
	for _, g := range snapshot.GetExitGroups() {
		for _, ep := range g.Exits {
			if ep.NodeId == h.GetExitId() && forwarding.NormalizeAddressID(ep.AddressId) == forwarding.NormalizeAddressID(h.GetAddressId()) && ep.EndpointConfigDigest == h.GetEndpointConfigDigest() {
				return true
			}
		}
	}
	return false
}
func (p *Panel) exitHealthSnapshot(ctx context.Context, q queryer, id string) (*controlpb.ConfigSnapshot, error) {
	var data []byte
	var encrypted bool
	rows, err := q.QueryContext(ctx, "SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", id)
	snapshot := &controlpb.ConfigSnapshot{}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return snapshot, rows.Err()
	}
	err = rows.Scan(&data, &encrypted)
	if err != nil {
		return nil, err
	}
	if err = p.decodeSnapshot(id, data, encrypted, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}
