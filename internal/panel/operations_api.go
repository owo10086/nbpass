package panel

import (
	"context"
	"database/sql"
	"net/http"
)

func (p *Panel) getOperations(w http.ResponseWriter, r *http.Request) {
	view, err := p.operationsSnapshot(r.Context())
	if err != nil {
		problem(w, 503, "运维状态暂不可用")
		return
	}
	view.IncidentSync, view.IncidentError, view.Counts.ManualIncidents = p.incidentProjectionStatus(r.Context())
	if err = p.attachOperationsIncidents(r.Context(), &view); err != nil {
		view.IncidentError = "故障摘要暂不可用"
	}
	respond(w, 200, view)
}

func (p *Panel) attachOperationsIncidents(ctx context.Context, view *operationsView) error {
	view.CurrentIncidents = []incidentRecord{}
	view.RecentIncidents = []incidentRecord{}
	for _, list := range []struct {
		where  string
		target *[]incidentRecord
	}{{"ended_at IS NULL ORDER BY CASE handling WHEN 'manual' THEN 0 ELSE 1 END,sequence DESC", &view.CurrentIncidents}, {"ended_at IS NOT NULL ORDER BY ended_at DESC,sequence DESC", &view.RecentIncidents}} {
		if err := operationRows(ctx, p.db, "SELECT "+incidentColumns+" FROM operation_incidents WHERE "+list.where+" LIMIT 20", func(rows *sql.Rows) error {
			r, e := scanIncident(rows)
			if e == nil {
				*list.target = append(*list.target, r)
			}
			return e
		}); err != nil {
			return err
		}
	}
	ids := map[string]string{}
	if err := operationRows(ctx, p.db, "SELECT "+incidentColumns+" FROM operation_incidents WHERE ended_at IS NULL ORDER BY CASE kind WHEN 'rule' THEN 1 ELSE 0 END,sequence", func(rows *sql.Rows) error {
		r, e := scanIncident(rows)
		if e != nil {
			return e
		}
		for _, ref := range r.Refs {
			if ref.Kind == "rule" {
				ids[ref.ID] = r.ID
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for i := range view.Rules {
		view.Rules[i].IncidentID = ids[view.Rules[i].ID]
	}
	return nil
}
