package panel

// 迁移 28：离线确认独立于自测轮次，只复用检测任务的传输和节点执行器。
const entryOfflineMigration = `CREATE TABLE entry_offline_checks (
 id TEXT PRIMARY KEY,
 group_id TEXT NOT NULL REFERENCES entry_groups(id) ON DELETE CASCADE,
 node_id TEXT NOT NULL REFERENCES nodes(id),
 incident_id INTEGER NOT NULL REFERENCES offline_incidents(id) ON DELETE CASCADE,
 desired_version INTEGER NOT NULL,
 target_ip TEXT NOT NULL, target_port INTEGER NOT NULL, config_revision INTEGER NOT NULL,
 issued_at INTEGER NOT NULL, deadline INTEGER NOT NULL,
 outcome TEXT NOT NULL DEFAULT 'pending',
 action_id TEXT NOT NULL DEFAULT '', notified INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX entry_offline_group ON entry_offline_checks(group_id,issued_at);
CREATE TABLE entry_offline_tasks (
 id TEXT PRIMARY KEY,
 check_id TEXT NOT NULL REFERENCES entry_offline_checks(id) ON DELETE CASCADE,
 checker_id TEXT NOT NULL, task BLOB NOT NULL, result BLOB,
 UNIQUE(check_id,checker_id)
);
CREATE INDEX entry_offline_assignee ON entry_offline_tasks(checker_id);
CREATE TRIGGER entry_offline_invalidate_ip AFTER UPDATE OF public_ipv4 ON node_regions
WHEN OLD.public_ipv4<>NEW.public_ipv4 BEGIN
 UPDATE entry_offline_checks SET outcome='invalidated' WHERE node_id=NEW.node_id AND outcome IN ('pending','unreachable');
END;
CREATE TRIGGER entry_offline_invalidate_config AFTER UPDATE OF desired_revision,check_port,maintenance_until ON nodes
WHEN OLD.desired_revision<>NEW.desired_revision OR OLD.check_port<>NEW.check_port OR OLD.maintenance_until IS NOT NEW.maintenance_until BEGIN
 UPDATE entry_offline_checks SET outcome='invalidated' WHERE node_id=NEW.id AND outcome IN ('pending','unreachable');
END;
CREATE TRIGGER entry_offline_invalidate_recovery AFTER UPDATE OF recovered_at ON offline_incidents
WHEN NEW.recovered_at IS NOT NULL BEGIN
 UPDATE entry_offline_checks SET outcome='invalidated' WHERE incident_id=NEW.id AND outcome IN ('pending','unreachable');
END;
CREATE TRIGGER entry_offline_invalidate_publication AFTER UPDATE OF desired_version ON entry_publications
WHEN OLD.desired_version<>NEW.desired_version BEGIN
 UPDATE entry_offline_checks SET outcome='invalidated' WHERE group_id=NEW.group_id AND outcome IN ('pending','unreachable');
END;`
