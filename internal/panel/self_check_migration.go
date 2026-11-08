package panel

// 迁移 13：只在 rotationMigration 之后追加；并行票合并时统一重排。
const selfCheckMigration = `CREATE TABLE self_check_settings (id INTEGER PRIMARY KEY CHECK(id=1), settings BLOB NOT NULL);
CREATE TABLE node_self_check (node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE, enabled INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'unknown', consecutive INTEGER NOT NULL DEFAULT 0, last_sequence INTEGER NOT NULL DEFAULT 0, last_config_key TEXT NOT NULL DEFAULT '');
CREATE TABLE self_check_rounds (node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, round_id TEXT NOT NULL, sequence INTEGER NOT NULL, config_revision INTEGER NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL, received_at INTEGER NOT NULL, result TEXT NOT NULL, final_result TEXT NOT NULL, evidence_source TEXT NOT NULL, raw BLOB NOT NULL, PRIMARY KEY(node_id,round_id), UNIQUE(node_id,sequence));
CREATE INDEX self_check_rounds_time ON self_check_rounds(node_id,sequence);
CREATE TABLE self_check_notifications (id TEXT PRIMARY KEY, node_id TEXT NOT NULL, kind TEXT NOT NULL, occurred_at INTEGER NOT NULL, config_revision INTEGER NOT NULL, message TEXT NOT NULL, event_done INTEGER NOT NULL DEFAULT 0, telegram_done INTEGER NOT NULL DEFAULT 0);`
