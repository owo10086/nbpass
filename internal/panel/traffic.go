package panel

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

const trafficMigration = `CREATE TABLE rule_traffic_totals (
 rule_id TEXT PRIMARY KEY REFERENCES forward_rules(id) ON DELETE CASCADE,
 upload_bytes INTEGER NOT NULL DEFAULT 0, download_bytes INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE rule_traffic_cursors (
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 process_instance TEXT NOT NULL, rule_id TEXT NOT NULL REFERENCES forward_rules(id) ON DELETE CASCADE,
 bucket_unix INTEGER NOT NULL, sequence INTEGER NOT NULL,
 upload_bytes INTEGER NOT NULL, download_bytes INTEGER NOT NULL,
 PRIMARY KEY(node_id,process_instance,rule_id,bucket_unix)
);
CREATE TABLE rule_traffic_realtime (
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 rule_id TEXT NOT NULL REFERENCES forward_rules(id) ON DELETE CASCADE,
 process_instance TEXT NOT NULL, sequence INTEGER NOT NULL,
 active_connections INTEGER NOT NULL, sampled_at INTEGER NOT NULL, received_at INTEGER NOT NULL,
 PRIMARY KEY(node_id,rule_id)
);
CREATE TABLE rule_traffic_sources (
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 rule_id TEXT NOT NULL REFERENCES forward_rules(id) ON DELETE CASCADE,
 PRIMARY KEY(node_id,rule_id)
);
INSERT INTO rule_traffic_sources(node_id,rule_id)
 SELECT m.node_id,r.id FROM forward_rules r JOIN entry_group_members m ON m.group_id=r.entry_group_id WHERE r.enabled=1;`

// 保留曾正式下发过的入口归属。缓存授权离线不设到期，移组不能丢掉原入口的补报。
func recordTrafficSources(ctx context.Context, tx *sql.Tx) error {
	rules, err := loadRules(ctx, tx)
	if err != nil {
		return err
	}
	members, err := loadEntryGroupMembers(ctx, tx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, id := range forwarding.RuleEntryNodes(r, members) {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO rule_traffic_sources(node_id,rule_id) VALUES(?,?)", id, r.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Panel) acceptTraffic(ctx context.Context, id, instance string, report *controlpb.NodeReport) (*controlpb.PanelMessage, error) {
	if report.ProbeOnly || report.EventsOnly || report.RotationOnly || len(report.Traffic) > 16 {
		return nil, status.Error(codes.InvalidArgument, "流量报告类型或批次无效")
	}
	now := p.deps.Clock.Now().UTC()
	for _, s := range report.Traffic {
		if s == nil || s.ProcessInstance == "" || len(s.ProcessInstance) > 128 || s.Sequence == 0 || s.Sequence > math.MaxInt64 || s.RuleId == "" || len(s.RuleId) > 128 || s.BucketUnix%60 != 0 || s.UploadBytes > math.MaxInt64 || s.DownloadBytes > math.MaxInt64 || s.ActiveConnections > math.MaxInt64 {
			return nil, status.Error(codes.InvalidArgument, "流量计数字段无效")
		}
		at, err := time.Parse(time.RFC3339Nano, s.SampledAt)
		if err != nil || at.After(now.Add(5*time.Minute)) || time.Unix(s.BucketUnix, 0).After(at) {
			return nil, status.Error(codes.InvalidArgument, "流量采样时间无效")
		}
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "流量存储暂不可用")
	}
	defer tx.Rollback()
	response := &controlpb.PanelMessage{ProtocolVersion: 1, TrafficOnly: true}
	for _, s := range report.Traffic {
		response.AcknowledgedTraffic = append(response.AcknowledgedTraffic, &controlpb.TrafficAck{ProcessInstance: s.ProcessInstance, Sequence: s.Sequence})
		if time.Unix(s.BucketUnix, 0).Before(now.Add(-24 * time.Hour)) {
			continue
		}
		// 身份取当前 token；只有曾下发过该规则的入口可补报，出口不能重复记账。
		var allowed int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM rule_traffic_sources WHERE rule_id=? AND node_id=?`, s.RuleId, id).Scan(&allowed)
		if err != nil {
			break
		}
		if allowed == 0 {
			continue
		}
		var seq, up, down uint64
		err = tx.QueryRowContext(ctx, `SELECT sequence,upload_bytes,download_bytes FROM rule_traffic_cursors WHERE node_id=? AND process_instance=? AND rule_id=? AND bucket_unix=?`, id, s.ProcessInstance, s.RuleId, s.BucketUnix).Scan(&seq, &up, &down)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			break
		}
		err = nil
		if s.Sequence <= seq {
			continue
		}
		// 同一进程同一桶计数不能回退；异常报告不能降低高水位或制造负增量。
		if s.UploadBytes < up || s.DownloadBytes < down {
			continue
		}
		var totalUp, totalDown uint64
		err = tx.QueryRowContext(ctx, `SELECT upload_bytes,download_bytes FROM rule_traffic_totals WHERE rule_id=?`, s.RuleId).Scan(&totalUp, &totalDown)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			break
		}
		err = nil
		du, dd := s.UploadBytes-up, s.DownloadBytes-down
		if du > math.MaxInt64-totalUp || dd > math.MaxInt64-totalDown {
			return nil, status.Error(codes.OutOfRange, "规则流量累计值超限")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO rule_traffic_totals(rule_id,upload_bytes,download_bytes) VALUES(?,?,?) ON CONFLICT(rule_id) DO UPDATE SET upload_bytes=excluded.upload_bytes,download_bytes=excluded.download_bytes`, s.RuleId, totalUp+du, totalDown+dd)
		if err != nil {
			break
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO rule_traffic_cursors VALUES(?,?,?,?,?,?,?) ON CONFLICT(node_id,process_instance,rule_id,bucket_unix) DO UPDATE SET sequence=excluded.sequence,upload_bytes=excluded.upload_bytes,download_bytes=excluded.download_bytes`, id, s.ProcessInstance, s.RuleId, s.BucketUnix, s.Sequence, s.UploadBytes, s.DownloadBytes)
		if err != nil {
			break
		}
		at, _ := time.Parse(time.RFC3339Nano, s.SampledAt)
		if s.ProcessInstance == instance && s.BucketUnix == now.Truncate(time.Minute).Unix() && now.Sub(at) < 10*time.Second {
			_, err = tx.ExecContext(ctx, `INSERT INTO rule_traffic_realtime VALUES(?,?,?,?,?,?,?) ON CONFLICT(node_id,rule_id) DO UPDATE SET process_instance=excluded.process_instance,sequence=excluded.sequence,active_connections=excluded.active_connections,sampled_at=excluded.sampled_at,received_at=excluded.received_at WHERE process_instance<>excluded.process_instance OR sequence<excluded.sequence`, id, s.RuleId, s.ProcessInstance, s.Sequence, s.ActiveConnections, at.UnixNano(), now.UnixNano())
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "流量存储暂不可用")
	}
	return response, nil
}

type ruleTrafficView struct {
	UploadBytes       uint64  `json:"upload_bytes"`
	DownloadBytes     uint64  `json:"download_bytes"`
	ActiveConnections *uint64 `json:"active_connections"`
}
type ruleWithTraffic struct {
	ruleView
	Traffic ruleTrafficView `json:"traffic"`
}

func (p *Panel) rulesWithTraffic(ctx context.Context, rules []ruleView) ([]ruleWithTraffic, error) {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	out := make([]ruleWithTraffic, 0, len(rules))
	now := p.deps.Clock.Now()
	for _, r := range rules {
		v := ruleWithTraffic{ruleView: r}
		err := p.db.QueryRowContext(ctx, `SELECT upload_bytes,download_bytes FROM rule_traffic_totals WHERE rule_id=?`, r.ID).Scan(&v.Traffic.UploadBytes, &v.Traffic.DownloadBytes)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		fresh := true
		members := len(r.SyncMembers)
		var total uint64
		// 同步成员已由 RuleEntryNodes 解析；历史来源只补字节，不进入实时连接数。
		ids := make([]string, 0, members)
		args := []any{r.ID}
		for _, member := range r.SyncMembers {
			ids = append(ids, "?")
			args = append(args, member.NodeID)
		}
		if members == 0 {
			out = append(out, v)
			continue
		}
		rows, err := p.db.QueryContext(ctx, `SELECT n.id,n.process_instance,n.last_seen_at,t.process_instance,t.active_connections,t.sampled_at,t.received_at FROM nodes n LEFT JOIN rule_traffic_realtime t ON t.node_id=n.id AND t.rule_id=? WHERE n.id IN (`+strings.Join(ids, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, instance string
			var sampledInstance sql.NullString
			var last, active, at, received sql.NullInt64
			if err := rows.Scan(&id, &instance, &last, &sampledInstance, &active, &at, &received); err != nil {
				rows.Close()
				return nil, err
			}
			session := p.controls[id]
			connected := session != nil
			if connected {
				select {
				case <-session.done:
					connected = false
				default:
				}
			}
			if !connected || !last.Valid || now.Sub(time.Unix(0, last.Int64)) >= 90*time.Second || !sampledInstance.Valid || sampledInstance.String != instance || !at.Valid || now.Sub(time.Unix(0, at.Int64)) >= 10*time.Second || now.Sub(time.Unix(0, received.Int64)) >= 10*time.Second {
				fresh = false
			} else {
				total += uint64(active.Int64)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if fresh && members > 0 {
			v.Traffic.ActiveConnections = &total
		}
		out = append(out, v)
	}
	return out, nil
}
