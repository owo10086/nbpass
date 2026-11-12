package panel

const forwardChainsMigration = `CREATE TABLE forward_chains (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL
);
CREATE TABLE forward_chain_hops (
 chain_id TEXT NOT NULL REFERENCES forward_chains(id) ON DELETE CASCADE,
 position INTEGER NOT NULL CHECK(position>=1),
 exit_group_id TEXT NOT NULL REFERENCES exit_groups(id) ON DELETE RESTRICT,
 PRIMARY KEY(chain_id,position)
);
CREATE INDEX forward_chain_hops_exit_group ON forward_chain_hops(exit_group_id);`
