package panel

// 不依赖迁移编号；节点监控可与主备组或其他节点使用相同域名。
const entryNodeRecordMigration = `ALTER TABLE node_publications ADD COLUMN published INTEGER NOT NULL DEFAULT 0 CHECK(published IN (0,1));
ALTER TABLE entry_action_records ADD COLUMN preserve_enabled INTEGER NOT NULL DEFAULT 0 CHECK(preserve_enabled IN (0,1));
ALTER TABLE dns_watches ADD COLUMN owner_node_id TEXT REFERENCES nodes(id) ON DELETE CASCADE;
CREATE UNIQUE INDEX dns_watch_owner_node ON dns_watches(owner_node_id);
DROP INDEX dns_watch_manual_domain;
CREATE UNIQUE INDEX dns_watch_manual_domain ON dns_watches(domain) WHERE entry_group_id IS NULL AND owner_node_id IS NULL;
CREATE TABLE node_address_sync_jobs (
 managed_record_id TEXT PRIMARY KEY REFERENCES managed_records(id) ON DELETE CASCADE,
 binding_generation INTEGER NOT NULL, node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 address TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind='address_update')
);`
