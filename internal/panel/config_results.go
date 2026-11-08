package panel

import (
	"context"
	"database/sql"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
)

const configResultsMigration = `CREATE TABLE node_rule_reports (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id),
 revision INTEGER NOT NULL,
 digest TEXT NOT NULL,
 revocations_applied INTEGER NOT NULL
);
CREATE TABLE node_rule_results (
 node_id TEXT NOT NULL REFERENCES nodes(id),
 rule_id TEXT NOT NULL,
 entry_group_id TEXT NOT NULL,
 listen_port INTEGER NOT NULL,
 applied_revision INTEGER NOT NULL,
 apply_error TEXT NOT NULL,
 PRIMARY KEY(node_id,rule_id)
);`

// 逐规则结果与完整确认共享事务；独立事件、探针和旧快照均不能写入。
func storeRuleResults(ctx context.Context, tx *sql.Tx, id string, report *controlpb.NodeReport, desired uint64, expectedDigest string, alreadyConfirmed bool) error {
	full := report.AppliedRevision == desired && report.AppliedDigest == expectedDigest && report.ApplyError == ""
	partial := report.AttemptedRevision == desired && report.AttemptedDigest == expectedDigest && !alreadyConfirmed
	if desired == 0 || expectedDigest == "" || !full && !partial {
		return nil
	}
	var data []byte
	if err := tx.QueryRowContext(ctx, "SELECT snapshot FROM node_snapshots WHERE node_id=?", id).Scan(&data); err != nil {
		return err
	}
	snapshot := &controlpb.ConfigSnapshot{}
	if err := proto.Unmarshal(data, snapshot); err != nil {
		return err
	}
	results := report.RuleResults
	configs := map[string]*controlpb.ForwardRule{}
	for _, r := range snapshot.Rules {
		configs[r.Id] = r
	}
	if full {
		// 完整成功的摘要确认也支持上一版节点，以及同进程丢失 ACK 后的心跳补确认。
		results = nil
		for _, r := range snapshot.Rules {
			results = append(results, &controlpb.RuleApplyResult{RuleId: r.Id, AppliedRevision: desired})
		}
	} else {
		expected := map[string]bool{}
		for _, r := range snapshot.Rules {
			expected[r.Id] = true
		}
		if len(results) != len(expected) {
			return status.Error(codes.InvalidArgument, "逐规则报告不完整")
		}
		for _, r := range results {
			if r == nil || !expected[r.RuleId] || r.AppliedRevision > desired || len(r.ApplyError) > 1024 || r.AppliedRevision == 0 && r.ApplyError == "" {
				return status.Error(codes.InvalidArgument, "逐规则报告无效")
			}
			delete(expected, r.RuleId)
		}
	}
	if full || report.RevocationsApplied {
		if _, err := tx.ExecContext(ctx, "DELETE FROM node_rule_results WHERE node_id=?", id); err != nil {
			return err
		}
	}
	for _, r := range results {
		config := configs[r.RuleId]
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_rule_results(node_id,rule_id,entry_group_id,listen_port,applied_revision,apply_error) VALUES(?,?,?,?,?,?)
 ON CONFLICT(node_id,rule_id) DO UPDATE SET
 entry_group_id=CASE WHEN ? THEN excluded.entry_group_id ELSE node_rule_results.entry_group_id END,
 listen_port=CASE WHEN ? THEN excluded.listen_port ELSE node_rule_results.listen_port END,
 applied_revision=excluded.applied_revision,apply_error=excluded.apply_error`, id, r.RuleId, config.EntryGroupId, config.ListenPort, r.AppliedRevision, r.ApplyError, full || report.RevocationsApplied, full || report.RevocationsApplied); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO node_rule_reports(node_id,revision,digest,revocations_applied) VALUES(?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET revision=excluded.revision,digest=excluded.digest,revocations_applied=excluded.revocations_applied`, id, desired, expectedDigest, full || report.RevocationsApplied)
	return err
}
