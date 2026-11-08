package panel

// 迁移 15：追加于第 14 版 selfCheckMigration 之后。
const checkerMigration = `ALTER TABLE self_check_rounds ADD COLUMN decision_eligible INTEGER NOT NULL DEFAULT 0;
CREATE TABLE checker_state_baselines (node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE, sequence INTEGER NOT NULL, state TEXT NOT NULL, consecutive INTEGER NOT NULL, config_key TEXT NOT NULL);
INSERT INTO checker_state_baselines(node_id,sequence,state,consecutive,config_key) SELECT node_id,last_sequence,state,consecutive,last_config_key FROM node_self_check;
CREATE TABLE checker_rounds (
 node_id TEXT NOT NULL, round_id TEXT NOT NULL, target_ip TEXT NOT NULL,
 target_port INTEGER NOT NULL, config_revision INTEGER NOT NULL,
 issued_at INTEGER NOT NULL, deadline INTEGER NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending',
 PRIMARY KEY(node_id,round_id),
 FOREIGN KEY(node_id,round_id) REFERENCES self_check_rounds(node_id,round_id) ON DELETE CASCADE
);
CREATE TABLE checker_tasks (
 id TEXT PRIMARY KEY, node_id TEXT NOT NULL, round_id TEXT NOT NULL,
 checker_id TEXT NOT NULL, task BLOB NOT NULL, result BLOB,
 FOREIGN KEY(node_id,round_id) REFERENCES checker_rounds(node_id,round_id) ON DELETE CASCADE,
 UNIQUE(node_id,round_id,checker_id)
);
CREATE INDEX checker_tasks_assignee ON checker_tasks(checker_id);
CREATE INDEX checker_rounds_deadline ON checker_rounds(status,deadline);
-- 地址变化与失效在同一事务提交，A→B→A不能复活旧任务；同样覆盖换IP进度的写入。
CREATE TRIGGER checker_invalidate_ip AFTER UPDATE OF public_ipv4 ON node_regions
WHEN OLD.public_ipv4<>NEW.public_ipv4 BEGIN
 UPDATE checker_rounds SET status='invalidated' WHERE node_id=NEW.node_id AND status='pending';
END;
CREATE TRIGGER checker_invalidate_config AFTER UPDATE OF desired_revision,check_port ON nodes
WHEN OLD.desired_revision<>NEW.desired_revision OR OLD.check_port<>NEW.check_port BEGIN
 UPDATE checker_rounds SET status='invalidated' WHERE node_id=NEW.id AND status='pending';
END;`
