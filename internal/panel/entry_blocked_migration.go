package panel

// 迁移32：被墙事故身份独立于可清理的事件/通知历史，决策与动作同事务保存。
const entryBlockedMigration = `CREATE TABLE entry_blocked_incidents (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 incident_id TEXT NOT NULL, target_ip TEXT NOT NULL, occurred_at INTEGER NOT NULL
);
INSERT INTO entry_blocked_incidents
 SELECT c.node_id,'existing-blocked:'||c.node_id||':'||c.last_sequence,r.public_ipv4,
 COALESCE((SELECT MAX(finished_at) FROM self_check_rounds WHERE node_id=c.node_id AND decision_eligible=1),0)
 FROM node_self_check c JOIN node_regions r ON r.node_id=c.node_id WHERE c.state='blocked';
CREATE TRIGGER entry_blocked_incident AFTER INSERT ON self_check_notifications
 WHEN NEW.kind='node_blocked' BEGIN
 INSERT INTO entry_blocked_incidents(node_id,incident_id,target_ip,occurred_at)
 VALUES(NEW.node_id,NEW.id,COALESCE((SELECT public_ipv4 FROM node_regions WHERE node_id=NEW.node_id),''),NEW.occurred_at)
 ON CONFLICT(node_id) DO UPDATE SET incident_id=excluded.incident_id,target_ip=excluded.target_ip,occurred_at=excluded.occurred_at;
END;
CREATE TABLE entry_blocked_decisions (
 id TEXT PRIMARY KEY, group_id TEXT NOT NULL REFERENCES entry_groups(id) ON DELETE CASCADE,
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE, incident_id TEXT NOT NULL, desired_version INTEGER NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('no_candidate','submitted')), action_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, waiting_notified INTEGER NOT NULL DEFAULT 0, switch_notified INTEGER NOT NULL DEFAULT 0
);`
