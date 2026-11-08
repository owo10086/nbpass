package panel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"
)

const loginLockDuration = 15 * time.Minute

type loginBudget struct {
	failures    int
	lockedUntil int64
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadLoginBudget(ctx context.Context, db rowQuerier, source string, now time.Time) (loginBudget, error) {
	var budget loginBudget
	err := db.QueryRowContext(ctx, "SELECT failures, locked_until FROM auth_login_failures WHERE source = ?", source).Scan(&budget.failures, &budget.lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return loginBudget{}, nil
	}
	if err != nil {
		return loginBudget{}, err
	}
	if budget.lockedUntil != 0 && budget.lockedUntil <= now.UnixNano() {
		return loginBudget{}, nil
	}
	return budget, nil
}

func (p *Panel) lockedLogin(w http.ResponseWriter, until int64) bool {
	remaining := time.Unix(0, until).Sub(p.deps.Clock.Now())
	if remaining <= 0 {
		return false
	}
	w.Header().Set("Retry-After", strconv.FormatInt(int64((remaining+time.Second-1)/time.Second), 10))
	problem(w, 429, "密码连续错误 5 次，请在 15 分钟锁定期结束后重试")
	return true
}

func (p *Panel) admitLogin(w http.ResponseWriter, r *http.Request) (string, bool) {
	source, err := p.loginSource(r)
	if err != nil {
		problem(w, 503, "无法确认登录来源")
		return "", false
	}
	budget, err := loadLoginBudget(r.Context(), p.db, source, p.deps.Clock.Now())
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return "", false
	}
	return source, !p.lockedLogin(w, budget.lockedUntil)
}

// 校验后的计数、锁定及会话创建串行提交。并发成功也不能越过已触发的锁定，
// INSERT 的密码哈希条件阻止改密或本机重置前已开始的登录创建新会话。
func (p *Panel) finishLogin(ctx context.Context, source, hash, token string, valid bool) (bool, int64, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	now := p.deps.Clock.Now()
	budget, err := loadLoginBudget(ctx, tx, source, now)
	if err != nil {
		return false, 0, err
	}
	if budget.lockedUntil > now.UnixNano() {
		return false, budget.lockedUntil, nil
	}
	if !valid {
		budget.failures++
		if budget.failures >= 5 {
			budget.lockedUntil = now.Add(loginLockDuration).UnixNano()
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO auth_login_failures(source, failures, locked_until) VALUES(?, ?, ?)
			ON CONFLICT(source) DO UPDATE SET failures = excluded.failures, locked_until = excluded.locked_until`, source, budget.failures, budget.lockedUntil)
		if err != nil {
			return false, 0, err
		}
		return false, budget.lockedUntil, tx.Commit()
	}
	digest := sha256.Sum256([]byte(token))
	result, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash, created_at, last_seen_at)
		SELECT ?, ?, ? FROM administrator WHERE id = 1 AND password_hash = ?`, digest[:], now.Unix(), now.Unix(), hash)
	if err != nil {
		return false, 0, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, 0, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM auth_login_failures WHERE source = ?", source); err != nil {
		return false, 0, err
	}
	return true, 0, tx.Commit()
}
