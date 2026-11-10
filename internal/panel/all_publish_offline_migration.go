package panel

// 迁移 41：全部发布的离线证据绑定成员记录，组版本仅保留为历史信息。
const allPublishOfflineMigration = `ALTER TABLE entry_offline_checks ADD COLUMN managed_record_id TEXT NOT NULL DEFAULT '';
ALTER TABLE entry_offline_checks ADD COLUMN binding_generation INTEGER NOT NULL DEFAULT 0;
CREATE INDEX entry_offline_member ON entry_offline_checks(group_id,node_id,incident_id,managed_record_id,binding_generation,issued_at);

-- 旧全部发布检测没有冻结记录身份，升级后重新检测，不能把旧证据套到当前绑定。
UPDATE entry_offline_checks SET outcome='invalidated'
 WHERE outcome IN ('pending','unreachable') AND group_id IN (SELECT id FROM entry_groups WHERE publication_mode='all');
DROP TRIGGER entry_offline_invalidate_publication;
CREATE TRIGGER entry_offline_invalidate_publication AFTER UPDATE OF desired_version ON entry_publications
WHEN OLD.desired_version<>NEW.desired_version BEGIN
 UPDATE entry_offline_checks SET outcome='invalidated' WHERE group_id=NEW.group_id AND outcome IN ('pending','unreachable')
 AND EXISTS(SELECT 1 FROM entry_groups WHERE id=NEW.group_id AND publication_mode='primary_backup');
END;
CREATE TRIGGER entry_offline_invalidate_binding AFTER UPDATE OF binding_generation ON managed_records
WHEN OLD.binding_generation<>NEW.binding_generation BEGIN
 UPDATE entry_offline_checks SET outcome='invalidated' WHERE managed_record_id=OLD.id AND outcome IN ('pending','unreachable');
END;
CREATE TRIGGER entry_offline_invalidate_unbinding AFTER DELETE ON managed_records BEGIN
 UPDATE entry_offline_checks SET outcome='invalidated' WHERE managed_record_id=OLD.id AND outcome IN ('pending','unreachable');
END;`
