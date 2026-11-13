package panel

// 52: retain the single configured endpoint and member order. Address identity
// stays "public" on the wire for compatibility with deployed nodes.
const singleEndpointAddressMigration = `ALTER TABLE node_snapshots ADD COLUMN address_upgrade_pending INTEGER NOT NULL DEFAULT 0 CHECK(address_upgrade_pending IN (0,1));
UPDATE node_snapshots SET address_upgrade_pending=1
WHERE EXISTS(SELECT 1 FROM exit_group_members WHERE address_id!='public');
DROP TRIGGER exit_member_address_insert;
DROP TRIGGER exit_member_address_update;
DROP TRIGGER tunnel_address_referenced_delete;
DROP TRIGGER tunnel_address_referenced_identity;
UPDATE exit_group_members SET address_id='public' WHERE address_id!='public';
DROP TABLE tunnel_addresses;
CREATE TRIGGER exit_member_address_insert BEFORE INSERT ON exit_group_members
WHEN NEW.address_id != 'public'
BEGIN SELECT RAISE(ABORT,'endpoint has a single address'); END;
CREATE TRIGGER exit_member_address_update BEFORE UPDATE OF address_id ON exit_group_members
WHEN NEW.address_id != 'public'
BEGIN SELECT RAISE(ABORT,'endpoint has a single address'); END;`
