package panel

import (
	"context"
	"database/sql"
	"time"
)

// 迁移23：旧历史没有地址归属，不回填；等待一次新鲜、已验证的成功。
const readinessMigration = `ALTER TABLE node_regions ADD COLUMN domestic_since INTEGER NOT NULL DEFAULT 0;
CREATE TABLE node_domestic_evidence (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 public_ipv4 TEXT NOT NULL,
 observed_at INTEGER NOT NULL,
 source TEXT NOT NULL
);
CREATE TRIGGER domestic_evidence_invalidate_ip AFTER UPDATE OF public_ipv4 ON node_regions
WHEN OLD.public_ipv4<>NEW.public_ipv4 BEGIN
 DELETE FROM node_domestic_evidence WHERE node_id=NEW.node_id;
END;`

const domesticEvidenceTTL = 5 * time.Minute

type domesticEvidence struct {
	PublicIPv4 string    `json:"public_ipv4"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Source     string    `json:"source"`
}

// 调用者只传已验证成功的自测或固定目标检测点结果；与接收证据的事务一起提交。
// 按发生时间去重，补报、重复上报不能延长五分钟期限。
func (p *Panel) recordDomesticEvidence(ctx context.Context, tx *sql.Tx, id, source string, at time.Time) error {
	return p.recordAddressDomesticEvidence(ctx, tx, id, "", source, at)
}

// 换 IP 复检已在动作协议中绑定新地址，不受面板稍后收到地址报告的时间影响。
func (p *Panel) recordAddressDomesticEvidence(ctx context.Context, tx *sql.Tx, id, ip, source string, at time.Time) error {
	now := p.deps.Clock.Now()
	if at.After(now) || !at.Add(domesticEvidenceTTL).After(now) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO node_domestic_evidence(node_id,public_ipv4,observed_at,source)
 SELECT node_id,public_ipv4,?,? FROM node_regions WHERE node_id=? AND public_ipv4<>'' AND ((?='' AND domestic_since<=?) OR public_ipv4=?)
 ON CONFLICT(node_id) DO UPDATE SET public_ipv4=excluded.public_ipv4,observed_at=excluded.observed_at,source=excluded.source WHERE excluded.observed_at>node_domestic_evidence.observed_at`, at.UnixNano(), source, id, ip, at.UnixNano(), ip)
	return err
}

func (p *Panel) recordNormalSelfCheckEvidence(ctx context.Context, tx *sql.Tx, id string, revision uint64, started time.Time) error {
	var desired uint64
	if err := tx.QueryRowContext(ctx, "SELECT desired_revision FROM nodes WHERE id=?", id).Scan(&desired); err != nil {
		return err
	}
	if revision != desired {
		return nil
	}
	return p.recordDomesticEvidence(ctx, tx, id, "self_check", started)
}
