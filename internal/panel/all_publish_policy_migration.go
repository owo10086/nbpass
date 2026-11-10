package panel

// 迁移 40：摘除时间与可重入通知；候选样本重启后重新累计。
const allPublishPolicyMigration = `ALTER TABLE entry_member_publications ADD COLUMN withdrawn_at INTEGER;
ALTER TABLE entry_groups ADD COLUMN minimum_kept_generation INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER entry_minimum_kept_generation AFTER UPDATE OF minimum_kept ON entry_groups
WHEN OLD.minimum_kept<>NEW.minimum_kept BEGIN
 UPDATE entry_groups SET minimum_kept_generation=minimum_kept_generation+1 WHERE id=NEW.id;
END;
ALTER TABLE entry_member_publications ADD COLUMN restore_generation INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER entry_member_restore_maintenance AFTER UPDATE OF maintenance_until ON nodes
WHEN OLD.maintenance_until IS NOT NEW.maintenance_until BEGIN
 UPDATE entry_member_publications SET restore_generation=restore_generation+1 WHERE node_id=NEW.id;
END;
CREATE TRIGGER entry_member_restore_address AFTER UPDATE OF public_ipv4 ON node_regions
WHEN OLD.public_ipv4<>NEW.public_ipv4 BEGIN
 UPDATE entry_member_publications SET restore_generation=restore_generation+1 WHERE node_id=NEW.node_id;
END;
CREATE TABLE entry_member_decisions (
 id TEXT PRIMARY KEY,
 group_id TEXT NOT NULL REFERENCES entry_groups(id) ON DELETE CASCADE,
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 managed_record_id TEXT NOT NULL, binding_generation INTEGER NOT NULL,
 kind TEXT NOT NULL, message TEXT NOT NULL, action_id TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL, notified INTEGER NOT NULL DEFAULT 0
);`
