package panel

import (
	"context"
	"database/sql"
	"net/http"

	"nbpass/internal/dnswatch"
)

func (p *Panel) bindNodeDomainWatchTx(ctx context.Context, tx *sql.Tx, b managedRecord) error {
	domain, err := dnswatch.Domain(b.Hostname)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM dns_watches WHERE owner_node_id=? AND domain<>?", b.NodeID, domain); err != nil {
		return err
	}
	now := p.deps.Clock.Now().UnixNano()
	_, err = tx.ExecContext(ctx, `UPDATE dns_watches SET owner_node_id=?,expected_ips='[]',expected_version=expected_version+1,next_sample_at=?,last_status='' WHERE domain=? AND entry_group_id IS NULL AND owner_node_id IS NULL AND NOT EXISTS(SELECT 1 FROM dns_watches WHERE owner_node_id=?)`, b.NodeID, now, domain, b.NodeID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO dns_watches(id,domain,expected_ips,expected_version,next_sample_at,created_at,owner_node_id) SELECT ?,?,'[]',0,?,?,? WHERE NOT EXISTS(SELECT 1 FROM dns_watches WHERE owner_node_id=?)`, "node:"+b.NodeID, domain, now, now, b.NodeID, b.NodeID)
	}
	if err == nil {
		err = p.syncNodeWatchTx(ctx, tx, b.NodeID)
	}
	return err
}

// 期望取确认地址；与确认地址/动作同事务更新并撤销旧采样，普通上报不调用。
func (p *Panel) syncNodeWatchTx(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var address string
	if err := tx.QueryRowContext(ctx, "SELECT coalesce((SELECT confirmed_ipv4 FROM node_regions WHERE node_id=?),'')", nodeID).Scan(&address); err != nil {
		return err
	}
	ips := []string{}
	if address != "" {
		ips = append(ips, address)
	}
	_, err := tx.ExecContext(ctx, `UPDATE dns_watches SET expected_ips=?,expected_version=expected_version+1,next_sample_at=?,last_status='' WHERE owner_node_id=?`, watchJSON(ips), p.deps.Clock.Now().UnixNano(), nodeID)
	if err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM dns_watch_tasks WHERE watch_id IN (SELECT id FROM dns_watches WHERE owner_node_id=?)", nodeID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE dns_watch_paths SET active=0,consecutive_matches=0,consecutive_mismatches=0,sampled_at=NULL WHERE watch_id IN (SELECT id FROM dns_watches WHERE owner_node_id=?)", nodeID)
	}
	return err
}
func (p *Panel) getNodeDomainWatch(w http.ResponseWriter, r *http.Request) {
	var id string
	err := p.db.QueryRowContext(r.Context(), "SELECT id FROM dns_watches WHERE owner_node_id=?", r.PathValue("id")).Scan(&id)
	if err != nil {
		p.nodeRecordError(w, err)
		return
	}
	r.SetPathValue("id", id)
	p.getDNSWatch(w, r)
}
