package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// 域名级切换：各路径的地址变化按发生顺序串联，新集合与上一条变化相同视为其他路径迟到看到的同一次切换。
// 旧值取上一条变化的新集合，使相邻切换首尾相接；首条变化沿用该路径自己的上次成功集合。
const dnsWatchSwitchesQuery = `SELECT sequence,sampled_at,coalesce(previous,old_ips) AS old_ips,ips FROM (
 SELECT sequence,sampled_at,old_ips,ips,lag(ips) OVER (ORDER BY sequence) AS previous
 FROM dns_watch_history WHERE watch_id=? AND changed=1
) WHERE previous IS NULL OR previous<>ips`

const dnsWatchSwitchDays = 7

type dnsWatchSwitch struct {
	At     time.Time `json:"at"`
	OldIPs []string  `json:"old_ips"`
	IPs    []string  `json:"ips"`
}
type dnsWatchSwitchDay struct {
	Date     string           `json:"date"`
	Count    int              `json:"count"`
	Switches []dnsWatchSwitch `json:"switches"`
}

// 按天统计使用浏览器时区；utc_offset 为东向分钟数，缺省按 UTC。
func dnsWatchZone(r *http.Request) (*time.Location, bool) {
	value := r.URL.Query().Get("utc_offset")
	if value == "" {
		return time.UTC, true
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < -840 || offset > 840 {
		return nil, false
	}
	return time.FixedZone("", offset*60), true
}

func dnsWatchDayStart(at time.Time, zone *time.Location) time.Time {
	at = at.In(zone)
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, zone)
}

// 今日与昨日的域名级切换次数。
func (p *Panel) dnsWatchRecentSwitches(r *http.Request, id string, zone *time.Location) (today, yesterday int, err error) {
	start := dnsWatchDayStart(p.deps.Clock.Now(), zone)
	previous := start.AddDate(0, 0, -1)
	err = p.db.QueryRowContext(r.Context(), "SELECT coalesce(sum(sampled_at>=?),0),coalesce(sum(sampled_at>=? AND sampled_at<?),0) FROM ("+dnsWatchSwitchesQuery+")",
		start.UnixNano(), previous.UnixNano(), start.UnixNano(), id).Scan(&today, &yesterday)
	return today, yesterday, err
}

func (p *Panel) getDNSWatchSwitches(w http.ResponseWriter, r *http.Request) {
	zone, ok := dnsWatchZone(r)
	if !ok {
		problem(w, 400, "时区偏移无效")
		return
	}
	before := int64(1<<63 - 1)
	if value := r.URL.Query().Get("before"); value != "" {
		day, err := time.ParseInLocation(time.DateOnly, value, zone)
		if err != nil {
			problem(w, 400, "日期分页游标无效")
			return
		}
		before = day.UnixNano()
	}
	id := r.PathValue("id")
	var exists int
	if err := p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM dns_watches WHERE id=?", id).Scan(&exists); err != nil {
		problem(w, 503, "解析监控存储暂不可用")
		return
	}
	if exists == 0 {
		problem(w, 404, "解析监控不存在")
		return
	}
	rows, err := p.db.QueryContext(r.Context(), "SELECT sampled_at,old_ips,ips FROM ("+dnsWatchSwitchesQuery+") WHERE sampled_at<? ORDER BY sequence DESC", id, before)
	if err != nil {
		problem(w, 503, "解析监控存储暂不可用")
		return
	}
	defer rows.Close()
	days := []dnsWatchSwitchDay{}
	var next *string
	for rows.Next() {
		var at int64
		var old, ips string
		var item dnsWatchSwitch
		if err = rows.Scan(&at, &old, &ips); err != nil {
			break
		}
		item.At = time.Unix(0, at).UTC()
		if err = errors.Join(json.Unmarshal([]byte(old), &item.OldIPs), json.Unmarshal([]byte(ips), &item.IPs)); err != nil {
			break
		}
		date := item.At.In(zone).Format(time.DateOnly)
		if len(days) == 0 || days[len(days)-1].Date != date {
			if len(days) == dnsWatchSwitchDays {
				next = &days[len(days)-1].Date
				break
			}
			days = append(days, dnsWatchSwitchDay{Date: date, Switches: []dnsWatchSwitch{}})
		}
		day := &days[len(days)-1]
		day.Count++
		day.Switches = append(day.Switches, item)
	}
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		problem(w, 503, "解析监控存储暂不可用")
		return
	}
	respond(w, 200, map[string]any{"days": days, "next_before": next})
}
