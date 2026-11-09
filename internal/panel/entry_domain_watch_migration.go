package panel

import "strings"

// 入口组按组维护期望；不同受管记录可以使用同一域名。保留手工域名唯一性。
// 重建父表前备份并移除子表，避免 SQLite 的 DROP TABLE 级联清除历史。
var entryDomainWatchMigration = func() string {
	children := []string{"dns_watch_paths", "dns_watch_history", "dns_watch_ips", "dns_watch_tasks", "dns_watch_notices"}
	var migration strings.Builder
	for _, table := range children {
		migration.WriteString("CREATE TEMP TABLE saved_" + table + " AS SELECT * FROM " + table + ";DROP TABLE " + table + ";")
	}
	migration.WriteString(`CREATE TABLE dns_watches_v29 (
 id TEXT PRIMARY KEY, domain TEXT NOT NULL, expected_ips TEXT NOT NULL,
 expected_version INTEGER NOT NULL DEFAULT 1, round INTEGER NOT NULL DEFAULT 0,
 next_sample_at INTEGER NOT NULL, created_at INTEGER NOT NULL, last_status TEXT NOT NULL DEFAULT '',
 entry_group_id TEXT REFERENCES entry_groups(id) ON DELETE CASCADE,
 propagation_status TEXT NOT NULL DEFAULT '', observation_deadline INTEGER
);
INSERT INTO dns_watches_v29(id,domain,expected_ips,expected_version,round,next_sample_at,created_at,last_status)
 SELECT id,domain,expected_ips,expected_version,round,next_sample_at,created_at,last_status FROM dns_watches;
DROP TABLE dns_watches;
ALTER TABLE dns_watches_v29 RENAME TO dns_watches;
CREATE UNIQUE INDEX dns_watch_entry_group ON dns_watches(entry_group_id);
CREATE UNIQUE INDEX dns_watch_manual_domain ON dns_watches(domain) WHERE entry_group_id IS NULL;
CREATE INDEX dns_watch_domain ON dns_watches(domain);
`)
	// 复用不可变的旧迁移中子表定义，保留其索引与约束。
	migration.WriteString(dnsWatchMigration[strings.Index(dnsWatchMigration, "CREATE TABLE dns_watch_paths"):])
	for _, table := range children {
		migration.WriteString("INSERT INTO " + table + " SELECT * FROM saved_" + table + ";DROP TABLE saved_" + table + ";")
	}
	migration.WriteString(`CREATE TABLE entry_dns_previous (
 watch_id TEXT NOT NULL REFERENCES dns_watches(id) ON DELETE CASCADE,
 ip TEXT NOT NULL, expires_at INTEGER NOT NULL, PRIMARY KEY(watch_id,ip)
);
CREATE TABLE entry_dns_audits (
 group_id TEXT PRIMARY KEY REFERENCES entry_groups(id) ON DELETE CASCADE,
 next_check_at INTEGER NOT NULL, last_error TEXT NOT NULL DEFAULT ''
);
UPDATE dns_watches SET entry_group_id=(SELECT m.group_id FROM managed_records m
 WHERE CASE WHEN m.name='@' THEN m.domain ELSE lower(m.name||'.'||m.domain) END=dns_watches.domain ORDER BY m.group_id LIMIT 1);
INSERT INTO dns_watches(id,domain,expected_ips,expected_version,next_sample_at,created_at,entry_group_id)
 SELECT 'entry:'||m.group_id,CASE WHEN m.name='@' THEN m.domain ELSE lower(m.name||'.'||m.domain) END,'[]',0,0,0,m.group_id
 FROM managed_records m WHERE NOT EXISTS(SELECT 1 FROM dns_watches w WHERE w.entry_group_id=m.group_id);
UPDATE dns_watches SET expected_ips=coalesce((SELECT json_array(s.expected_value) FROM entry_publications s
 WHERE s.group_id=entry_group_id AND s.expected_value<>''),'[]'),
 expected_version=coalesce((SELECT s.desired_version FROM entry_publications s WHERE s.group_id=entry_group_id),0),
 next_sample_at=0,last_status='',
 propagation_status=coalesce((SELECT CASE WHEN s.written_at IS NULL THEN '期望已变更' ELSE '传播中' END FROM entry_publications s WHERE s.group_id=entry_group_id),''),
 observation_deadline=(SELECT s.written_at + min(3600,max(600,a.old_ttl*2))*1000000000
 FROM entry_publications s JOIN entry_actions a ON a.id=s.current_action_id WHERE s.group_id=entry_group_id)
 WHERE entry_group_id IS NOT NULL;
INSERT INTO entry_dns_previous(watch_id,ip,expires_at)
 SELECT w.id,a.old_value,max(a.written_at+a.old_ttl*1000000000) FROM dns_watches w
 JOIN entry_actions a ON a.group_id=w.entry_group_id WHERE a.written_at IS NOT NULL GROUP BY w.id,a.old_value;
DELETE FROM dns_watch_tasks WHERE watch_id IN (SELECT id FROM dns_watches WHERE entry_group_id IS NOT NULL);
UPDATE dns_watch_paths SET active=0,consecutive_matches=0,consecutive_mismatches=0,sampled_at=NULL
 WHERE watch_id IN (SELECT id FROM dns_watches WHERE entry_group_id IS NOT NULL);
INSERT INTO entry_dns_audits(group_id,next_check_at) SELECT group_id,0 FROM managed_records;`)
	return migration.String()
}()
