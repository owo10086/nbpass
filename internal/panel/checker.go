package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"time"

	"nbpass/internal/checker"
	"nbpass/internal/selfcheck"
)

// 调用者持有 controlMu，任务和未知原始轮在同一事务中冻结。
func (p *Panel) scheduleCheckers(ctx context.Context, tx *sql.Tx, nodeID string, round selfcheck.Round) error {
	var ip string
	var port int
	var revision uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(r.public_ipv4,''),n.check_port,n.desired_revision FROM nodes n LEFT JOIN node_regions r ON r.node_id=n.id WHERE n.id=?`, nodeID).Scan(&ip, &port, &revision); err != nil {
		return err
	}
	now := p.deps.Clock.Now()
	deadline := now.Add(60 * time.Second)
	_, err := tx.ExecContext(ctx, `INSERT INTO checker_rounds(node_id,round_id,target_ip,target_port,config_revision,issued_at,deadline) VALUES(?,?,?,?,?,?,?)`, nodeID, round.ID, ip, port, revision, now.UnixNano(), deadline.UnixNano())
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT n.id FROM nodes n JOIN node_regions r ON r.node_id=n.id WHERE n.id<>? AND CASE WHEN r.override_name IS NULL THEN r.auto_mainland ELSE r.override_mainland END=1 AND r.checker_disabled=0 AND (n.maintenance_until IS NULL OR n.maintenance_until<=?) AND n.last_seen_at>? ORDER BY n.id`, nodeID, now.UnixNano(), now.Add(-90*time.Second).UnixNano())
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		if p.controls[id] != nil {
			ids = append(ids, id)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if net.ParseIP(ip) == nil || len(ids) == 0 {
		_, err = tx.ExecContext(ctx, "UPDATE checker_rounds SET status='completed' WHERE node_id=? AND round_id=?", nodeID, round.ID)
		return err
	}
	settings, err := loadSelfCheckSettings(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		sum := sha256.Sum256([]byte(nodeID + ":" + round.ID + ":" + id))
		task := checker.Task{ID: hex.EncodeToString(sum[:16]), RoundID: round.ID, TargetID: nodeID, CheckerID: id, TargetIP: ip, TargetPort: port, ConfigRevision: revision, ControlHost: settings.Sites[0], IssuedAt: now, Deadline: deadline}
		data, _ := json.Marshal(task)
		if _, err = tx.ExecContext(ctx, "INSERT INTO checker_tasks(id,node_id,round_id,checker_id,task) VALUES(?,?,?,?,?)", task.ID, nodeID, round.ID, id, data); err != nil {
			return err
		}
	}
	return nil
}
func (p *Panel) checkerTasks(ctx context.Context, id string) ([][]byte, error) {
	now := p.deps.Clock.Now().UnixNano()
	rows, err := p.db.QueryContext(ctx, `SELECT task FROM (
 SELECT t.task,r.issued_at,t.id FROM checker_tasks t JOIN checker_rounds r ON r.node_id=t.node_id AND r.round_id=t.round_id WHERE t.checker_id=? AND t.result IS NULL AND r.status='pending' AND r.deadline>?
 UNION ALL
 SELECT t.task,r.issued_at,t.id FROM entry_offline_tasks t JOIN entry_offline_checks r ON r.id=t.check_id WHERE t.checker_id=? AND t.result IS NULL AND r.outcome='pending' AND r.deadline>?
 ) ORDER BY issued_at,id LIMIT 8`, id, now, id, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := [][]byte{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		tasks = append(tasks, data)
	}
	return tasks, rows.Err()
}
func validCheckerResult(r checker.Result, t checker.Task) bool {
	if !reflect.DeepEqual(r.Task, t) || r.StartedAt.Before(t.IssuedAt.Add(-5*time.Minute)) || r.FinishedAt.Before(r.StartedAt) || r.FinishedAt.After(t.Deadline.Add(5*time.Minute)) || len(r.ExecutionError) > 200 {
		return false
	}
	validConn := func(c checker.Connection) bool {
		return c.DurationMS >= 0 && c.DurationMS <= 60000 && len(c.Error) <= 200 && !(c.Connected && c.Error != "")
	}
	if !validConn(r.Control) {
		return false
	}
	if r.Control.Address != "" {
		host, port, err := net.SplitHostPort(r.Control.Address)
		if err != nil || net.ParseIP(host) == nil || port != "443" {
			return false
		}
	}
	if r.Control.Connected && r.Control.Address == "" {
		return false
	}
	if r.Target != nil {
		if !r.Control.Connected || !validConn(*r.Target) || r.Target.Address != net.JoinHostPort(t.TargetIP, strconv.Itoa(t.TargetPort)) {
			return false
		}
	}
	return !r.Control.Connected || r.Target != nil || r.ExecutionError != ""
}
func (p *Panel) acceptCheckerResults(ctx context.Context, id string, batch [][]byte) ([]string, error) {
	if len(batch) > 8 {
		return nil, errors.New("检测点批次过大")
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ack := []string{}
	size := 0
	for _, data := range batch {
		size += len(data)
		if size > 48<<10 {
			return nil, errors.New("检测点结果过大")
		}
		var result checker.Result
		if err = json.Unmarshal(data, &result); err != nil {
			return nil, err
		}
		var assigned string
		var taskData []byte
		var offline bool
		err = tx.QueryRowContext(ctx, `SELECT checker_id,task,0 FROM checker_tasks WHERE id=? UNION ALL SELECT checker_id,task,1 FROM entry_offline_tasks WHERE id=?`, result.Task.ID, result.Task.ID).Scan(&assigned, &taskData, &offline)
		if err != nil || assigned != id {
			return nil, errors.New("未分配给当前节点的检测任务")
		}
		var task checker.Task
		if err = json.Unmarshal(taskData, &task); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(result.Task, task) {
			return nil, errors.New("检测点任务与分配内容不一致")
		}
		ack = append(ack, task.ID)
		// 截止以面板接收时间为准；正常超时完成的结果也只确认丢弃，不影响控制流。
		if !p.deps.Clock.Now().Before(task.Deadline) {
			continue
		}
		if !validCheckerResult(result, task) {
			return nil, errors.New("检测点证据与任务不一致")
		}
		if offline {
			_, err = tx.ExecContext(ctx, `UPDATE entry_offline_tasks SET result=? WHERE id=? AND result IS NULL AND EXISTS(SELECT 1 FROM entry_offline_checks c WHERE c.id=entry_offline_tasks.check_id AND c.outcome='pending')`, data, task.ID)
			if err == nil {
				err = p.finishOfflineCheck(ctx, tx, task.RoundID, false)
			}
			if err != nil {
				return nil, err
			}
			continue
		}
		_, err = tx.ExecContext(ctx, `UPDATE checker_tasks SET result=? WHERE id=? AND result IS NULL AND EXISTS(SELECT 1 FROM checker_rounds r WHERE r.node_id=checker_tasks.node_id AND r.round_id=checker_tasks.round_id AND r.status='pending')`, data, task.ID)
		if err != nil {
			return nil, err
		}
		if err = p.finishCheckerRound(ctx, tx, task.TargetID, task.RoundID, false); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	p.wakeCheckerSessions()
	if err = p.flushSelfCheckNotifications(ctx); err != nil {
		return nil, err
	}
	return ack, nil
}
func (p *Panel) finishCheckerRound(ctx context.Context, tx *sql.Tx, id, roundID string, expired bool) error {
	var status, ip, currentIP string
	var port, currentPort int
	var revision, currentRevision uint64
	err := tx.QueryRowContext(ctx, `SELECT r.status,r.target_ip,r.target_port,r.config_revision,COALESCE(nr.public_ipv4,''),n.check_port,n.desired_revision FROM checker_rounds r JOIN nodes n ON n.id=r.node_id LEFT JOIN node_regions nr ON nr.node_id=n.id WHERE r.node_id=? AND r.round_id=?`, id, roundID).Scan(&status, &ip, &port, &revision, &currentIP, &currentPort, &currentRevision)
	if err != nil || status != "pending" {
		return err
	}
	invalidated := ip != currentIP || port != currentPort || revision != currentRevision
	rows, err := tx.QueryContext(ctx, "SELECT result FROM checker_tasks WHERE node_id=? AND round_id=?", id, roundID)
	if err != nil {
		return err
	}
	total, received, valid := 0, 0, 0
	success, executionError := false, false
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			break
		}
		total++
		if data == nil {
			continue
		}
		received++
		var r checker.Result
		if err = json.Unmarshal(data, &r); err != nil {
			break
		}
		if r.ExecutionError != "" {
			executionError = true
			continue
		}
		if r.Control.Connected && r.Target != nil {
			valid++
			success = success || r.Target.Connected
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	final := "unknown"
	if !invalidated {
		if success {
			final = "normal"
		} else if received == total && valid > 0 && !executionError {
			final = "abnormal"
		}
		if !expired && !success && received < total {
			return nil
		}
	}
	if success && !invalidated {
		var issued int64
		if err = tx.QueryRowContext(ctx, "SELECT issued_at FROM checker_rounds WHERE node_id=? AND round_id=?", id, roundID).Scan(&issued); err != nil {
			return err
		}
		if err = p.recordDomesticEvidence(ctx, tx, id, "domestic_checker", time.Unix(0, issued)); err != nil {
			return err
		}
	}
	status = "completed"
	if invalidated {
		status = "invalidated"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE checker_rounds SET status=? WHERE node_id=? AND round_id=?", status, id, roundID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE self_check_rounds SET final_result=?,evidence_source='domestic_checker' WHERE node_id=? AND round_id=?", final, id, roundID); err != nil {
		return err
	}
	return p.recomputeCheckerState(ctx, tx, id, roundID)
}

// 按轮序重放最终结果，避免补判回包顺序改变连续计数，也不让旧轮覆盖较新的正常轮。
func (p *Panel) recomputeCheckerState(ctx context.Context, tx *sql.Tx, id, roundID string) error {
	var enabled bool
	var old string
	if err := tx.QueryRowContext(ctx, "SELECT enabled,state FROM node_self_check WHERE node_id=?", id).Scan(&enabled, &old); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	state, count, last, previousKey := "unknown", 0, uint64(0), ""
	err := tx.QueryRowContext(ctx, "SELECT state,consecutive,sequence,config_key FROM checker_state_baselines WHERE node_id=?", id).Scan(&state, &count, &last, &previousKey)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT raw,final_result,decision_eligible FROM self_check_rounds WHERE node_id=? AND sequence>? ORDER BY sequence", id, last)
	if err != nil {
		return err
	}
	var normal *selfcheck.Round
	for rows.Next() {
		var data []byte
		var final string
		var eligible int
		if err = rows.Scan(&data, &final, &eligible); err != nil {
			break
		}
		var r selfcheck.Round
		if err = json.Unmarshal(data, &r); err != nil {
			break
		}
		if eligible < 0 {
			continue
		}
		if r.Sequence != last+1 || previousKey != r.ConfigKey {
			count = 0
		}
		last, previousKey = r.Sequence, r.ConfigKey
		if eligible == 0 {
			count = 0
			continue
		}
		if final == "normal" && (normal == nil || r.StartedAt.After(normal.StartedAt)) {
			normal = &r
		}
		state, count = advanceSelfCheckState(state, count, final)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE node_self_check SET state=?,consecutive=? WHERE node_id=?", state, count, id); err != nil {
		return err
	}
	// 正常补判可能晚于后续异常轮到达；恢复证据按发生时间处理，
	// 不能要求重放结束时仍为正常，也不能让旧证据清掉较晚的暂停。
	if normal != nil {
		if err = p.rotationNormalEvidence(ctx, tx, id, normal.ID, normal.StartedAt); err != nil {
			return err
		}
	}
	kind, message := "", ""
	if old != "blocked" && state == "blocked" {
		kind, message = "node_blocked", "节点连续三轮最终结果异常，认定被墙（含检测点补判）"
	}
	if (old == "blocked" || old == "suspected") && state == "normal" {
		kind, message = "node_unblocked", "节点最终结果正常，已解除疑似被墙或被墙（检测点补判）"
	}
	if kind != "" {
		var name string
		var revision uint64
		if err = tx.QueryRowContext(ctx, "SELECT name,desired_revision FROM nodes WHERE id=?", id).Scan(&name, &revision); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO self_check_notifications(id,node_id,kind,occurred_at,config_revision,message) VALUES(?,?,?,?,?,?)", "checker:"+id+":"+roundID+":"+kind, id, kind, p.deps.Clock.Now().UnixNano(), revision, fmt.Sprintf("%s：%s", name, message))
	}
	return err
}
func advanceSelfCheckState(state string, count int, result string) (string, int) {
	switch result {
	case "unknown":
		count = 0
	case "normal":
		state, count = "normal", 0
	case "abnormal":
		count++
		if count >= 3 {
			count = 3
			state = "blocked"
		} else if state != "blocked" {
			state = "suspected"
		}
	}
	return state, count
}
func (p *Panel) wakeCheckerSessions() {
	for _, s := range p.controls {
		select {
		case s.changed <- struct{}{}:
		default:
		}
	}
}
func (p *Panel) expireCheckerRounds(ctx context.Context) error {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 同时检查地址/端口/版本变化，即使 IP 随后变回也不会复活旧任务。
	rows, err := tx.QueryContext(ctx, `SELECT r.node_id,r.round_id FROM checker_rounds r JOIN nodes n ON n.id=r.node_id LEFT JOIN node_regions nr ON nr.node_id=n.id WHERE r.status='pending' AND (r.deadline<=? OR r.target_ip<>COALESCE(nr.public_ipv4,'') OR r.target_port<>n.check_port OR r.config_revision<>n.desired_revision)`, p.deps.Clock.Now().UnixNano())
	if err != nil {
		return err
	}
	ids := [][2]string{}
	for rows.Next() {
		var v [2]string
		if err = rows.Scan(&v[0], &v[1]); err != nil {
			break
		}
		ids = append(ids, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range ids {
		if err = p.finishCheckerRound(ctx, tx, v[0], v[1], true); err != nil {
			return err
		}
	}
	return tx.Commit()
}
