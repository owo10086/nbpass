package panel

const dnsWatchMigration = `CREATE TABLE dns_watch_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), resolvers TEXT NOT NULL
);
INSERT INTO dns_watch_settings VALUES(1,'[{"kind":"dns","address":"223.5.5.5:53"},{"kind":"dns","address":"119.29.29.29:53"},{"kind":"doh","address":"https://cloudflare-dns.com/dns-query"}]');
CREATE TABLE dns_watches (
 id TEXT PRIMARY KEY, domain TEXT NOT NULL UNIQUE, expected_ips TEXT NOT NULL,
 expected_version INTEGER NOT NULL DEFAULT 1, round INTEGER NOT NULL DEFAULT 0,
 next_sample_at INTEGER NOT NULL, created_at INTEGER NOT NULL, last_status TEXT NOT NULL DEFAULT ''
);
CREATE TABLE dns_watch_paths (
 watch_id TEXT NOT NULL REFERENCES dns_watches(id) ON DELETE CASCADE,
 path_id TEXT NOT NULL, node_id TEXT NOT NULL, kind TEXT NOT NULL, resolver TEXT NOT NULL,
 expected_version INTEGER NOT NULL, round INTEGER NOT NULL, ips TEXT NOT NULL DEFAULT '[]',
 last_success TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT '等待采样',
 consecutive_mismatches INTEGER NOT NULL DEFAULT 0, consecutive_matches INTEGER NOT NULL DEFAULT 0,
 sampled_at INTEGER, active INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(watch_id,path_id)
);
CREATE TABLE dns_watch_history (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT, watch_id TEXT NOT NULL REFERENCES dns_watches(id) ON DELETE CASCADE,
 path_id TEXT NOT NULL, sampled_at INTEGER NOT NULL, old_ips TEXT NOT NULL, ips TEXT NOT NULL,
 status TEXT NOT NULL, changed INTEGER NOT NULL
);
CREATE INDEX dns_watch_history_watch ON dns_watch_history(watch_id,sequence);
CREATE TABLE dns_watch_ips (
 watch_id TEXT NOT NULL REFERENCES dns_watches(id) ON DELETE CASCADE, ip TEXT NOT NULL,
 PRIMARY KEY(watch_id,ip)
);
CREATE TABLE dns_watch_tasks (
 id TEXT PRIMARY KEY, watch_id TEXT NOT NULL REFERENCES dns_watches(id) ON DELETE CASCADE,
 path_id TEXT NOT NULL, node_id TEXT NOT NULL, process_instance TEXT NOT NULL,
 domain TEXT NOT NULL, resolver TEXT NOT NULL, kind TEXT NOT NULL,
 expected_version INTEGER NOT NULL, round INTEGER NOT NULL, expires_at INTEGER NOT NULL
);
CREATE INDEX dns_watch_tasks_node ON dns_watch_tasks(node_id,expires_at);
CREATE TABLE dns_watch_notices (
 id TEXT PRIMARY KEY, watch_id TEXT NOT NULL REFERENCES dns_watches(id) ON DELETE CASCADE,
 type TEXT NOT NULL, summary TEXT NOT NULL, event_recorded INTEGER NOT NULL DEFAULT 0,
 notification_queued INTEGER NOT NULL DEFAULT 0
);`
