package panel

import "context"

func (p *Panel) wakeDNSWatch() {
	select {
	case p.dnsWatchWake <- struct{}{}:
	default:
	}
}
func (p *Panel) dnsWatchCheckers(ctx context.Context) (map[string]string, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	rows, err := p.db.QueryContext(ctx, `SELECT n.id,n.process_instance FROM nodes n JOIN node_regions r ON r.node_id=n.id
 WHERE CASE WHEN r.override_name IS NULL THEN r.auto_mainland ELSE r.override_mainland END=1
 AND r.checker_disabled=0 AND n.token_hash IS NOT NULL AND n.disconnected_at IS NULL AND n.last_seen_at>?
 AND (n.maintenance_until IS NULL OR n.maintenance_until<=?)`, p.deps.Clock.Now().Add(-offlineWindow).UnixNano(), p.deps.Clock.Now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var id, instance string
		if err := rows.Scan(&id, &instance); err != nil {
			return nil, err
		}
		if s := p.controls[id]; s != nil && s.dnsWatchCapable {
			result[id] = instance
		}
	}
	return result, rows.Err()
}
