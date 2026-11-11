package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"nbpass/internal/clusterharness/fakedns"
	"nbpass/internal/controlpb"
	"nbpass/internal/dnsprovider"
)

func TestManagedRecordMigrationPreservesBindingsPublicationsAndPendingActions(t *testing.T) {
	db, err := diskDependencies(t.TempDir()).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for v, m := range migrations[:35] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", v+1)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','既有节点',0);
 INSERT INTO node_regions(node_id,public_ipv4) VALUES('n','8.8.8.8');
 INSERT INTO entry_groups(id,name,auto_failback_paused) VALUES('g','既有组',1);
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0);
 INSERT INTO dns_accounts(id,name,provider) VALUES('dns','原账号','dnspod');
 INSERT INTO managed_records(group_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings)
 VALUES('g','dns','dnspod','example.com','100','entry','默认','0','9.9.9.9',600,'[]');
 INSERT INTO entry_actions(id,group_id,version,kind,node_id,old_value,new_value,old_ttl,stage,created_at,write_started,last_error,failback_notified)
 VALUES('a','g',7,'manual_switch','n','9.9.9.9','8.8.8.8',600,'结果未确认',123,1,'等待读回',1);
 INSERT INTO entry_publications(group_id,desired_version,active_node_id,expected_value,current_action_id) VALUES('g',7,'n','8.8.8.8','a');`)
	if err != nil {
		t.Fatal(err)
	}
	p := &Panel{db: db}
	if err := p.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var desired, generation, oldTTL, started, notified, paused, enabled, version int
	var active, action, confirmed, owner, oldValue, newValue, stage, message string
	err = db.QueryRow(`SELECT s.desired_version,s.active_node_id,s.current_action_id,n.confirmed_ipv4,m.owner_kind,m.binding_generation,
 r.old_value,r.new_value,r.old_ttl,r.write_started,r.stage,a.last_error,a.failback_notified,g.auto_failback_paused,r.old_enabled
 FROM entry_publications s JOIN entry_groups g ON g.id=s.group_id JOIN node_regions n ON n.node_id=s.active_node_id
 JOIN managed_records m ON m.group_id=s.group_id JOIN entry_actions a ON a.id=s.current_action_id JOIN entry_action_records r ON r.action_id=a.id`).Scan(&desired, &active, &action, &confirmed, &owner, &generation, &oldValue, &newValue, &oldTTL, &started, &stage, &message, &notified, &paused, &enabled)
	if err != nil || desired != 7 || active != "n" || action != "a" || confirmed != "8.8.8.8" || owner != "group" || generation != 1 || oldValue != "9.9.9.9" || newValue != "8.8.8.8" || oldTTL != 600 || started != 1 || stage != EntryUnconfirmed || message != "等待读回" || notified != 1 || paused != 1 || enabled != 1 {
		t.Fatalf("迁移未完整保留旧状态：%v %d %s %s %s %s %d %s %s %d %d %s %s %d %d %d", err, desired, active, action, confirmed, owner, generation, oldValue, newValue, oldTTL, started, stage, message, notified, paused, enabled)
	}
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatal(version, err)
	}
}

func submitModelAction(t *testing.T, p *Panel, req RecordActionRequest) EntryAction {
	t.Helper()
	tx, err := p.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	a, err := p.submitRecordActionTx(context.Background(), tx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestManagedRecordPausedValueUpdateStaysPausedThroughRealDNSPodAdapter(t *testing.T) {
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	r := dns.Record("example.com", "100")
	r.Enabled = false
	dns.Set("example.com", r)
	if _, err := p.db.Exec("UPDATE managed_records SET current_enabled=0 WHERE id='m1'"); err != nil {
		t.Fatal(err)
	}
	service := httptest.NewServer(dns)
	defer service.Close()
	p.deps.DNSProvider = dnsprovider.DNSPod{URL: service.URL, Client: service.Client()}
	a := submitModelAction(t, p, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "address_update", Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: false}}})
	if err := p.coordinator.execute(a); err != nil {
		t.Fatal(err)
	}
	r = dns.Record("example.com", "100")
	if r.Enabled || r.Value != "8.8.8.8" || dns.Writes() != 1 {
		t.Fatalf("修改暂停记录时误启用：%+v，%v", r, dns.Calls())
	}
}

func TestManagedRecordEnableRejectionKeepsPartialSuccessAndNotifies(t *testing.T) {
	dns := fakedns.NewDNSPod()
	dns.EnableError = "500025"
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	r := dns.Record("example.com", "200")
	r.Enabled = false
	dns.Set("example.com", r)
	if _, err := p.db.Exec("UPDATE managed_records SET current_enabled=0 WHERE id='m2'"); err != nil {
		t.Fatal(err)
	}
	a := submitModelAction(t, p, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{
		{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: true},
		{ManagedRecordID: "m2", BindingGeneration: 1, Value: "4.2.2.2", Enabled: true},
	}})
	if err := p.coordinator.execute(a); err != nil {
		t.Fatal(err)
	}
	var first, second, stage, current string
	var enabled bool
	if err := p.db.QueryRow("SELECT stage FROM entry_action_records WHERE managed_record_id='m1'").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT stage FROM entry_action_records WHERE managed_record_id='m2'").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT stage FROM entry_actions WHERE id=?", a.ID).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT current_value,current_enabled FROM managed_records WHERE id='m2'").Scan(&current, &enabled); err != nil {
		t.Fatal(err)
	}
	if first != EntryWritten || second != EntryFailed || stage != EntryPartial || current != "4.2.2.2" || enabled {
		t.Fatalf("未保留已成功的逐条结果：%s %s %s %s %v", first, second, stage, current, enabled)
	}
	if err := p.notifyRecordRejections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.notifyRecordRejections(context.Background()); err != nil {
		t.Fatal(err)
	}
	var notifications int
	if err := p.db.QueryRow("SELECT count(*) FROM telegram_deliveries WHERE message LIKE '%500025%'").Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 || dns.Writes() != 3 {
		t.Fatal(notifications, dns.Calls())
	}
}

func TestManagedRecordLastEnabledRecordWaitsForConfirmedEnable(t *testing.T) {
	for _, outcome := range []string{"成功", "明确拒绝", "结果未知"} {
		t.Run(outcome, func(t *testing.T) {
			dns := fakedns.NewDNSPod()
			if outcome == "明确拒绝" {
				dns.EnableError = "500025"
			}
			if outcome == "结果未知" {
				dns.Status = func(context.Context) error { return context.DeadlineExceeded }
			}
			p := recordModelPanel(t, t.TempDir(), dns)
			seedRecordModel(t, p, dns)
			r := dns.Record("example.com", "100")
			r.Enabled = false
			dns.Set("example.com", r)
			if _, err := p.db.Exec("UPDATE managed_records SET current_enabled=0 WHERE id='m1'"); err != nil {
				t.Fatal(err)
			}
			a := submitModelAction(t, p, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{
				{ManagedRecordID: "m2", BindingGeneration: 1, Value: "1.1.1.1", Enabled: false},
				{ManagedRecordID: "m1", BindingGeneration: 1, Value: "9.9.9.9", Enabled: true},
			}})
			_ = p.coordinator.execute(a)
			if outcome == "成功" {
				if dns.Record("example.com", "200").Enabled {
					t.Fatal("启用成功后未暂停原记录")
				}
			} else if !dns.Record("example.com", "200").Enabled {
				t.Fatal("未确认目标启用却暂停了最后的原记录")
			}
		})
	}
}

func TestManagedRecordUnknownEnableIsReadBackBeforePausingLastOriginal(t *testing.T) {
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	r := dns.Record("example.com", "100")
	r.Enabled = false
	dns.Set("example.com", r)
	if _, err := p.db.Exec("UPDATE managed_records SET current_enabled=0 WHERE id='m1'"); err != nil {
		t.Fatal(err)
	}
	statuses := 0
	dns.Status = func(context.Context) error {
		statuses++
		if statuses == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}
	a := submitModelAction(t, p, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "9.9.9.9", Enabled: true}, {ManagedRecordID: "m2", BindingGeneration: 1, Value: "1.1.1.1", Enabled: false}}})
	if err := p.coordinator.execute(a); err == nil {
		t.Fatal("启用超时被标为完成")
	}
	if !dns.Record("example.com", "200").Enabled {
		t.Fatal("未确认启用前暂停最后记录")
	}
	if err := p.coordinator.execute(a); err != nil {
		t.Fatal(err)
	}
	var stage string
	if err := p.db.QueryRow("SELECT stage FROM entry_actions WHERE id=?", a.ID).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if stage != EntryWritten || dns.Record("example.com", "200").Enabled || statuses != 2 {
		t.Fatal("读回成功后未续完暂停步骤", stage, dns.Calls())
	}
}

func TestManagedRecordLateResultCannotUpdateNewBindingOrDifferentAction(t *testing.T) {
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	a := submitModelAction(t, p, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: true}}})
	rows, err := loadActionRecords(context.Background(), p.db, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := rows[0]
	wrong := a
	wrong.ID = "另一个动作"
	if err := p.coordinator.recordResult(wrong, r, EntryWritten, ""); err == nil {
		t.Fatal("接纳了错误动作 ID 的回包")
	}
	wrongRecord := r
	wrongRecord.RecordID = "其他记录"
	if err := p.coordinator.recordResult(a, wrongRecord, EntryWritten, ""); err == nil {
		t.Fatal("接纳了错误远端身份的回包")
	}
	wrongGeneration := r
	wrongGeneration.BindingGeneration++
	if err := p.coordinator.recordResult(a, wrongGeneration, EntryWritten, ""); err == nil {
		t.Fatal("接纳了错误绑定代次的回包")
	}
	// 模拟恢复核对封存旧动作之后更换绑定，迟到请求仍握着原来的快照。
	if _, err := p.db.Exec("UPDATE entry_action_records SET stage=? WHERE action_id=?", EntryArchived, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE entry_actions SET stage=? WHERE id=?", EntryArchived, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("UPDATE managed_records SET binding_generation=2,record_id='300',current_value='4.2.2.2' WHERE id='m1'"); err != nil {
		t.Fatal(err)
	}
	if err := p.coordinator.recordResult(a, r, EntryWritten, ""); err == nil {
		t.Fatal("迟到结果写到了新绑定")
	}
	var value string
	var generation int
	if err := p.db.QueryRow("SELECT current_value,binding_generation FROM managed_records WHERE id='m1'").Scan(&value, &generation); err != nil {
		t.Fatal(err)
	}
	if value != "4.2.2.2" || generation != 2 || dns.Writes() != 0 {
		t.Fatal(value, generation, dns.Calls())
	}
}

func TestManagedRecordRestoreArchivesRecordRowsWithoutReplayingOldAction(t *testing.T) {
	dir := t.TempDir()
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, dir, dns)
	seedRecordModel(t, p, dns)
	if _, err := p.db.Exec(`DELETE FROM managed_records WHERE id='m2';
 UPDATE managed_records SET owner_kind='group',node_id=NULL WHERE id='m1';
 INSERT INTO node_regions(node_id,public_ipv4,confirmed_ipv4) VALUES('n','9.9.9.9','9.9.9.9');`); err != nil {
		t.Fatal(err)
	}
	a := submitModelAction(t, p, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: true}}})
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRestore(context.Background(), diskDependencies(dir).Persistence, time.Now()); err != nil {
		t.Fatal(err)
	}
	p = recordModelPanel(t, dir, dns)
	if _, err := p.db.Exec("UPDATE recovery_nodes SET verified=1,high_water=1"); err != nil {
		t.Fatal(err)
	}
	if err := p.reconcileRecoveryGroup(context.Background(), "g"); err != nil {
		t.Fatal(err)
	}
	var stage string
	if err := p.db.QueryRow("SELECT stage FROM entry_action_records WHERE action_id=?", a.ID).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	if stage != EntryArchived || dns.Writes() != 0 {
		t.Fatal("恢复没有逐条封存旧动作", stage, dns.Calls())
	}
	allowed, err := recoveryRecordAllowed(context.Background(), p.db, "m1", 1)
	if err != nil || !allowed {
		t.Fatal("核对后未逐条放行", allowed, err)
	}
	tx, err := p.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := unbindRecordTx(context.Background(), tx, "m1"); err != nil {
		t.Fatal("封存动作仍阻止解绑", err)
	}
}

func TestManagedRecordReportedAddressDoesNotReplaceConfirmedAddress(t *testing.T) {
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	tx, err := p.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.confirmNodeAddressTx(context.Background(), tx, "n", "8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.recordNodeIP(context.Background(), "n", "1.1.1.1"); err != nil {
			t.Fatal(err)
		}
	}
	var reported, confirmed string
	if err := p.db.QueryRow("SELECT public_ipv4,confirmed_ipv4 FROM node_regions WHERE node_id='n'").Scan(&reported, &confirmed); err != nil {
		t.Fatal(err)
	}
	if reported != "1.1.1.1" || confirmed != "8.8.8.8" || dns.Writes() != 0 {
		t.Fatal(reported, confirmed, dns.Calls())
	}
	var notified int
	if err := p.db.QueryRow("SELECT count(*) FROM telegram_deliveries WHERE message LIKE '%地址变化%' ").Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Fatal("未通知面板外地址变化，或重复刷屏", notified)
	}
}

func TestManagedRecordRecoveryConfirmsLatestControlledAddressWithoutRollingBackManualSync(t *testing.T) {
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	if _, err := p.db.Exec("INSERT INTO node_regions(node_id,public_ipv4,confirmed_ipv4) VALUES('n','9.9.9.9','9.9.9.9')"); err != nil {
		t.Fatal(err)
	}
	now := p.deps.Clock.Now()
	importChanged := func(id, old, value string, at time.Time) {
		t.Helper()
		tx, err := p.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		progress := &controlpb.RotationProgress{ActionId: id, OldIp: old, NewIp: value, State: "changed", Sequence: 1, Reason: "changed", ObservationComplete: true}
		if err := p.importRecoveryRotation(context.Background(), tx, "n", &controlpb.RecoveryRotation{ActionId: id, AttemptedAt: at.UnixNano(), State: "changed", Progress: progress}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	importChanged("recovered-new", "9.9.9.9", "8.8.8.8", now.Add(-time.Hour))
	var confirmed string
	if err := p.db.QueryRow("SELECT confirmed_ipv4 FROM node_regions WHERE node_id='n'").Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed != "8.8.8.8" {
		t.Fatal("恢复受控换 IP 成功没有更新确认地址", confirmed)
	}
	tx, err := p.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.confirmNodeAddressTx(context.Background(), tx, "n", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	importChanged("recovered-old", "4.2.2.2", "9.9.9.9", now.Add(-2*time.Hour))
	importChanged("recovered-new", "9.9.9.9", "8.8.8.8", now.Add(-time.Hour))
	if err := p.db.QueryRow("SELECT confirmed_ipv4 FROM node_regions WHERE node_id='n'").Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed != "1.1.1.1" {
		t.Fatal("历史受控动作回滚了后来的手动同步", confirmed)
	}
}

func TestManagedRecordNodePublicationHasOwnVersionAndExplicitEnableStep(t *testing.T) {
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	if _, err := p.db.Exec("UPDATE managed_records SET owner_kind='node',group_id=NULL WHERE id='m1'"); err != nil {
		t.Fatal(err)
	}
	a := submitModelAction(t, p, RecordActionRequest{OwnerNodeID: "n", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: true}}})
	if err := p.coordinator.execute(a); err != nil {
		t.Fatal(err)
	}
	if calls := dns.Calls(); len(calls) != 3 || calls[1] != "write" || calls[2] != "status" {
		t.Fatal("节点首次发布必须先改值，再明确启用", calls)
	}
	var version int
	var current string
	if err := p.db.QueryRow("SELECT desired_version,current_action_id FROM node_publications WHERE node_id='n'").Scan(&version, &current); err != nil {
		t.Fatal(err)
	}
	if version != 1 || current != a.ID {
		t.Fatal(version, current)
	}
	old := submitModelAction(t, p, RecordActionRequest{OwnerNodeID: "n", NodeID: "n", Kind: "address_update", ExpectedVersion: 1, Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "4.2.2.2", Enabled: true}}})
	newer := submitModelAction(t, p, RecordActionRequest{OwnerNodeID: "n", NodeID: "n", Kind: "address_update", ExpectedVersion: 2, Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "1.1.1.1", Enabled: true}}})
	if err := p.coordinator.execute(old); err != nil {
		t.Fatal(err)
	}
	if err := p.coordinator.execute(newer); err != nil {
		t.Fatal(err)
	}
	if dns.Record("example.com", "100").Value != "1.1.1.1" || dns.Writes() != 3 {
		t.Fatal("过时节点动作没有失效", dns.Calls())
	}
	tx, err := p.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := p.submitRecordActionTx(context.Background(), tx, RecordActionRequest{OwnerNodeID: "n", NodeID: "n", Kind: "address_update", ExpectedVersion: 1, Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: true}}}); err == nil {
		t.Fatal("接受了过时期望版本")
	}
}

func TestManagedRecordRestoreChecksEachAddressAndStatusBeforeReleasingGroup(t *testing.T) {
	dir := t.TempDir()
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, dir, dns)
	seedRecordModel(t, p, dns)
	r := dns.Record("example.com", "200")
	r.Enabled = false
	dns.Set("example.com", r)
	if _, err := p.db.Exec(`INSERT INTO node_regions(node_id,public_ipv4,confirmed_ipv4) VALUES('n','4.2.2.2','9.9.9.9'),('other','1.1.1.1','1.1.1.1');`); err != nil {
		t.Fatal(err)
	}
	_ = submitModelAction(t, p, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: true}, {ManagedRecordID: "m2", BindingGeneration: 1, Value: "4.2.2.2", Enabled: true}}})
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRestore(context.Background(), diskDependencies(dir).Persistence, time.Now()); err != nil {
		t.Fatal(err)
	}
	p = recordModelPanel(t, dir, dns)
	p.coordinator.close()
	if _, err := p.db.Exec("UPDATE recovery_nodes SET verified=1,high_water=1"); err != nil {
		t.Fatal(err)
	}
	reads := 0
	dns.Read = func(context.Context) error {
		reads++
		if reads == 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := p.reconcileRecoveryGroup(context.Background(), "g"); err == nil {
		t.Fatal("第二条读失败却整体放行")
	}
	var groupStage string
	if err := p.db.QueryRow("SELECT publication_status FROM entry_groups WHERE id='g'").Scan(&groupStage); err != nil {
		t.Fatal(err)
	}
	if groupStage == EntryWritten {
		t.Fatal("部分核对被显示成整组完成")
	}
	first, err := recoveryRecordAllowed(context.Background(), p.db, "m1", 1)
	if err != nil || !first {
		t.Fatal(first, err)
	}
	second, err := recoveryRecordAllowed(context.Background(), p.db, "m2", 1)
	if err != nil || second {
		t.Fatal(second, err)
	}
	if allowed, err := recoveryGroupAllowed(context.Background(), p.db, "g"); err != nil || allowed {
		t.Fatal("部分核对提前放行组", allowed, err)
	}
	if err := p.reconcileRecoveryGroup(context.Background(), "g"); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var value string
	if err := p.db.QueryRow("SELECT current_enabled,current_value FROM managed_records WHERE id='m2'").Scan(&enabled, &value); err != nil {
		t.Fatal(err)
	}
	if reads != 3 || enabled || value != "1.1.1.1" || dns.Writes() != 0 {
		t.Fatal("重读已核对记录、遗漏暂停状态或重放动作", reads, enabled, value, dns.Calls())
	}
	if allowed, err := recoveryGroupAllowed(context.Background(), p.db, "g"); err != nil || !allowed {
		t.Fatal("整组核对成功未在同一事务放行", allowed, err)
	}
}

func TestManagedRecordRecoveryRechecksRecordsEvenWhenGroupFlagIsStale(t *testing.T) {
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, t.TempDir(), dns)
	seedRecordModel(t, p, dns)
	_, err := p.db.Exec(`INSERT INTO node_regions(node_id,public_ipv4,confirmed_ipv4) VALUES('n','9.9.9.9','9.9.9.9'),('other','1.1.1.1','1.1.1.1');
 INSERT INTO recovery_state(id,restored_at) VALUES(1,1);
 INSERT INTO recovery_nodes(node_id,verified,high_water) VALUES('n',1,1),('other',1,1);
 INSERT INTO recovery_groups(group_id,verified) VALUES('g',1);
 INSERT INTO recovery_records(managed_record_id,binding_generation,verified) VALUES('m1',1,0),('m2',1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	// 物理交错断点：节点补报使逐条状态失效，但旧调用者留下了组已核对标记。
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "record-model-test-password"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "record-model-test-password"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	diskRequest(t, p, "POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, cookie, 200)
	allowed, err := recoveryRecordAllowed(context.Background(), p.db, "m1", 1)
	if err != nil || !allowed {
		t.Fatal("组标记过时导致记录永久无法重新核对", allowed, err)
	}
	view := diskRequest(t, p, "GET", "/api/recovery", nil, cookie, 200)
	var result struct {
		Records []struct {
			Verified bool `json:"verified"`
		}
	}
	if err := json.Unmarshal(view.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 || !result.Records[0].Verified || !result.Records[1].Verified {
		t.Fatal("恢复接口遗漏逐条核对状态", view.Body.String())
	}
	// 恢复后新建组没有旧清单行，后续节点水位失效时也必须能重新核对。
	if _, err := p.db.Exec(`DELETE FROM recovery_groups WHERE group_id='g';UPDATE recovery_records SET verified=0 WHERE managed_record_id='m1'`); err != nil {
		t.Fatal(err)
	}
	diskRequest(t, p, "POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, cookie, 200)
	allowed, err = recoveryRecordAllowed(context.Background(), p.db, "m1", 1)
	if err != nil || !allowed {
		t.Fatal("没有组恢复清单行时无法重新核对", allowed, err)
	}
}

func recordModelPanel(t *testing.T, dir string, dns *fakedns.DNSPod) *Panel {
	t.Helper()
	d := diskDependencies(dir)
	d.DNSProvider = dns
	p, err := Open(context.Background(), Config{PublicOrigin: "https://panel.test"}, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func seedRecordModel(t *testing.T, p *Panel, dns *fakedns.DNSPod) {
	t.Helper()
	_, err := p.db.Exec(`INSERT INTO nodes(id,name,created_at) VALUES('n','入口',0),('other','备用',0);
 INSERT INTO entry_groups(id,name) VALUES('g','全部发布底座');
 INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0),('g','other',1);
 INSERT INTO dns_accounts(id,name,provider) VALUES('dns','DNSPod','dnspod');
 INSERT INTO managed_records(id,owner_kind,group_id,node_id,account_id,provider,domain,record_id,name,line,line_id,current_value,current_ttl,warnings)
 VALUES('m1','member','g','n','dns','dnspod','example.com','100','entry','默认','0','9.9.9.9',600,'[]'),
 ('m2','member','g','other','dns','dnspod','example.com','200','entry','默认','0','1.1.1.1',600,'[]');`)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(dnsCredentials{SecretID: "AKIDtest12345", SecretKey: "testSecretKey"})
	if err := p.secrets.Put(context.Background(), "dnspod.dns", data); err != nil {
		t.Fatal(err)
	}
	dns.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	dns.Set("example.com", dnsprovider.Record{ID: "200", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "1.1.1.1", TTL: 600, Enabled: true})
}

func TestManagedRecordActionRestartKeepsConfirmedRowsAndReadsUnknownRow(t *testing.T) {
	dir := t.TempDir()
	dns := fakedns.NewDNSPod()
	p := recordModelPanel(t, dir, dns)
	seedRecordModel(t, p, dns)
	writes := 0
	dns.Write = func(context.Context) error {
		writes++
		if writes == 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	tx, err := p.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.submitRecordActionTx(context.Background(), tx, RecordActionRequest{GroupID: "g", NodeID: "n", Kind: "first_publish", Records: []ManagedRecordChange{
		{ManagedRecordID: "m1", BindingGeneration: 1, Value: "8.8.8.8", Enabled: true},
		{ManagedRecordID: "m2", BindingGeneration: 1, Value: "4.2.2.2", Enabled: true},
	}})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// 底座边界由调用者明确推进，避免真实退避计时影响重启断点。
	if err := p.coordinator.execute(a); err == nil {
		t.Fatal("第二条超时被标为整体完成")
	}
	var first, second string
	if err := p.db.QueryRow("SELECT stage FROM entry_action_records WHERE managed_record_id='m1'").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT stage FROM entry_action_records WHERE managed_record_id='m2'").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != EntryWritten || second != EntryUnconfirmed {
		t.Fatal(first, second)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = recordModelPanel(t, dir, dns)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var stage string
		err := p.db.QueryRow("SELECT stage FROM entry_actions WHERE id=?", a.ID).Scan(&stage)
		if err != nil {
			t.Fatal(err)
		}
		if stage == EntryWritten {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(stage)
		}
		time.Sleep(time.Millisecond * 5)
	}
	if dns.Writes() != 2 || dns.Record("example.com", "100").Value != "8.8.8.8" || dns.Record("example.com", "200").Value != "4.2.2.2" {
		t.Fatalf("重启重发了已确认记录：%v", dns.Calls())
	}
	if calls := dns.Calls(); len(calls) != 5 || calls[4] != "read" {
		t.Fatal(calls)
	}
}
