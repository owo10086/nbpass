package panel

// Append only: ALTER preserves all six traffic/authorization child tables.
const chainRulesMigration = `ALTER TABLE forward_rules ADD COLUMN chain_id TEXT NULL REFERENCES forward_chains(id);
CREATE INDEX forward_rules_chain ON forward_rules(chain_id);
CREATE TRIGGER forward_rules_path_insert BEFORE INSERT ON forward_rules
WHEN NOT ((NEW.kind='direct' AND NEW.exit_group_id='' AND NEW.chain_id IS NULL)
 OR (NEW.kind='tunnel' AND ((NEW.exit_group_id<>'' AND NEW.chain_id IS NULL)
 OR (NEW.exit_group_id='' AND NEW.chain_id IS NOT NULL AND NEW.chain_id<>''))))
BEGIN SELECT RAISE(ABORT,'规则出口组与转发链引用不合法'); END;
CREATE TRIGGER forward_rules_path_update BEFORE UPDATE ON forward_rules
WHEN NOT ((NEW.kind='direct' AND NEW.exit_group_id='' AND NEW.chain_id IS NULL)
 OR (NEW.kind='tunnel' AND ((NEW.exit_group_id<>'' AND NEW.chain_id IS NULL)
 OR (NEW.exit_group_id='' AND NEW.chain_id IS NOT NULL AND NEW.chain_id<>''))))
BEGIN SELECT RAISE(ABORT,'规则出口组与转发链引用不合法'); END;
CREATE TABLE node_tunnel_grant_results (
 node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 entry_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
 rule_id TEXT NOT NULL REFERENCES forward_rules(id) ON DELETE CASCADE,
 generation INTEGER NOT NULL CHECK(generation>0),
 applied_revision INTEGER NOT NULL,
 apply_error TEXT NOT NULL,
 PRIMARY KEY(node_id,entry_id,rule_id)
);`
