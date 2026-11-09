package panel

import (
	"context"
	"database/sql"
	"time"
)

const (
	entryFailbackSuccesses = 6
	entryMinimumResidence  = 2 * time.Minute
)

type entryFailbackWindow struct {
	node, ip  string
	version   int64
	slot      time.Time
	successes int
}

type entryFailbackPublication struct {
	group, preferred, active string
	version                  int64
	written                  sql.NullInt64
	paused                   bool
}

const failbackPublicationColumns = `s.group_id,m.node_id,s.active_node_id,s.desired_version,s.written_at,g.auto_failback_paused`
const failbackPublicationFrom = ` FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id
 JOIN entry_group_members m ON m.group_id=s.group_id AND m.position=(SELECT MIN(position) FROM entry_group_members WHERE group_id=s.group_id)`

func scanFailbackPublication(row scanner) (entryFailbackPublication, error) {
	var v entryFailbackPublication
	err := row.Scan(&v.group, &v.preferred, &v.active, &v.version, &v.written, &v.paused)
	return v, err
}

// 只由十秒候选定时器调用，持 entryMu → controlMu。页面读取、故障检查
// 和提交前复核均不累计成功；重启或读取失败后从零重新收集样本。
func (p *Panel) checkEntryFailbacks(ctx context.Context, states map[[2]string]entryMemberState, previous map[string]entryFailbackWindow) (map[string]entryFailbackWindow, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT "+failbackPublicationColumns+failbackPublicationFrom)
	if err != nil {
		return nil, err
	}
	var publications []entryFailbackPublication
	for rows.Next() {
		v, e := scanFailbackPublication(rows)
		if e != nil {
			err = e
			break
		}
		publications = append(publications, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	next := make(map[string]entryFailbackWindow)
	for _, v := range publications {
		allowed, err := strategyThreeFailbackAllowed(ctx, p.db, v.group, v.version)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		state := states[[2]string{v.group, v.preferred}]
		now := p.deps.Clock.Now()
		if v.active == v.preferred || !state.Candidate || state.CheckedAt == nil || now.Before(*state.CheckedAt) || now.Sub(*state.CheckedAt) > entryCandidateInterval || state.DomesticEvidence == nil || !now.Before(state.DomesticEvidence.ExpiresAt) {
			continue
		}
		// 每个十秒采样时段最多一次。跳过的时段不会补样本；按时段比较
		// 而非纳秒级间隔，避免正常调度抖动把相邻检查误判为缺样。
		slot := state.CheckedAt.Truncate(entryCandidateInterval)
		w := previous[v.group]
		ip := state.DomesticEvidence.PublicIPv4
		if w.node != v.preferred || w.ip != ip || w.version != v.version || !slot.Equal(w.slot.Add(entryCandidateInterval)) {
			w = entryFailbackWindow{node: v.preferred, ip: ip, version: v.version}
		}
		w.slot, w.successes = slot, min(w.successes+1, entryFailbackSuccesses)
		next[v.group] = w
		if w.successes < entryFailbackSuccesses || v.paused || !v.written.Valid || now.Sub(time.Unix(0, v.written.Int64)) < entryMinimumResidence {
			continue
		}
		allowed, err = recoveryGroupAllowed(ctx, p.db, v.group)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		_, err = p.submitEntryFailback(ctx, v, state)
		if err != nil {
			return nil, err
		}
		delete(next, v.group)
	}
	return next, nil
}

func (p *Panel) submitEntryFailback(ctx context.Context, observed entryFailbackPublication, state entryMemberState) (bool, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	v, err := scanFailbackPublication(tx.QueryRowContext(ctx, "SELECT "+failbackPublicationColumns+failbackPublicationFrom+" WHERE s.group_id=?", observed.group))
	if err != nil {
		return false, err
	}
	if v.version != observed.version || v.preferred != observed.preferred || v.active == v.preferred || v.paused || !v.written.Valid || p.deps.Clock.Now().Sub(time.Unix(0, v.written.Int64)) < entryMinimumResidence {
		return false, nil
	}
	allowed, err := strategyThreeFailbackAllowed(ctx, tx, v.group, v.version)
	if err != nil || !allowed {
		return false, err
	}
	fresh, err := p.offlineCandidateCurrent(ctx, tx, state)
	if err != nil || !fresh {
		return false, err
	}
	_, err = p.submitEntryActionTx(ctx, tx, EntryActionRequest{GroupID: v.group, NodeID: v.preferred, IPv4: state.DomesticEvidence.PublicIPv4, Kind: "failback", ExpectedVersion: v.version})
	if err != nil {
		return false, err
	}
	// 随机源或磁盘停顿也可能跨过证据期限；失效时连同动作和事件回滚。
	fresh, err = p.offlineCandidateCurrent(ctx, tx, state)
	if err != nil || !fresh {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	p.coordinator.wake(v.group)
	return true, nil
}
