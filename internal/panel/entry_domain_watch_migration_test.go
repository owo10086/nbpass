package panel

import (
	"context"
	"fmt"
	"testing"
)

// 验证从并行票基线 28 升级，不丢发布/历史，不沿用手工期望和旧任务。
func TestEntryDomainWatchMigrationAdoptsExistingWatch(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for version, m := range migrations[:28] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','活动入口',0);
INSERT INTO entry_groups(id,name,auto_failback_paused) VALUES('g','已有入口组',1);
INSERT INTO entry_groups(id,name) VALUES('g2','同域名另一受管记录');
INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0);
INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g2','n',0);
INSERT INTO dns_accounts(id,name,provider) VALUES('a','已有账号','dnspod');
INSERT INTO managed_records(group_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings)
 VALUES('g','a','dnspod','example.com','100','entry','默认','0','8.8.8.8',60,'[]');
INSERT INTO managed_records(group_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings)
 VALUES('g2','a','dnspod','example.com','101','entry','默认','0','1.1.1.1',60,'[]');
INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,written_at)
 VALUES('action','g',4,'manual_switch','n','9.9.9.9','8.8.8.8',600,'服务商已写入',1,1);
INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id,written_at)
 VALUES('g',4,'n','8.8.8.8','action',1);
UPDATE entry_actions SET written_at=unixepoch('now')*1000000000;
UPDATE entry_publications SET written_at=unixepoch('now')*1000000000;
INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,written_at)
 VALUES('earlier','g',3,'manual_switch','n','4.2.2.2','9.9.9.9',600,'服务商已写入',1,(unixepoch('now')-30)*1000000000);
INSERT INTO dns_watches(id,domain,expected_ips,expected_version,next_sample_at,created_at)
 VALUES('manual','entry.example.com','["4.2.2.2"]',99,0,0);
INSERT INTO dns_watch_paths(watch_id,path_id,node_id,kind,resolver,expected_version,round,ips,consecutive_matches,sampled_at)
 VALUES('manual','old','','doh','https://dns.example/dns-query',99,0,'["4.2.2.2"]',5,1);
INSERT INTO dns_watch_history(watch_id,path_id,sampled_at,old_ips,ips,status,changed)
 VALUES('manual','old',0,'["8.8.8.8"]','["4.2.2.2"]','期望不符',1);
INSERT INTO dns_watch_tasks(id,watch_id,path_id,node_id,process_instance,domain,resolver,kind,expected_version,round,expires_at)
 VALUES('old-task','manual','old','','','entry.example.com','https://dns.example/dns-query','doh',99,0,9223372036854775807);`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var owner, ips string
	var version, pause, count int
	if err = p.db.QueryRow("SELECT entry_group_id,expected_ips,expected_version FROM dns_watches WHERE id='manual'").Scan(&owner, &ips, &version); err != nil || owner != "g" || ips != `["8.8.8.8"]` || version != 4 {
		t.Fatal("自动纳管丢失发布", owner, ips, version, err)
	}
	if err = p.db.QueryRow("SELECT auto_failback_paused FROM entry_groups WHERE id='g'").Scan(&pause); err != nil || pause != 1 {
		t.Fatal("丢失手动暂停", pause, err)
	}
	for _, query := range []string{"SELECT count(*) FROM dns_watch_tasks WHERE id='old-task'", "SELECT count(*) FROM dns_watch_paths WHERE watch_id='manual' AND path_id='old' AND (active<>0 OR consecutive_matches<>0 OR sampled_at IS NOT NULL)"} {
		if err = p.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatal("旧证据没有失效", count, err)
		}
	}
	if err = p.db.QueryRow("SELECT count(*) FROM dns_watch_history WHERE watch_id='manual'").Scan(&count); err != nil || count != 1 {
		t.Fatal("丢失变化历史", count, err)
	}
	if err = p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal("迁移版本错误", version, err)
	}
	if err = p.db.QueryRow("SELECT count(*) FROM dns_watches WHERE entry_group_id='g2' AND domain='entry.example.com'").Scan(&count); err != nil || count != 1 {
		t.Fatal("同域名另一入口组丢失监控", count, err)
	}
	if err = p.db.QueryRow("SELECT count(*) FROM entry_dns_previous WHERE watch_id='manual' AND ip='4.2.2.2'").Scan(&count); err != nil || count != 1 {
		t.Fatal("遗漏升级前较早但未过期的缓存", count, err)
	}
}
