package panel

// 票 14 使用迁移 3；聚合直接入库，不保存原始 5 秒样本。
const probeMigration = `CREATE TABLE probe_realtime (
	node_id TEXT PRIMARY KEY REFERENCES nodes(id),
	process_instance TEXT NOT NULL,
	sequence INTEGER NOT NULL,
	received_at INTEGER NOT NULL,
	values_json BLOB NOT NULL
);
CREATE TABLE probe_minutes (
	node_id TEXT NOT NULL REFERENCES nodes(id),
	minute INTEGER NOT NULL,
	samples INTEGER NOT NULL,
	aggregates_json BLOB NOT NULL,
	PRIMARY KEY(node_id, minute)
);`
