package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"
)

const telegramMigration = `
CREATE TABLE telegram_status (
	id INTEGER PRIMARY KEY CHECK(id=1),
	config_id TEXT NOT NULL,
	reachable INTEGER NOT NULL,
	attempted_at INTEGER NOT NULL,
	succeeded_at INTEGER
);
CREATE TABLE telegram_deliveries (
	incident_id TEXT PRIMARY KEY,
	node_id TEXT NOT NULL,
	message TEXT NOT NULL,
	parts TEXT NOT NULL,
	version INTEGER NOT NULL DEFAULT 1,
	sent_version INTEGER NOT NULL DEFAULT 0,
	state TEXT NOT NULL DEFAULT 'pending',
	attempts INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	next_attempt_at INTEGER NOT NULL
);
CREATE INDEX telegram_deliveries_due ON telegram_deliveries(state,next_attempt_at);
CREATE TABLE telegram_failures (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	incident_id TEXT NOT NULL,
	attempted_at INTEGER NOT NULL,
	attempt INTEGER NOT NULL,
	message TEXT NOT NULL
);`

type telegramConfig struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Token   string `json:"bot_token"`
	ChatID  string `json:"chat_id"`
}

func (p *Panel) telegramConfig(ctx context.Context) (telegramConfig, error) {
	var config telegramConfig
	data, err := p.secrets.Get(ctx, "telegram.settings")
	if errors.Is(err, sql.ErrNoRows) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	err = json.Unmarshal(data, &config)
	return config, err
}

func (p *Panel) telegramRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/telegram", p.getTelegram)
	mux.HandleFunc("PUT /api/settings/telegram", p.putTelegram)
	mux.HandleFunc("POST /api/settings/telegram/test", p.testTelegram)
}

func (p *Panel) getTelegram(w http.ResponseWriter, r *http.Request) {
	p.telegramMu.Lock()
	defer p.telegramMu.Unlock()
	config, err := p.telegramConfig(r.Context())
	if err != nil {
		problem(w, 503, "Telegram 设置无法读取")
		return
	}
	result, err := p.telegramView(r.Context(), config)
	if err != nil {
		problem(w, 503, "Telegram 状态无法读取")
		return
	}
	respond(w, 200, result)
}

func (p *Panel) telegramView(ctx context.Context, config telegramConfig) (map[string]any, error) {
	result := map[string]any{"enabled": config.Enabled, "configured": config.Token != "" && config.ChatID != "", "reachability": "unknown", "last_attempt_at": nil, "last_success_at": nil}
	var reachable bool
	var attempted int64
	var succeeded sql.NullInt64
	err := p.db.QueryRowContext(ctx, "SELECT reachable,attempted_at,succeeded_at FROM telegram_status WHERE id=1 AND config_id=?", config.ID).Scan(&reachable, &attempted, &succeeded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		result["reachability"] = "unreachable"
		if reachable {
			result["reachability"] = "reachable"
		}
		result["last_attempt_at"], result["last_success_at"] = time.Unix(0, attempted).UTC(), nullableTime(succeeded)
	}
	var pending, expired int
	if err := p.db.QueryRowContext(ctx, "SELECT count(*) FROM telegram_deliveries WHERE state='pending'").Scan(&pending); err != nil {
		return nil, err
	}
	if err := p.db.QueryRowContext(ctx, "SELECT count(*) FROM telegram_deliveries WHERE state='expired'").Scan(&expired); err != nil {
		return nil, err
	}
	result["pending"], result["expired"] = pending, expired
	rows, err := p.db.QueryContext(ctx, "SELECT incident_id,attempted_at,attempt,message FROM telegram_failures ORDER BY id DESC LIMIT 20")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	failures := []map[string]any{}
	for rows.Next() {
		var incident, message string
		var at int64
		var attempt int
		if err := rows.Scan(&incident, &at, &attempt, &message); err != nil {
			return nil, err
		}
		failures = append(failures, map[string]any{"incident_id": incident, "attempted_at": time.Unix(0, at).UTC(), "attempt": attempt, "message": message})
	}
	result["failures"] = failures
	return result, rows.Err()
}

var telegramTokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,128}$`)
var telegramChatPattern = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

func (p *Panel) putTelegram(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool   `json:"enabled"`
		Token   *string `json:"bot_token"`
		ChatID  *string `json:"chat_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil || body.Token != nil && !telegramTokenPattern.MatchString(*body.Token) || body.ChatID != nil && !telegramChatPattern.MatchString(*body.ChatID) {
		problem(w, 400, "请指定开关，并填写有效的 Bot token 和聊天目标")
		return
	}
	p.telegramMu.Lock()
	defer p.telegramMu.Unlock()
	config, err := p.telegramConfig(r.Context())
	if err != nil {
		problem(w, 503, "Telegram 设置无法读取")
		return
	}
	config.Enabled = *body.Enabled
	if body.Token != nil {
		config.Token = *body.Token
	}
	if body.ChatID != nil {
		config.ChatID = *body.ChatID
	}
	if config.Enabled && (config.Token == "" || config.ChatID == "") {
		problem(w, 400, "启用前请配置 Bot token 和聊天目标")
		return
	}
	config.ID, _, err = p.randomCredential()
	if err != nil {
		problem(w, 503, "Telegram 设置无法保存")
		return
	}
	data, err := json.Marshal(config)
	if err == nil {
		err = p.secrets.Put(r.Context(), "telegram.settings", data)
	}
	if err != nil {
		problem(w, 503, "Telegram 设置无法保存")
		return
	}
	p.wakeTelegram()
	result, err := p.telegramView(r.Context(), config)
	if err != nil {
		problem(w, 503, "Telegram 状态无法读取")
		return
	}
	respond(w, 200, result)
}

func (p *Panel) testTelegram(w http.ResponseWriter, r *http.Request) {
	p.telegramMu.Lock()
	defer p.telegramMu.Unlock()
	config, err := p.telegramConfig(r.Context())
	if err != nil {
		problem(w, 503, "Telegram 设置无法读取")
		return
	}
	if !config.Enabled {
		problem(w, 409, "请先启用 Telegram 通知")
		return
	}
	id, _, err := p.randomCredential()
	if err == nil {
		err = p.QueueTelegram(r.Context(), "test:"+id, "", "nbpass Telegram 测试通知")
	}
	if err != nil {
		problem(w, 503, "测试通知无法排队")
		return
	}
	respond(w, 202, map[string]bool{"queued": true})
}
