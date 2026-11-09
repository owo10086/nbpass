package panel

import "net/http"

type retentionSettings struct {
	EventDays  int `json:"event_days"`
	ProbeHours int `json:"probe_hours"`
}

func (p *Panel) retentionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/retention", p.getRetention)
	mux.HandleFunc("PUT /api/settings/retention", p.putRetention)
}

func (p *Panel) getRetention(w http.ResponseWriter, r *http.Request) {
	var settings retentionSettings
	if err := p.db.QueryRowContext(r.Context(), "SELECT event_days,probe_hours FROM retention_settings WHERE id=1").Scan(&settings.EventDays, &settings.ProbeHours); err != nil {
		problem(w, 503, "无法读取保留期设置")
		return
	}
	respond(w, 200, settings)
}

func (p *Panel) putRetention(w http.ResponseWriter, r *http.Request) {
	var settings retentionSettings
	if !decode(w, r, &settings) {
		return
	}
	if settings.EventDays < 1 || settings.EventDays > 3650 || settings.ProbeHours < 1 || settings.ProbeHours > 8760 {
		problem(w, 400, "事件日志保留期须为 1–3650 整天，探针曲线保留期须为 1–8760 整小时")
		return
	}
	if _, err := p.db.ExecContext(r.Context(), "UPDATE retention_settings SET event_days=?,probe_hours=? WHERE id=1", settings.EventDays, settings.ProbeHours); err != nil {
		problem(w, 503, "无法保存保留期设置")
		return
	}
	select {
	case p.retentionWake <- struct{}{}:
	default:
	}
	respond(w, 200, settings)
}
