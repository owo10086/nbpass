package panel

import (
	"context"
	"fmt"
)

var migrations = []string{
	`CREATE TABLE administrator (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		password_hash TEXT NOT NULL
	);
	CREATE TABLE sessions (
		token_hash BLOB PRIMARY KEY,
		created_at INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL
	);
	CREATE TABLE secrets (
		name TEXT PRIMARY KEY,
		ciphertext BLOB NOT NULL
	);`,
	`CREATE TABLE nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		token_hash BLOB UNIQUE,
		created_at INTEGER NOT NULL,
		last_seen_at INTEGER,
		disconnected_at INTEGER,
		maintenance_until INTEGER,
		desired_revision INTEGER NOT NULL DEFAULT 0,
		applied_revision INTEGER NOT NULL DEFAULT 0,
		process_instance TEXT NOT NULL DEFAULT '',
		software_version TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE installation_tickets (
		ticket_hash BLOB PRIMARY KEY,
		node_id TEXT NOT NULL UNIQUE REFERENCES nodes(id),
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE retired_control_instances (
		node_id TEXT NOT NULL REFERENCES nodes(id),
		process_instance TEXT NOT NULL,
		PRIMARY KEY(node_id, process_instance)
	);`,
	`CREATE TABLE auth_login_failures (
		source TEXT PRIMARY KEY,
		failures INTEGER NOT NULL,
		locked_until INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE auth_trusted_proxies (
		prefix TEXT PRIMARY KEY
	);`,
}

func (p *Panel) migrate(ctx context.Context) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("数据库版本 %d 高于当前程序支持的版本", version)
	}
	for version < len(migrations) {
		if _, err := tx.ExecContext(ctx, migrations[version]); err != nil {
			return fmt.Errorf("迁移 %d: %w", version+1, err)
		}
		version++
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
