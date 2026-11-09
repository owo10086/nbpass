package panel

// 迁移 23：保留期独立于业务状态；时间索引使每批只扫描待删除的历史。
const retentionMigration = `CREATE TABLE retention_settings (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 event_days INTEGER NOT NULL DEFAULT 90 CHECK (event_days BETWEEN 1 AND 3650),
 probe_hours INTEGER NOT NULL DEFAULT 24 CHECK (probe_hours BETWEEN 1 AND 8760)
);
INSERT INTO retention_settings(id) VALUES(1);
CREATE INDEX probe_minutes_time ON probe_minutes(minute);`
