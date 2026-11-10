package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// 与业务表无外键：展示历史不能阻碍删除节点，也不能级联改变业务动作。
const incidentsMigration = `CREATE TABLE operation_incidents (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,source_key TEXT NOT NULL,kind TEXT NOT NULL,title TEXT NOT NULL,
 impact TEXT NOT NULL,handling TEXT NOT NULL,started_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,
 ended_at INTEGER,closure TEXT NOT NULL DEFAULT '',refs TEXT NOT NULL,reasons TEXT NOT NULL
);
CREATE UNIQUE INDEX operation_incidents_open ON operation_incidents(source_key) WHERE ended_at IS NULL;
CREATE INDEX operation_incidents_history ON operation_incidents(ended_at,sequence);
CREATE INDEX operation_incidents_kind ON operation_incidents(kind,sequence);
CREATE TABLE operation_timeline (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,incident_id INTEGER NOT NULL REFERENCES operation_incidents(sequence) ON DELETE CASCADE,
 fact_key TEXT NOT NULL,stage TEXT NOT NULL,message TEXT NOT NULL,source TEXT NOT NULL,version INTEGER NOT NULL,
 occurred_at INTEGER,observed_at INTEGER NOT NULL,UNIQUE(incident_id,fact_key)
);
CREATE TABLE operation_projection (
 id INTEGER PRIMARY KEY CHECK(id=1),synced_at INTEGER NOT NULL DEFAULT 0
);
INSERT INTO operation_projection(id) VALUES(1);
CREATE TABLE operation_processed (source_key TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,ended INTEGER NOT NULL DEFAULT 0);
`
const incidentColumns = `sequence,source_key,kind,title,impact,handling,started_at,updated_at,ended_at,closure,refs,reasons`

func scanIncident(row scanner) (incidentRecord, error) {
	var r incidentRecord
	var id, started, updated int64
	var ended sql.NullInt64
	var refs, reasons string
	err := row.Scan(&id, &r.sourceKey, &r.Kind, &r.Title, &r.Impact, &r.Handling, &started, &updated, &ended, &r.Closure, &refs, &reasons)
	if err != nil {
		return r, err
	}
	r.ID = strconv.FormatInt(id, 10)
	r.StartedAt = time.Unix(0, started).UTC()
	r.UpdatedAt = time.Unix(0, updated).UTC()
	r.EndedAt = operationTime(ended)
	if err = json.Unmarshal([]byte(refs), &r.Refs); err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(reasons), &r.Reasons)
	return r, err
}
func incidentFingerprint(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func saveIncidentObservation(ctx context.Context, tx *sql.Tx, o incidentObservation, now time.Time) error {
	if o.Refs == nil {
		o.Refs = []incidentRef{}
	}
	if o.Reasons == nil {
		o.Reasons = []string{}
	}
	// 优先活动记录；单次业务事故与迟到结束事实允许补充已结束的同一记录。
	r, err := scanIncident(tx.QueryRowContext(ctx, "SELECT "+incidentColumns+" FROM operation_incidents WHERE source_key=? ORDER BY (ended_at IS NULL) DESC,sequence DESC LIMIT 1", o.Key))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	missing := errors.Is(err, sql.ErrNoRows)
	if !missing {
		seen := map[string]bool{}
		for _, ref := range o.Refs {
			seen[ref.Kind+":"+ref.ID] = true
		}
		for _, ref := range r.Refs {
			if !seen[ref.Kind+":"+ref.ID] {
				o.Refs = append(o.Refs, ref)
			}
		}
	}
	// 采样时间变化不是新状态。事实首次写入时仍保留其发生和观察时间。
	stable := o
	stable.Entries = append([]incidentEntry(nil), o.Entries...)
	for i := range stable.Entries {
		stable.Entries[i].At = nil
	}
	fingerprint := incidentFingerprint(stable)
	var old string
	var processedEnd bool
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,ended FROM operation_processed WHERE source_key=?", o.Key).Scan(&old, &processedEnd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if old == fingerprint || (missing && processedEnd && (strings.HasPrefix(o.Key, "blocked:") || strings.HasPrefix(o.Key, "rotation:"))) {
		return nil
	}
	if !missing && r.EndedAt != nil && o.Closure == "" {
		if strings.HasPrefix(o.Key, "blocked:") || strings.HasPrefix(o.Key, "rotation:") {
			// 同一业务事故不能因迟到事实重开。保留原结束语义，仍可补充时间线。
			o.Closure = r.Closure
			o.Impact = r.Impact
		} else {
			missing = true
		}
	}
	if missing {
		if o.Closure != "" || o.ExistingOnly {
			return nil
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO operation_incidents(source_key,kind,title,impact,handling,started_at,updated_at,refs,reasons) VALUES(?,?,?,?,?,?,?,?,?)`, o.Key, o.Kind, o.Title, o.Impact, o.Handling, now.UnixNano(), now.UnixNano(), watchJSON(o.Refs), watchJSON(o.Reasons))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		r.ID = strconv.FormatInt(id, 10)
	}
	var end any
	if o.Closure != "" {
		end = now.UnixNano()
		if r.EndedAt != nil && !missing {
			end = r.EndedAt.UnixNano()
		}
		o.Handling = "ended"
	}
	_, err = tx.ExecContext(ctx, `UPDATE operation_incidents SET title=?,impact=?,handling=?,updated_at=?,ended_at=?,closure=?,refs=?,reasons=? WHERE sequence=?`, o.Title, o.Impact, o.Handling, now.UnixNano(), end, o.Closure, watchJSON(o.Refs), watchJSON(o.Reasons), r.ID)
	if err != nil {
		return err
	}
	for _, entry := range o.Entries {
		var at any
		if entry.At != nil {
			at = entry.At.UnixNano()
		}
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO operation_timeline(incident_id,fact_key,stage,message,source,version,occurred_at,observed_at) VALUES(?,?,?,?,?,?,?,?)`, r.ID, entry.Key, entry.Stage, entry.Message, entry.Source, entry.Version, at, now.UnixNano())
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO operation_processed(source_key,fingerprint,ended) VALUES(?,?,?) ON CONFLICT(source_key) DO UPDATE SET fingerprint=excluded.fingerprint,ended=excluded.ended`, o.Key, fingerprint, o.Closure != "")
	return err
}

func (p *Panel) incidentProjectionStatus(ctx context.Context) (*time.Time, string, *int) {
	p.operationsMu.Lock()
	message := p.operationsError
	p.operationsMu.Unlock()
	var at int64
	var count int
	if err := p.db.QueryRowContext(ctx, "SELECT synced_at FROM operation_projection WHERE id=1").Scan(&at); err != nil {
		return nil, "故障记录读取失败", nil
	}
	if at == 0 {
		return nil, "故障记录尚未完成首次整理", nil
	}
	if err := p.db.QueryRowContext(ctx, "SELECT count(*) FROM operation_incidents WHERE ended_at IS NULL AND handling='manual'").Scan(&count); err != nil {
		return nil, "故障记录读取失败", nil
	}
	t := time.Unix(0, at).UTC()
	if age := p.deps.Clock.Now().Sub(t); message == "" && (age < 0 || age > 15*time.Second) {
		message = "故障记录整理延迟"
	}
	return &t, message, &count
}
