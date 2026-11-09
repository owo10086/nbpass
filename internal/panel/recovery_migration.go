package panel

const recoveryMigration = `CREATE TABLE recovery_state (
 id INTEGER PRIMARY KEY CHECK(id=1), restored_at INTEGER NOT NULL
);
CREATE TABLE recovery_nodes (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 high_water INTEGER, history_since INTEGER NOT NULL DEFAULT 0, history_until INTEGER NOT NULL DEFAULT 0,
 verified INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE recovery_groups (
 group_id TEXT PRIMARY KEY REFERENCES entry_groups(id) ON DELETE CASCADE,
 verified INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE recovery_rotation_attempts (
 action_id TEXT PRIMARY KEY, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 attempted_at INTEGER NOT NULL, state TEXT NOT NULL
);
CREATE INDEX recovery_rotation_node_time ON recovery_rotation_attempts(node_id,attempted_at);`
