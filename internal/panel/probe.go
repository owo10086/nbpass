package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"nbpass/internal/controlpb"
	"nbpass/internal/probe"
)

type probeAggregate struct {
	Count   int     `json:"count"`
	Sum     float64 `json:"sum"`
	Maximum float64 `json:"maximum"`
}

// 调用方已持有 controlMu，且已验证 token、活动会话和进程实例。
func (p *Panel) storeProbe(ctx context.Context, id, instance string, sample *controlpb.ProbeSample) error {
	if sample == nil || sample.Sequence == 0 || sample.Sequence > math.MaxInt64 {
		return nil
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previousInstance string
	var previousSequence uint64
	err = tx.QueryRowContext(ctx, "SELECT process_instance, sequence FROM probe_realtime WHERE node_id = ?", id).Scan(&previousInstance, &previousSequence)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && instance == previousInstance && sample.Sequence <= previousSequence {
		return nil
	}
	now := p.deps.Clock.Now().UTC()
	minute := now.Truncate(time.Minute).Unix()
	var count int
	var raw []byte
	aggregates := map[string]probeAggregate{}
	err = tx.QueryRowContext(ctx, "SELECT samples, aggregates_json FROM probe_minutes WHERE node_id = ? AND minute = ?", id, minute).Scan(&count, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if err := json.Unmarshal(raw, &aggregates); err != nil {
			return err
		}
	}
	values := probe.Clean(sample.Values)
	for key, value := range values {
		a := aggregates[key]
		a.Count++
		a.Sum += value
		a.Maximum = max(a.Maximum, value)
		aggregates[key] = a
	}
	raw, err = json.Marshal(aggregates)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO probe_minutes(node_id, minute, samples, aggregates_json) VALUES(?, ?, ?, ?)
		ON CONFLICT(node_id, minute) DO UPDATE SET samples = excluded.samples, aggregates_json = excluded.aggregates_json`, id, minute, count+1, raw)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO probe_realtime(node_id, process_instance, sequence, received_at, values_json) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET process_instance = excluded.process_instance, sequence = excluded.sequence,
		received_at = excluded.received_at, values_json = excluded.values_json`, id, instance, sample.Sequence, now.UnixNano(), raw)
	if err != nil {
		return err
	}
	if err := p.storeExitHealth(ctx, tx, id, sample.ExitHealth); err != nil {
		return err
	}
	return tx.Commit()
}

// 实时值只在当前进程实例仍保持控制连接、且最近一次采样未过期时可信。
func probeFresh(connected bool, instance, sampledInstance string, lastSeen *time.Time, sampledAt, now time.Time) bool {
	return connected && sampledInstance == instance && lastSeen != nil && now.Sub(*lastSeen) < 90*time.Second && now.Sub(sampledAt) < 2*probe.IntervalSeconds*time.Second
}

// 调用方已持有 controlMu。
func (p *Panel) liveSession(id string) bool {
	session := p.controls[id]
	if session == nil {
		return false
	}
	select {
	case <-session.done:
		return false
	default:
		return true
	}
}

// 节点列表的实时值（含悬停明细）；入库前已清洗，缺失的指标直接省略。
type probeSnapshot struct {
	instance string
	received time.Time
	values   probe.Values
}

// 调用方已持有 controlMu。
func (p *Panel) probeSnapshots(ctx context.Context) (map[string]probeSnapshot, error) {
	snapshots := map[string]probeSnapshot{}
	rows, err := p.db.QueryContext(ctx, "SELECT node_id, process_instance, received_at, values_json FROM probe_realtime")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var s probeSnapshot
		var received int64
		var raw []byte
		if err := rows.Scan(&id, &s.instance, &received, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.values); err != nil {
			return nil, err
		}
		s.received = time.Unix(0, received).UTC()
		snapshots[id] = s
	}
	return snapshots, rows.Err()
}

type probeMinute struct {
	Minute  time.Time           `json:"minute"`
	Samples int                 `json:"samples"`
	Average map[string]*float64 `json:"average"`
	Maximum map[string]*float64 `json:"maximum"`
	Counts  map[string]int      `json:"counts,omitempty"`
}

func (p *Panel) getProbe(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p.controlMu.Lock()
	connected := p.liveSession(id)
	var instance string
	var last sql.NullInt64
	err := p.db.QueryRowContext(r.Context(), "SELECT process_instance, last_seen_at FROM nodes WHERE id = ?", id).Scan(&instance, &last)
	var raw []byte
	var sampledInstance string
	var received int64
	var probeErr error
	if err == nil {
		probeErr = p.db.QueryRowContext(r.Context(), "SELECT process_instance, received_at, values_json FROM probe_realtime WHERE node_id = ?", id).Scan(&sampledInstance, &received, &raw)
	}
	p.controlMu.Unlock()
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "节点不存在")
		return
	}
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if probeErr != nil && !errors.Is(probeErr, sql.ErrNoRows) {
		problem(w, 503, "存储暂不可用")
		return
	}
	now := p.deps.Clock.Now().UTC()
	start := now.Truncate(time.Minute).Add(-1439 * time.Minute)
	minutes := make([]probeMinute, 1440)
	for i := range minutes {
		minutes[i].Minute = start.Add(time.Duration(i) * time.Minute)
	}
	rows, err := p.db.QueryContext(r.Context(), "SELECT minute, samples, aggregates_json FROM probe_minutes WHERE node_id = ? AND minute >= ? AND minute <= ? ORDER BY minute", id, start.Unix(), now.Truncate(time.Minute).Unix())
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	for rows.Next() {
		var minute int64
		var count int
		var raw []byte
		if err = rows.Scan(&minute, &count, &raw); err != nil {
			break
		}
		aggregates := map[string]probeAggregate{}
		if err = json.Unmarshal(raw, &aggregates); err != nil {
			break
		}
		averages, maxima := probe.Values{}, probe.Values{}
		counts := map[string]int{}
		for key, a := range aggregates {
			if a.Count > 0 {
				averages[key] = a.Sum / float64(a.Count)
				maxima[key] = a.Maximum
				counts[key] = a.Count
			}
		}
		index := int((minute - start.Unix()) / 60)
		if index >= 0 && index < len(minutes) {
			minutes[index].Samples = count
			minutes[index].Average = probe.Nullable(averages)
			minutes[index].Maximum = probe.Nullable(maxima)
			minutes[index].Counts = counts
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	var realtime map[string]*float64
	var sampledAt *time.Time
	fresh := false
	if probeErr == nil {
		t := time.Unix(0, received).UTC()
		sampledAt = &t
		fresh = probeFresh(connected, instance, sampledInstance, nullableTime(last), t, now)
		if fresh {
			values := probe.Values{}
			if err = json.Unmarshal(raw, &values); err != nil {
				problem(w, 503, "存储暂不可用")
				return
			}
			realtime = probe.Nullable(values)
		}
	}
	respond(w, 200, map[string]any{"fresh": fresh, "sampled_at": sampledAt, "realtime": realtime, "minutes": minutes})
}
