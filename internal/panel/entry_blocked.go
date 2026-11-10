package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
)

type blockedEntry struct {
	group, node, ip, incident, policy string
	version                           int64
	crossRotation                     bool
}

// 原始自测和检测点补判共同产出的被墙状态只在此消费，不推进其他上报状态。
const blockedEntryFrom = ` FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id
 JOIN node_self_check c ON c.node_id=s.active_node_id
 JOIN nodes n ON n.id=c.node_id JOIN node_regions r ON r.node_id=n.id
 JOIN entry_blocked_incidents i ON i.node_id=n.id
 WHERE g.publication_mode='primary_backup' AND g.blocked_policy IN ('switch_entry','switch_then_rotate') AND c.enabled=1 AND c.state='blocked'
 AND (n.maintenance_until IS NULL OR n.maintenance_until<=?)
 AND s.expected_value=r.public_ipv4 AND i.target_ip=r.public_ipv4
 AND i.occurred_at>=COALESCE((SELECT MAX(restored_at) FROM recovery_state),0)
 AND NOT EXISTS(SELECT 1 FROM entry_blocked_rotations f JOIN entry_blocked_decisions d ON d.id=f.decision_id
 WHERE g.blocked_policy='switch_then_rotate' AND d.group_id=g.id AND d.node_id=s.active_node_id
 AND f.expected_version=s.desired_version AND d.created_at>=COALESCE((SELECT MAX(restored_at) FROM recovery_state),0))`

// 沿用 entryMu → controlMu，只读写本地数据。外部 DNS 写入仍由统一协调器执行。
func (p *Panel) checkBlockedEntries(ctx context.Context) error {
	p.entryMu.Lock()
	defer p.entryMu.Unlock()
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	if err := p.refreshBlockedEntryIncidents(ctx); err != nil {
		return err
	}
	rows, err := p.db.QueryContext(ctx, "SELECT s.group_id,s.active_node_id,s.expected_value,s.desired_version,i.incident_id,g.blocked_policy"+blockedEntryFrom, p.deps.Clock.Now().UnixNano())
	if err != nil {
		return err
	}
	var entries []blockedEntry
	for rows.Next() {
		var e blockedEntry
		if err = rows.Scan(&e.group, &e.node, &e.ip, &e.version, &e.incident, &e.policy); err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(entries) == 0 {
		return err
	}
	states, err := p.checkEntryCandidates(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		allowed, err := recoveryGroupAllowed(ctx, p.db, e.group)
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		inputs, err := p.loadEntryReadinessInputs(ctx, e.node, p.deps.Clock.Now())
		if err != nil {
			return err
		}
		// 新地址正常复检等较新的成功证据可覆盖旧被墙状态，沿用候选计算口径。
		if !inputs.blocked {
			continue
		}
		if err = p.submitBlockedSwitch(ctx, e, states); err != nil {
			return err
		}
	}
	return nil
}

// 换 IP 不会推进原始自测状态，持续 blocked 也不会再次产生 node_blocked。
// 当前地址至少三轮连续、新鲜归属的异常后，才替换旧地址（或旧备份）的事故。
func (p *Panel) refreshBlockedEntryIncidents(ctx context.Context) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO entry_blocked_incidents(node_id,incident_id,target_ip,occurred_at)
 SELECT c.node_id,'address-blocked:'||c.node_id||':'||r.public_ipv4||':'||r.domestic_since,r.public_ipv4,
 (SELECT MAX(finished_at) FROM self_check_rounds WHERE node_id=c.node_id AND decision_eligible=1)
 FROM node_self_check c JOIN node_regions r ON r.node_id=c.node_id
 LEFT JOIN entry_blocked_incidents i ON i.node_id=c.node_id
 WHERE c.enabled=1 AND c.state='blocked' AND c.consecutive>=3 AND r.public_ipv4<>''
 AND (i.node_id IS NULL OR i.target_ip<>r.public_ipv4 OR i.occurred_at<COALESCE((SELECT MAX(restored_at) FROM recovery_state),0))
 AND 3=(SELECT CASE WHEN MAX(sequence)-MIN(sequence)=2 THEN COUNT(*) ELSE 0 END FROM
  (SELECT sequence,started_at,final_result,decision_eligible FROM self_check_rounds WHERE node_id=c.node_id ORDER BY sequence DESC LIMIT 3)
  WHERE decision_eligible=1 AND final_result='abnormal' AND started_at>=MAX(r.domestic_since,COALESCE((SELECT MAX(restored_at) FROM recovery_state),0)))
 ON CONFLICT(node_id) DO UPDATE SET incident_id=excluded.incident_id,target_ip=excluded.target_ip,occurred_at=excluded.occurred_at`)
	return err
}

func (p *Panel) submitBlockedSwitch(ctx context.Context, e blockedEntry, states map[[2]string]entryMemberState) error {
	rows, err := p.db.QueryContext(ctx, "SELECT node_id FROM entry_group_members WHERE group_id=? ORDER BY position", e.group)
	if err != nil {
		return err
	}
	var candidate entryMemberState
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		state := states[[2]string{e.group, id}]
		if candidate.NodeID == "" && id != e.node && state.Candidate && state.DomesticEvidence != nil {
			candidate = state
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := p.blockedEntryCurrent(ctx, tx, e)
	if err != nil || !current {
		return err
	}
	identity := fmt.Sprintf("%s:%s:%d", e.group, e.incident, e.version)
	// 策略②等待期间改选③须形成自己的决策，不复用“不换 IP”的通知；保留②原去重键。
	if e.policy == "switch_then_rotate" {
		identity += ":switch_then_rotate"
	}
	sum := sha256.Sum256([]byte(identity))
	id := fmt.Sprintf("entry-blocked:%x", sum)
	_, err = tx.ExecContext(ctx, `INSERT INTO entry_blocked_decisions(id,group_id,node_id,incident_id,desired_version,outcome,created_at,policy,cross_rotation) VALUES(?,?,?,?,?,'no_candidate',?,?,?)
 ON CONFLICT(id) DO UPDATE SET cross_rotation=MAX(cross_rotation,excluded.cross_rotation)`, id, e.group, e.node, e.incident, e.version, p.deps.Clock.Now().UnixNano(), e.policy, e.crossRotation)
	if err != nil {
		return err
	}
	if candidate.NodeID == "" {
		if e.policy == "switch_then_rotate" {
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO entry_blocked_rotations(decision_id,expected_version) VALUES(?,?)", id, e.version); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	fresh, err := p.offlineCandidateCurrent(ctx, tx, candidate)
	if err != nil || !fresh {
		return err
	}
	a, err := p.submitEntryActionTx(ctx, tx, EntryActionRequest{GroupID: e.group, NodeID: candidate.NodeID, IPv4: candidate.DomesticEvidence.PublicIPv4, Kind: "switch", ExpectedVersion: e.version})
	if err != nil {
		return err
	}
	// 动作 ID 分配或磁盘停顿可能跨过候选期限/维护起点；失效则回滚全部状态。
	fresh, err = p.offlineCandidateCurrent(ctx, tx, candidate)
	if err != nil || !fresh {
		return err
	}
	// 提交已更换活动入口，不再按旧发布复核；时间变化只需复核原节点维护状态。
	var maintenance bool
	if err = tx.QueryRowContext(ctx, "SELECT maintenance_until IS NOT NULL AND maintenance_until>? FROM nodes WHERE id=?", p.deps.Clock.Now().UnixNano(), e.node).Scan(&maintenance); err != nil || maintenance {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE entry_blocked_decisions SET outcome='submitted',action_id=? WHERE id=?", a.ID, id); err != nil {
		return err
	}
	if e.policy == "switch_then_rotate" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO entry_blocked_rotations(decision_id,expected_version) VALUES(?,?)
 ON CONFLICT(decision_id) DO UPDATE SET expected_version=excluded.expected_version`, id, a.Version); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err == nil {
		p.coordinator.wake(e.group)
	}
	return err
}

func (p *Panel) blockedEntryCurrent(ctx context.Context, tx *sql.Tx, e blockedEntry) (bool, error) {
	var count int
	if e.crossRotation {
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id
 JOIN nodes n ON n.id=s.active_node_id JOIN node_regions r ON r.node_id=n.id
 WHERE s.group_id=? AND s.active_node_id=? AND s.expected_value=? AND s.desired_version=? AND g.blocked_policy=?
 AND r.public_ipv4=s.expected_value AND (n.maintenance_until IS NULL OR n.maintenance_until<=?)`, e.group, e.node, e.ip, e.version, e.policy, p.deps.Clock.Now().UnixNano()).Scan(&count)
		return count == 1, err
	}
	err := tx.QueryRowContext(ctx, "SELECT count(*)"+blockedEntryFrom+" AND s.group_id=? AND s.active_node_id=? AND s.expected_value=? AND s.desired_version=? AND i.incident_id=? AND g.blocked_policy=?", p.deps.Clock.Now().UnixNano(), e.group, e.node, e.ip, e.version, e.incident, e.policy).Scan(&count)
	return count == 1, err
}
