package panel

// SQL 不依赖迁移编号，便于与其他票的迁移统一排序。
const allPublishMigration = `ALTER TABLE entry_groups ADD COLUMN publication_mode TEXT NOT NULL DEFAULT 'primary_backup' CHECK(publication_mode IN ('primary_backup','all'));
ALTER TABLE entry_groups ADD COLUMN minimum_kept INTEGER NOT NULL DEFAULT 1 CHECK(minimum_kept>=1);
ALTER TABLE entry_groups ADD COLUMN first_published_at INTEGER;
ALTER TABLE entry_action_records ADD COLUMN propagation_status TEXT NOT NULL DEFAULT '';
ALTER TABLE entry_action_records ADD COLUMN propagation_note TEXT NOT NULL DEFAULT '';
CREATE TABLE entry_member_publications (
 group_id TEXT NOT NULL REFERENCES entry_groups(id) ON DELETE CASCADE,
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 expected_value TEXT NOT NULL DEFAULT '', expected_enabled INTEGER NOT NULL DEFAULT 0 CHECK(expected_enabled IN (0,1)),
 address_pending INTEGER NOT NULL DEFAULT 1 CHECK(address_pending IN (0,1)),
 last_action_error TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '等待首次发布', manual_locked INTEGER NOT NULL DEFAULT 0 CHECK(manual_locked IN (0,1)),
 removal_action_id TEXT NOT NULL DEFAULT '', propagation_status TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(group_id,node_id)
);
CREATE TRIGGER entry_publication_mode_immutable BEFORE UPDATE OF publication_mode ON entry_groups WHEN OLD.publication_mode<>NEW.publication_mode BEGIN SELECT RAISE(ABORT,'发布模式创建后不能更改'); END;`
