package panel

// 单节点入口迁移不依赖版本编号，可随并行票重排。
// SQLite 无法直接放宽 NOT NULL；先保留级联子表数据，再重建规则表并回填。
// 子表本身不重建，外键与索引保持原样；全部步骤由迁移事务原子提交。
const singleEntryRuleMigration = `CREATE TEMP TABLE sg02_rule_traffic_totals AS SELECT * FROM rule_traffic_totals;
CREATE TEMP TABLE sg02_rule_traffic_cursors AS SELECT * FROM rule_traffic_cursors;
CREATE TEMP TABLE sg02_rule_traffic_realtime AS SELECT * FROM rule_traffic_realtime;
CREATE TEMP TABLE sg02_rule_traffic_sources AS SELECT * FROM rule_traffic_sources;
CREATE TEMP TABLE sg02_rule_authorizations AS SELECT * FROM rule_authorizations;
CREATE TEMP TABLE sg02_tunnel_authorization_confirmations AS SELECT * FROM tunnel_authorization_confirmations;
CREATE TABLE forward_rules_single_entry (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 entry_group_id TEXT REFERENCES entry_groups(id),
 entry_node_id TEXT REFERENCES nodes(id),
 kind TEXT NOT NULL,
 listen_port INTEGER NOT NULL,
 target_address TEXT NOT NULL,
 target_family TEXT NOT NULL,
 target_port INTEGER NOT NULL,
 allow_private INTEGER NOT NULL,
 enabled INTEGER NOT NULL,
 exit_group_id TEXT NOT NULL DEFAULT '',
 CHECK ((entry_group_id IS NOT NULL AND entry_group_id<>'' AND entry_node_id IS NULL)
     OR (entry_node_id IS NOT NULL AND entry_node_id<>'' AND entry_group_id IS NULL))
);
INSERT INTO forward_rules_single_entry(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id)
 SELECT id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id FROM forward_rules;
DROP TABLE forward_rules;
ALTER TABLE forward_rules_single_entry RENAME TO forward_rules;
CREATE INDEX forward_rules_entry_node ON forward_rules(entry_node_id);
CREATE INDEX forward_rules_entry_group ON forward_rules(entry_group_id);
INSERT INTO rule_traffic_totals SELECT * FROM sg02_rule_traffic_totals;
DROP TABLE sg02_rule_traffic_totals;
INSERT INTO rule_traffic_cursors SELECT * FROM sg02_rule_traffic_cursors;
DROP TABLE sg02_rule_traffic_cursors;
INSERT INTO rule_traffic_realtime SELECT * FROM sg02_rule_traffic_realtime;
DROP TABLE sg02_rule_traffic_realtime;
INSERT INTO rule_traffic_sources SELECT * FROM sg02_rule_traffic_sources;
DROP TABLE sg02_rule_traffic_sources;
INSERT INTO rule_authorizations SELECT * FROM sg02_rule_authorizations;
DROP TABLE sg02_rule_authorizations;
INSERT INTO tunnel_authorization_confirmations SELECT * FROM sg02_tunnel_authorization_confirmations;
DROP TABLE sg02_tunnel_authorization_confirmations;
`
