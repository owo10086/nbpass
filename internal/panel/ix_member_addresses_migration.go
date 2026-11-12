package panel

// 48: keep server uniqueness and primary/backup positions; addresses never become node identities.
const ixMemberAddressesMigration = `ALTER TABLE exit_group_members ADD COLUMN address_id TEXT NOT NULL DEFAULT 'public' CHECK(address_id != '');
CREATE TRIGGER exit_member_address_insert BEFORE INSERT ON exit_group_members
WHEN NEW.address_id != 'public' AND NOT EXISTS(SELECT 1 FROM tunnel_addresses WHERE node_id=NEW.node_id AND id=NEW.address_id)
BEGIN SELECT RAISE(ABORT,'member address does not belong to server'); END;
CREATE TRIGGER exit_member_address_update BEFORE UPDATE OF node_id,address_id ON exit_group_members
WHEN NEW.address_id != 'public' AND NOT EXISTS(SELECT 1 FROM tunnel_addresses WHERE node_id=NEW.node_id AND id=NEW.address_id)
BEGIN SELECT RAISE(ABORT,'member address does not belong to server'); END;
CREATE TRIGGER tunnel_address_referenced_delete BEFORE DELETE ON tunnel_addresses
WHEN EXISTS(SELECT 1 FROM exit_group_members WHERE node_id=OLD.node_id AND address_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'address referenced by endpoint group'); END;
CREATE TRIGGER tunnel_address_referenced_identity BEFORE UPDATE OF node_id,id ON tunnel_addresses
WHEN (NEW.node_id != OLD.node_id OR NEW.id != OLD.id) AND EXISTS(SELECT 1 FROM exit_group_members WHERE node_id=OLD.node_id AND address_id=OLD.id)
BEGIN SELECT RAISE(ABORT,'address referenced by endpoint group'); END;`
