package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"nbpass/internal/checker"
	"time"
)

type checkerView struct {
	ID        string          `json:"id"`
	CheckerID string          `json:"checker_id"`
	Result    *checker.Result `json:"result"`
}
type checkerRoundView struct {
	TargetIP       string        `json:"target_ip"`
	TargetPort     int           `json:"target_port"`
	ConfigRevision uint64        `json:"config_revision"`
	IssuedAt       time.Time     `json:"issued_at"`
	Deadline       time.Time     `json:"deadline"`
	Status         string        `json:"status"`
	Checkers       []checkerView `json:"checkers"`
}

func loadCheckerRound(ctx context.Context, tx *sql.Tx, id, roundID string) (*checkerRoundView, error) {
	v := &checkerRoundView{Checkers: []checkerView{}}
	var issued, deadline int64
	err := tx.QueryRowContext(ctx, "SELECT target_ip,target_port,config_revision,issued_at,deadline,status FROM checker_rounds WHERE node_id=? AND round_id=?", id, roundID).Scan(&v.TargetIP, &v.TargetPort, &v.ConfigRevision, &issued, &deadline, &v.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.IssuedAt = time.Unix(0, issued).UTC()
	v.Deadline = time.Unix(0, deadline).UTC()
	rows, err := tx.QueryContext(ctx, "SELECT id,checker_id,result FROM checker_tasks WHERE node_id=? AND round_id=? ORDER BY checker_id", id, roundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c checkerView
		var data []byte
		if err = rows.Scan(&c.ID, &c.CheckerID, &data); err != nil {
			return nil, err
		}
		if data != nil {
			c.Result = &checker.Result{}
			if err = json.Unmarshal(data, c.Result); err != nil {
				return nil, err
			}
		}
		v.Checkers = append(v.Checkers, c)
	}
	return v, rows.Err()
}
