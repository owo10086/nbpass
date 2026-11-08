package panel

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"
)

const sessionCookie = "__Host-nbpass_session"

func (p *Panel) configured(r *http.Request) (bool, error) {
	var count int
	err := p.db.QueryRowContext(r.Context(), "SELECT count(*) FROM administrator").Scan(&count)
	return count != 0, err
}

func (p *Panel) status(w http.ResponseWriter, r *http.Request) {
	configured, err := p.configured(r)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	valid, err := p.authenticate(r)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	respond(w, 200, map[string]bool{"setup_required": !configured, "authenticated": valid})
}

func (p *Panel) passwordSlot(w http.ResponseWriter) bool {
	select {
	case p.passwordSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		problem(w, http.StatusTooManyRequests, "密码校验繁忙，请稍后重试")
		return false
	}
}

func (p *Panel) setup(w http.ResponseWriter, r *http.Request) {
	configured, err := p.configured(r)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if configured {
		problem(w, http.StatusConflict, "管理员密码已设置")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Password) < 12 || len(body.Password) > 72 {
		problem(w, 400, "密码须为 12–72 字节")
		return
	}
	if !p.passwordSlot(w) {
		return
	}
	defer func() { <-p.passwordSlots }()
	hash, err := p.deps.Passwords.Hash(body.Password)
	if err != nil {
		problem(w, 500, "无法保存密码")
		return
	}
	result, err := p.db.ExecContext(r.Context(), "INSERT INTO administrator(id, password_hash) VALUES(1, ?) ON CONFLICT(id) DO NOTHING", hash)
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
		problem(w, 409, "管理员密码已设置")
		return
	}
	respond(w, 201, map[string]bool{"setup_required": false})
}

func (p *Panel) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Password) > 72 {
		problem(w, 401, "密码错误")
		return
	}
	if !p.passwordSlot(w) {
		return
	}
	defer func() { <-p.passwordSlots }()
	var hash string
	if err := p.db.QueryRowContext(r.Context(), "SELECT password_hash FROM administrator WHERE id = 1").Scan(&hash); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	if !p.deps.Passwords.Verify(hash, body.Password) {
		problem(w, 401, "密码错误")
		return
	}
	token := make([]byte, 32)
	if _, err := io.ReadFull(p.deps.Random, token); err != nil {
		problem(w, 500, "无法创建会话")
		return
	}
	value := base64.RawURLEncoding.EncodeToString(token)
	digest := sha256.Sum256([]byte(value))
	now := p.deps.Clock.Now()
	if _, err := p.db.ExecContext(r.Context(), "INSERT INTO sessions(token_hash, created_at, last_seen_at) VALUES(?, ?, ?)", digest[:], now.Unix(), now.Unix()); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 60 * 60, Expires: now.Add(7 * 24 * time.Hour)})
	respond(w, 200, map[string]bool{"authenticated": true})
}

func (p *Panel) authenticate(r *http.Request) (bool, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) != 43 {
		return false, nil
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	now := p.deps.Clock.Now()
	var created int64
	// 条件更新同时检查到期和撤销，避免并发请求把已撤销的会话写回来。
	err = p.db.QueryRowContext(r.Context(), `UPDATE sessions SET last_seen_at = MAX(last_seen_at, ?)
		WHERE token_hash = ? AND last_seen_at > ? AND created_at > ? RETURNING created_at`,
		now.Unix(), digest[:], now.Add(-24*time.Hour).Unix(), now.Add(-7*24*time.Hour).Unix()).Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		problem(w, 401, "请登录面板")
		return
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	if _, err := p.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE token_hash = ?", digest[:]); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	clearSession(w)
	respond(w, 204, nil)
}

func (p *Panel) revokeSessions(w http.ResponseWriter, r *http.Request) {
	if _, err := p.db.ExecContext(r.Context(), "DELETE FROM sessions"); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	clearSession(w)
	respond(w, 204, nil)
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}
