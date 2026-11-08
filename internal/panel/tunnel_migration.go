package panel

const tunnelMigration = `
CREATE TABLE tunnel_exits (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id),
 address TEXT NOT NULL, listen_port INTEGER NOT NULL, transport TEXT NOT NULL,
 server_name TEXT NOT NULL, certificate_pem BLOB NOT NULL, private_key_ciphertext BLOB NOT NULL
);
CREATE TABLE exit_groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, strategy TEXT NOT NULL);
CREATE TABLE exit_group_members (
 group_id TEXT NOT NULL REFERENCES exit_groups(id) ON DELETE CASCADE,
 node_id TEXT NOT NULL REFERENCES tunnel_exits(node_id), position INTEGER NOT NULL,
 PRIMARY KEY(group_id,node_id), UNIQUE(group_id,position)
);
CREATE TABLE tunnel_pairs (
 entry_id TEXT NOT NULL REFERENCES nodes(id), exit_id TEXT NOT NULL REFERENCES tunnel_exits(node_id),
 credential_ciphertext BLOB NOT NULL, PRIMARY KEY(entry_id,exit_id)
);
ALTER TABLE forward_rules ADD COLUMN exit_group_id TEXT NOT NULL DEFAULT '';
ALTER TABLE node_snapshots ADD COLUMN encrypted INTEGER NOT NULL DEFAULT 0;
`
