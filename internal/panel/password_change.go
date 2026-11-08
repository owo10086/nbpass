package panel

import (
	"crypto/sha256"
	"net/http"
	"time"
)

func (p *Panel) changePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validPassword(body.NewPassword) {
		problem(w, 400, "新密码须为 12–72 字节")
		return
	}
	source, ok := p.admitLogin(w, r)
	if !ok || !p.passwordSlot(w) {
		return
	}
	defer func() { <-p.passwordSlots }()
	var previous string
	if err := p.db.QueryRowContext(r.Context(), "SELECT password_hash FROM administrator WHERE id = 1").Scan(&previous); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if len(body.CurrentPassword) > 72 || !p.deps.Passwords.Verify(previous, body.CurrentPassword) {
		_, until, err := p.finishLogin(r.Context(), source, previous, "", false)
		if err != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
		if !p.lockedLogin(w, until) {
			problem(w, 401, "当前密码错误")
		}
		return
	}
	hash, err := p.deps.Passwords.Hash(body.NewPassword)
	if err != nil {
		problem(w, 500, "无法保存密码")
		return
	}
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	now := p.deps.Clock.Now()
	budget, err := loadLoginBudget(r.Context(), tx, source, now)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if p.lockedLogin(w, budget.lockedUntil) {
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		problem(w, 401, "请重新登录面板")
		return
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	result, err := tx.ExecContext(r.Context(), `UPDATE administrator SET password_hash = ? WHERE id = 1 AND password_hash = ?
		AND EXISTS (SELECT 1 FROM sessions WHERE token_hash = ? AND last_seen_at > ? AND created_at > ?)`,
		hash, previous, digest[:], now.Add(-24*time.Hour).Unix(), now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if count == 0 {
		problem(w, 401, "密码或会话已变更，请重新登录")
		return
	}
	if err := clearPasswordSessions(r.Context(), tx); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if err := tx.Commit(); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	clearSession(w)
	respond(w, 204, nil)
}
