package panel

import (
	"encoding/json"
	"net/http"
	"time"

	"nbpass/internal/checker"
)

// 提供离线确认的证据和决策，读取不会发起检测或切换。
func (p *Panel) listOfflineEntryChecks(w http.ResponseWriter, r *http.Request) {
	type checkView struct {
		ID             string `json:"id"`
		NodeID         string `json:"node_id"`
		IncidentID     int64  `json:"incident_id"`
		DesiredVersion int64  `json:"desired_version"`
		Outcome        string `json:"outcome"`
		ActionID       string `json:"action_id"`
		checkerRoundView
	}
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "无法读取离线确认")
		return
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(r.Context(), "SELECT count(*) FROM entry_groups WHERE id=?", r.PathValue("id")).Scan(&exists); err != nil {
		problem(w, 503, "无法读取入口组")
		return
	}
	if exists == 0 {
		problem(w, 404, "入口组不存在")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT id,node_id,incident_id,desired_version,outcome,action_id,target_ip,target_port,config_revision,issued_at,deadline FROM entry_offline_checks WHERE group_id=? ORDER BY issued_at DESC LIMIT 20`, r.PathValue("id"))
	if err != nil {
		problem(w, 503, "无法读取离线确认")
		return
	}
	checks := []checkView{}
	for rows.Next() {
		var c checkView
		var issued, deadline int64
		if err = rows.Scan(&c.ID, &c.NodeID, &c.IncidentID, &c.DesiredVersion, &c.Outcome, &c.ActionID, &c.TargetIP, &c.TargetPort, &c.ConfigRevision, &issued, &deadline); err != nil {
			break
		}
		c.IssuedAt, c.Deadline = time.Unix(0, issued).UTC(), time.Unix(0, deadline).UTC()
		c.Status = c.Outcome
		c.Checkers = []checkerView{}
		checks = append(checks, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		problem(w, 503, "无法读取离线确认")
		return
	}
	for i := range checks {
		rows, err := tx.QueryContext(r.Context(), "SELECT id,checker_id,result FROM entry_offline_tasks WHERE check_id=? ORDER BY checker_id", checks[i].ID)
		if err != nil {
			problem(w, 503, "无法读取检测点证据")
			return
		}
		for rows.Next() {
			var c checkerView
			var data []byte
			if err = rows.Scan(&c.ID, &c.CheckerID, &data); err != nil {
				break
			}
			if data != nil {
				c.Result = &checker.Result{}
				if err = json.Unmarshal(data, c.Result); err != nil {
					break
				}
			}
			checks[i].Checkers = append(checks[i].Checkers, c)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			problem(w, 503, "无法读取检测点证据")
			return
		}
	}
	if err = tx.Commit(); err != nil {
		problem(w, 503, "无法读取离线确认")
		return
	}
	respond(w, 200, map[string]any{"checks": checks})
}
