package panel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"nbpass/internal/dnswatch"
)

func (p *Panel) dnsWatchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/dns-watch", p.getDNSWatchSettings)
	mux.HandleFunc("PUT /api/settings/dns-watch", p.putDNSWatchSettings)
	mux.HandleFunc("GET /api/dns-watches", p.listDNSWatches)
	mux.HandleFunc("POST /api/dns-watches", p.addDNSWatch)
	mux.HandleFunc("GET /api/dns-watches/{id}", p.getDNSWatch)
	mux.HandleFunc("GET /api/dns-watches/{id}/switches", p.getDNSWatchSwitches)
	mux.HandleFunc("PUT /api/dns-watches/{id}", p.putDNSWatch)
	mux.HandleFunc("DELETE /api/dns-watches/{id}", p.deleteDNSWatch)
}

type dnsWatchSettings struct {
	Resolvers []dnswatch.Resolver `json:"resolvers"`
}

func (p *Panel) getDNSWatchSettings(w http.ResponseWriter, r *http.Request) {
	var encoded string
	if err := p.db.QueryRowContext(r.Context(), "SELECT resolvers FROM dns_watch_settings WHERE id=1").Scan(&encoded); err != nil {
		problem(w, 503, "解析器设置暂不可用")
		return
	}
	var result dnsWatchSettings
	if json.Unmarshal([]byte(encoded), &result.Resolvers) != nil {
		problem(w, 503, "解析器设置损坏")
		return
	}
	respond(w, 200, result)
}
func (p *Panel) putDNSWatchSettings(w http.ResponseWriter, r *http.Request) {
	var body dnsWatchSettings
	if !decode(w, r, &body) {
		return
	}
	if len(body.Resolvers) == 0 || len(body.Resolvers) > 8 {
		problem(w, 400, "请配置 1–8 个解析器")
		return
	}
	seen := map[string]bool{}
	doh := false
	for _, resolver := range body.Resolvers {
		if err := resolver.Validate(); err != nil {
			problem(w, 400, err.Error())
			return
		}
		if seen[resolver.Key()] {
			problem(w, 400, "解析器不能重复")
			return
		}
		seen[resolver.Key()] = true
		doh = doh || resolver.Kind == "doh"
	}
	if !doh {
		problem(w, 400, "至少保留一个 DoH 对照解析器")
		return
	}
	data, _ := json.Marshal(body.Resolvers)
	p.dnsWatchMu.Lock()
	defer p.dnsWatchMu.Unlock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), "UPDATE dns_watch_settings SET resolvers=? WHERE id=1", string(data))
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "DELETE FROM dns_watch_tasks")
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE dns_watch_paths SET active=0,consecutive_mismatches=0,consecutive_matches=0")
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "UPDATE dns_watches SET next_sample_at=?", p.deps.Clock.Now().UnixNano())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	p.wakeDNSWatch()
	respond(w, 200, body)
}
func watchJSON(v any) string { data, _ := json.Marshal(v); return string(data) }
func (p *Panel) addDNSWatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Domain      string   `json:"domain"`
		ExpectedIPs []string `json:"expected_ips"`
	}
	if !decode(w, r, &body) {
		return
	}
	domain, err := dnswatch.Domain(body.Domain)
	if err != nil {
		problem(w, 400, "请填写纯域名，不包含协议、端口或路径")
		return
	}
	if len(body.ExpectedIPs) > 32 {
		problem(w, 400, "手工监控的期望 IPv4 最多 32 个")
		return
	}
	ips, err := dnswatch.Addresses(body.ExpectedIPs)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	id, _, err := p.randomCredential()
	if err != nil {
		problem(w, 503, "无法创建解析监控")
		return
	}
	p.dnsWatchMu.Lock()
	defer p.dnsWatchMu.Unlock()
	var exists int
	if err = p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM dns_watches WHERE domain=?", domain).Scan(&exists); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if exists > 0 {
		problem(w, 409, "域名已加入解析监控")
		return
	}
	now := p.deps.Clock.Now().UnixNano()
	_, err = p.db.ExecContext(r.Context(), "INSERT INTO dns_watches(id,domain,expected_ips,next_sample_at,created_at) VALUES(?,?,?,?,?)", id, domain, watchJSON(ips), now, now)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	p.wakeDNSWatch()
	respond(w, 201, map[string]any{"id": id, "domain": domain})
}
func (p *Panel) putDNSWatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedIPs *[]string `json:"expected_ips"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ExpectedIPs == nil {
		problem(w, 400, "请指定期望 IP，空数组表示仅观察")
		return
	}
	if len(*body.ExpectedIPs) > 32 {
		problem(w, 400, "手工监控的期望 IPv4 最多 32 个")
		return
	}
	ips, err := dnswatch.Addresses(*body.ExpectedIPs)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	p.dnsWatchMu.Lock()
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		p.dnsWatchMu.Unlock()
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	id := r.PathValue("id")
	var old string
	var group, ownerNode sql.NullString
	err = tx.QueryRowContext(r.Context(), "SELECT expected_ips,entry_group_id,owner_node_id FROM dns_watches WHERE id=?", id).Scan(&old, &group, &ownerNode)
	if errors.Is(err, sql.ErrNoRows) {
		p.dnsWatchMu.Unlock()
		problem(w, 404, "解析监控不存在")
		return
	}
	if err == nil && (group.Valid || ownerNode.Valid) {
		p.dnsWatchMu.Unlock()
		problem(w, 409, "受管域名的期望由面板自动维护")
		return
	}
	if err == nil && old != watchJSON(ips) {
		_, err = tx.ExecContext(r.Context(), "UPDATE dns_watches SET expected_ips=?,expected_version=expected_version+1,next_sample_at=? WHERE id=?", watchJSON(ips), p.deps.Clock.Now().UnixNano(), id)
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "DELETE FROM dns_watch_tasks WHERE watch_id=?", id)
		}
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "UPDATE dns_watch_paths SET active=0,consecutive_mismatches=0,consecutive_matches=0 WHERE watch_id=?", id)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	p.dnsWatchMu.Unlock()
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	p.wakeDNSWatch()
	p.getDNSWatch(w, r)
}
func (p *Panel) deleteDNSWatch(w http.ResponseWriter, r *http.Request) {
	p.dnsWatchMu.Lock()
	defer p.dnsWatchMu.Unlock()
	result, err := p.db.ExecContext(r.Context(), "DELETE FROM dns_watches WHERE id=? AND entry_group_id IS NULL AND owner_node_id IS NULL", r.PathValue("id"))
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		var managed int
		if e := p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM dns_watches WHERE id=? AND (entry_group_id IS NOT NULL OR owner_node_id IS NOT NULL)", r.PathValue("id")).Scan(&managed); e != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
		if managed > 0 {
			problem(w, 409, "受管域名自动加入解析监控，不能单独删除")
			return
		}
		problem(w, 404, "解析监控不存在")
		return
	}
	respond(w, 204, nil)
}

type dnsWatchPath struct {
	PathID   string `json:"path_id"`
	NodeID   string `json:"node_id"`
	Kind     string `json:"kind"`
	Resolver string `json:"resolver"`
	dnswatch.Progress
	Round     int64      `json:"round"`
	IPs       []string   `json:"ips"`
	Status    string     `json:"status"`
	SampledAt *time.Time `json:"sampled_at"`
}
type dnsWatchHistory struct {
	Sequence  int64     `json:"sequence"`
	PathID    string    `json:"path_id"`
	SampledAt time.Time `json:"sampled_at"`
	OldIPs    []string  `json:"old_ips"`
	IPs       []string  `json:"ips"`
	Status    string    `json:"status"`
	Changed   bool      `json:"changed"`
}
type dnsWatchView struct {
	OwnerNodeID         string            `json:"owner_node_id"`
	EntryGroupID        string            `json:"entry_group_id"`
	PropagationStatus   string            `json:"propagation_status"`
	ObservationDeadline *time.Time        `json:"observation_deadline"`
	ProviderError       string            `json:"provider_error"`
	ID                  string            `json:"id"`
	Domain              string            `json:"domain"`
	ExpectedIPs         []string          `json:"expected_ips"`
	ExpectedVersion     int64             `json:"expected_version"`
	Round               int64             `json:"round"`
	NextSampleAt        time.Time         `json:"next_sample_at"`
	CoverageLabel       string            `json:"coverage_label"`
	Status              string            `json:"status"`
	ChangeCount         int               `json:"change_count"`
	DifferentIPCount    int               `json:"different_ip_count"`
	TodaySwitches       int               `json:"today_switches"`
	YesterdaySwitches   int               `json:"yesterday_switches"`
	Paths               []dnsWatchPath    `json:"paths"`
	History             []dnsWatchHistory `json:"history,omitempty"`
	NextBefore          *int64            `json:"next_before,omitempty"`
}

func (p *Panel) listDNSWatches(w http.ResponseWriter, r *http.Request) { p.readDNSWatches(w, r, false) }
func (p *Panel) getDNSWatch(w http.ResponseWriter, r *http.Request)    { p.readDNSWatches(w, r, true) }
func (p *Panel) readDNSWatches(w http.ResponseWriter, r *http.Request, detail bool) {
	checkers, err := p.dnsWatchCheckers(r.Context())
	if err != nil {
		problem(w, 503, "检测点状态暂不可用")
		return
	}
	before := int64(1<<63 - 1)
	if value := r.URL.Query().Get("before"); value != "" {
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil || before < 1 {
			problem(w, 400, "历史分页游标无效")
			return
		}
	}
	zone, ok := dnsWatchZone(r)
	if !ok {
		problem(w, 400, "时区偏移无效")
		return
	}
	p.dnsWatchMu.Lock()

	query := "SELECT id,domain,expected_ips,expected_version,round,next_sample_at,coalesce(entry_group_id,''),coalesce(owner_node_id,''),propagation_status,observation_deadline,coalesce((SELECT last_error FROM entry_dns_audits WHERE group_id=entry_group_id),'') FROM dns_watches"
	args := []any{}
	if detail {
		query += " WHERE id=?"
		args = append(args, r.PathValue("id"))
	}
	query += " ORDER BY created_at,id"
	rows, err := p.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		p.dnsWatchMu.Unlock()
		problem(w, 503, "存储暂不可用")
		return
	}
	result := []dnsWatchView{}
	for rows.Next() {
		var v dnsWatchView
		var encoded string
		var next int64
		var deadline sql.NullInt64
		if err = rows.Scan(&v.ID, &v.Domain, &encoded, &v.ExpectedVersion, &v.Round, &next, &v.EntryGroupID, &v.OwnerNodeID, &v.PropagationStatus, &deadline, &v.ProviderError); err != nil {
			break
		}
		err = json.Unmarshal([]byte(encoded), &v.ExpectedIPs)
		if err != nil {
			break
		}
		v.NextSampleAt = time.Unix(0, next).UTC()
		v.ObservationDeadline = nullableTime(deadline)
		v.Paths = []dnsWatchPath{}
		v.Status = "等待采样"
		v.CoverageLabel = "国内覆盖缺失"
		result = append(result, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	for i := range result {
		v := &result[i]
		if err != nil {
			break
		}
		err = p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM dns_watch_history WHERE watch_id=? AND changed=1", v.ID).Scan(&v.ChangeCount)
		if err == nil {
			err = p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM dns_watch_ips WHERE watch_id=?", v.ID).Scan(&v.DifferentIPCount)
		}
		if err == nil {
			v.TodaySwitches, v.YesterdaySwitches, err = p.dnsWatchRecentSwitches(r, v.ID, zone)
		}
		if err != nil {
			break
		}
		paths, e := p.db.QueryContext(r.Context(), `SELECT path_id,node_id,kind,resolver,expected_version,round,ips,status,consecutive_mismatches,consecutive_matches,sampled_at FROM dns_watch_paths WHERE watch_id=? AND active=1 ORDER BY path_id`, v.ID)
		if e != nil {
			err = e
			break
		}
		for paths.Next() {
			var path dnsWatchPath
			var encoded string
			var at sql.NullInt64
			if err = paths.Scan(&path.PathID, &path.NodeID, &path.Kind, &path.Resolver, &path.ExpectedVersion, &path.Round, &encoded, &path.Status, &path.ConsecutiveMismatches, &path.ConsecutiveMatches, &at); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(encoded), &path.IPs); err != nil {
				break
			}
			path.SampledAt = nullableTime(at)
			v.Paths = append(v.Paths, path)
		}
		if err == nil {
			err = paths.Err()
		}
		paths.Close()
		summarizeDNSWatch(v, checkers, p.deps.Clock.Now())
		if detail && err == nil {
			history, e := p.db.QueryContext(r.Context(), "SELECT sequence,path_id,sampled_at,old_ips,ips,status,changed FROM dns_watch_history WHERE watch_id=? AND sequence<? ORDER BY sequence DESC LIMIT 101", v.ID, before)
			if e != nil {
				err = e
				break
			}
			v.History = []dnsWatchHistory{}
			for history.Next() {
				var item dnsWatchHistory
				var at int64
				var old, ips string
				if err = history.Scan(&item.Sequence, &item.PathID, &at, &old, &ips, &item.Status, &item.Changed); err != nil {
					break
				}
				item.SampledAt = time.Unix(0, at).UTC()
				if err = json.Unmarshal([]byte(old), &item.OldIPs); err != nil {
					break
				}
				if err = json.Unmarshal([]byte(ips), &item.IPs); err != nil {
					break
				}
				v.History = append(v.History, item)
			}
			if err == nil {
				err = history.Err()
			}
			history.Close()
			if len(v.History) > 100 {
				v.History = v.History[:100]
				cursor := v.History[99].Sequence
				v.NextBefore = &cursor
			}
		}
	}
	p.dnsWatchMu.Unlock()
	if err != nil {
		problem(w, 503, "解析监控存储暂不可用")
		return
	}
	if detail {
		if len(result) == 0 {
			problem(w, 404, "解析监控不存在")
			return
		}
		respond(w, 200, result[0])
		return
	}
	respond(w, 200, map[string]any{"watches": result})
}
