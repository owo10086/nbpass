package panel

// 47: existing address remains the public slot, including administrator-entered LAN addresses.
const ixAddressesMigration = `ALTER TABLE tunnel_exits ADD COLUMN public_port INTEGER NULL CHECK(public_port BETWEEN 1 AND 65535);
CREATE TABLE tunnel_addresses (
 node_id TEXT NOT NULL REFERENCES tunnel_exits(node_id) ON DELETE CASCADE,
 id TEXT NOT NULL CHECK(id != '' AND id != 'public'),
 name TEXT NOT NULL,
 address TEXT NOT NULL,
 public_port INTEGER NULL CHECK(public_port BETWEEN 1 AND 65535),
 position INTEGER NOT NULL,
 PRIMARY KEY(node_id,id),
 UNIQUE(node_id,position)
);`
