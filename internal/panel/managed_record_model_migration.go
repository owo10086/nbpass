package panel

// 不依赖迁移序号；旧动作的身份快照随绑定一起迁移，未确认动作不能丢失。
const managedRecordModelMigration = `CREATE TABLE managed_records_next (
 id TEXT PRIMARY KEY NOT NULL DEFAULT (lower(hex(randomblob(16)))),
 owner_kind TEXT NOT NULL DEFAULT 'group' CHECK(owner_kind IN ('group','member','node')),
 group_id TEXT REFERENCES entry_groups(id) ON DELETE CASCADE,
 node_id TEXT REFERENCES nodes(id) ON DELETE CASCADE,
 binding_generation INTEGER NOT NULL DEFAULT 1 CHECK(binding_generation>0),
 account_id TEXT NOT NULL REFERENCES dns_accounts(id),
 provider TEXT NOT NULL CHECK(provider='dnspod'), domain TEXT NOT NULL, record_id TEXT NOT NULL,
 name TEXT NOT NULL, line TEXT NOT NULL, line_id TEXT NOT NULL,
 current_value TEXT NOT NULL, current_ttl INTEGER NOT NULL, warnings TEXT NOT NULL,
 current_enabled INTEGER NOT NULL DEFAULT 1 CHECK(current_enabled IN (0,1)),
 CHECK((owner_kind='group' AND group_id IS NOT NULL AND node_id IS NULL) OR
       (owner_kind='member' AND group_id IS NOT NULL AND node_id IS NOT NULL) OR
       (owner_kind='node' AND group_id IS NULL AND node_id IS NOT NULL)),
 UNIQUE(provider,domain,record_id)
);
INSERT INTO managed_records_next(group_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings)
 SELECT group_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings FROM managed_records;
DROP TABLE managed_records;
ALTER TABLE managed_records_next RENAME TO managed_records;
CREATE UNIQUE INDEX managed_group_owner ON managed_records(group_id) WHERE owner_kind='group';
CREATE UNIQUE INDEX managed_member_owner ON managed_records(group_id,node_id) WHERE owner_kind='member';
CREATE UNIQUE INDEX managed_node_owner ON managed_records(node_id) WHERE owner_kind='node';

CREATE TABLE entry_actions_next (
 id TEXT PRIMARY KEY, group_id TEXT REFERENCES entry_groups(id) ON DELETE CASCADE,
 owner_node_id TEXT REFERENCES nodes(id) ON DELETE CASCADE,
 version INTEGER NOT NULL, kind TEXT NOT NULL, node_id TEXT NOT NULL REFERENCES nodes(id),
 old_value TEXT NOT NULL, new_value TEXT NOT NULL, old_ttl INTEGER NOT NULL,
 stage TEXT NOT NULL, last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, written_at INTEGER,
 minimum_enabled INTEGER NOT NULL DEFAULT 0 CHECK(minimum_enabled>=0),
 write_started INTEGER NOT NULL DEFAULT 0, failback_notified INTEGER NOT NULL DEFAULT 0 CHECK(failback_notified IN (0,1)),
 CHECK((group_id IS NULL)<>(owner_node_id IS NULL)), UNIQUE(group_id,version), UNIQUE(owner_node_id,version)
);
INSERT INTO entry_actions_next(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,last_error,created_at,written_at,write_started,failback_notified)
 SELECT id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,last_error,created_at,written_at,write_started,failback_notified FROM entry_actions;
DROP TABLE entry_actions;
ALTER TABLE entry_actions_next RENAME TO entry_actions;
CREATE TABLE entry_action_records (
 action_id TEXT NOT NULL REFERENCES entry_actions(id) ON DELETE CASCADE,
 managed_record_id TEXT NOT NULL, binding_generation INTEGER NOT NULL,
 provider TEXT NOT NULL, domain TEXT NOT NULL, record_id TEXT NOT NULL,
 old_value TEXT NOT NULL, new_value TEXT NOT NULL, old_ttl INTEGER NOT NULL, new_ttl INTEGER NOT NULL DEFAULT 60,
 old_enabled INTEGER NOT NULL CHECK(old_enabled IN (0,1)), new_enabled INTEGER NOT NULL CHECK(new_enabled IN (0,1)),
 stage TEXT NOT NULL, write_started INTEGER NOT NULL DEFAULT 0,
 value_confirmed INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', written_at INTEGER,
 rejection_notified INTEGER NOT NULL DEFAULT 0,
 explicit_enable INTEGER NOT NULL DEFAULT 0, status_started INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(action_id,managed_record_id)
);
INSERT INTO entry_action_records(action_id,managed_record_id,binding_generation,provider,domain,record_id,old_value,new_value,old_ttl,old_enabled,new_enabled,stage,write_started,last_error,written_at,value_confirmed)
 SELECT a.id,m.id,m.binding_generation,m.provider,m.domain,m.record_id,a.old_value,a.new_value,a.old_ttl,1,1,a.stage,a.write_started,a.last_error,a.written_at,a.stage='服务商已写入'
 FROM entry_actions a JOIN managed_records m ON m.group_id=a.group_id;
CREATE INDEX entry_record_pending ON entry_action_records(managed_record_id,binding_generation,stage);
CREATE TABLE node_publications (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
 desired_version INTEGER NOT NULL DEFAULT 0, current_action_id TEXT NOT NULL DEFAULT '', written_at INTEGER
);
ALTER TABLE node_regions ADD COLUMN confirmed_ipv4 TEXT NOT NULL DEFAULT '';
ALTER TABLE node_regions ADD COLUMN confirmed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_regions ADD COLUMN address_notice_ipv4 TEXT NOT NULL DEFAULT '';
UPDATE node_regions SET confirmed_ipv4=public_ipv4,confirmed_at=domestic_since;
CREATE TABLE recovery_records (
 managed_record_id TEXT PRIMARY KEY REFERENCES managed_records(id) ON DELETE CASCADE,
 binding_generation INTEGER NOT NULL, verified INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT ''
);
INSERT INTO recovery_records(managed_record_id,binding_generation)
 SELECT id,binding_generation FROM managed_records WHERE EXISTS(SELECT 1 FROM recovery_state);
CREATE TRIGGER managed_record_pending_delete BEFORE DELETE ON managed_records
 WHEN EXISTS(SELECT 1 FROM entry_action_records r WHERE r.managed_record_id=OLD.id AND r.binding_generation=OLD.binding_generation
 AND r.stage IN ('期望已变更','结果未确认')) BEGIN SELECT RAISE(ABORT,'受管记录有未完成动作'); END;
CREATE TRIGGER managed_record_pending_rebind BEFORE UPDATE OF owner_kind,group_id,node_id,provider,domain,record_id,account_id,binding_generation ON managed_records
 WHEN EXISTS(SELECT 1 FROM entry_action_records r WHERE r.managed_record_id=OLD.id AND r.binding_generation=OLD.binding_generation
 AND r.stage IN ('期望已变更','结果未确认')) BEGIN SELECT RAISE(ABORT,'受管记录有未完成动作'); END;
CREATE TRIGGER managed_group_pending_delete BEFORE DELETE ON entry_groups
 WHEN EXISTS(SELECT 1 FROM managed_records m JOIN entry_action_records r ON r.managed_record_id=m.id AND r.binding_generation=m.binding_generation
 WHERE m.group_id=OLD.id AND r.stage IN ('期望已变更','结果未确认')) BEGIN SELECT RAISE(ABORT,'受管记录有未完成动作'); END;
CREATE TRIGGER managed_node_pending_delete BEFORE DELETE ON nodes
 WHEN EXISTS(SELECT 1 FROM managed_records m JOIN entry_action_records r ON r.managed_record_id=m.id AND r.binding_generation=m.binding_generation
 WHERE m.node_id=OLD.id AND r.stage IN ('期望已变更','结果未确认')) BEGIN SELECT RAISE(ABORT,'受管记录有未完成动作'); END;`
