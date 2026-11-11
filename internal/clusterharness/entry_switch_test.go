package clusterharness_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/panel"
)

func manualSwitch(c *clusterharness.Client, gid, id, ip string, version int, status int) map[string]any {
	return c.Do("POST", "/api/entry-groups/"+gid+"/switch", map[string]any{
		"active_node_id": id, "ipv4": ip, "expected_version": version, "confirm_inbound": true,
	}, status)
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("未到达外部 DNS 写入")
	}
}

func TestEntryManualSwitchSerializesAndLateResultPreservesNewDesire(t *testing.T) {
	_, c, fake, account, gid := dnsBrowser(t)
	a := memberOf(c, gid)
	b, d := addNode(c, "入口 B")["id"].(string), addNode(c, "入口 C")["id"].(string)
	saveMembers(c, gid, 200, a, b, d)
	second, third, releaseSecond, releaseThird := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var onceSecond, onceThird sync.Once
	defer onceSecond.Do(func() { close(releaseSecond) })
	defer onceThird.Do(func() { close(releaseThird) })
	var writes atomic.Int32
	fake.Write = func(ctx context.Context) error {
		var release <-chan struct{}
		switch writes.Add(1) {
		case 2:
			close(second)
			release = releaseSecond
		case 3:
			close(third)
			release = releaseThird
		default:
			return nil
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, a, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	old := manualSwitch(c, gid, b, "1.1.1.1", 1, 202)
	waitSignal(t, second)
	newer := manualSwitch(c, gid, d, "4.2.2.2", 2, 202)
	manualSwitch(c, gid, a, "8.8.8.8", 1, 409)
	if fake.Writes() != 2 || fake.Record("example.com", "100").Value != "1.1.1.1" {
		t.Fatal("第二次切换未排队或旧期望被重放")
	}
	// 旧动作完成时，新动作已成为当前期望，且新的服务商响应尚未返回。
	onceSecond.Do(func() { close(releaseSecond) })
	waitSignal(t, third)
	pub := publication(c, gid)
	if pub["desired_version"] != float64(3) || pub["active_node_id"] != d || pub["expected_value"] != "4.2.2.2" || pub["written_at"] != nil || pub["action"].(map[string]any)["id"] != newer["id"] || pub["action"].(map[string]any)["stage"] != "期望已变更" {
		t.Fatalf("迟到的旧动作结果覆盖了新期望或提前确认: %v", pub)
	}
	history := c.Do("GET", "/api/entry-groups/"+gid+"/actions", nil, 200)["actions"].([]any)
	if len(history) != 3 || history[1].(map[string]any)["id"] != old["id"] || history[1].(map[string]any)["stage"] != "服务商已写入" {
		t.Fatalf("旧动作没有独立完成: %v", history)
	}
	onceThird.Do(func() { close(releaseThird) })
	last := awaitDNSStage(t, c, gid, "服务商已写入")
	if last["old_value"] != "1.1.1.1" || fake.Record("example.com", "100").Value != "4.2.2.2" || fake.Writes() != 3 {
		t.Fatal("串行切换顺序或最终记录错误")
	}
}

func TestEntryManualSwitchPausePersistsUntilExplicitResume(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	a := memberOf(c, gid)
	b := addNode(c, "入口 B")["id"].(string)
	saveMembers(c, gid, 200, a, b)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, a, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	manualSwitch(c, gid, a, "8.8.8.8", 1, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	if publication(c, gid)["auto_failback_paused"] != false {
		t.Fatal("手动选择首选入口不应主动暂停")
	}
	manualSwitch(c, gid, b, "1.1.1.1", 2, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	pub := publication(c, gid)
	if pub["auto_failback_paused"] != true || pub["auto_failback_status"] != "自动回切已暂停" || pub["preferred_node_id"] != a {
		t.Fatalf("暂停回切没有持久化或状态错误: %v", pub)
	}
	// 改排序和再次手动选首选入口都不能隐式清除暂停。
	saveMembers(c, gid, 200, b, a)
	manualSwitch(c, gid, b, "1.1.1.1", 3, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	if publication(c, gid)["auto_failback_paused"] != true {
		t.Fatal("排序或切到首选入口隐式清除了暂停")
	}
	path := "/api/entry-groups/" + gid + "/resume-failback"
	c.Do("POST", path, map[string]int{"expected_version": 3}, 409)
	before := fake.Writes()
	c.Do("POST", path, map[string]int{"expected_version": 4}, 204)
	pub = publication(c, gid)
	if pub["auto_failback_paused"] != false || pub["auto_failback_status"] != "自动回切已启用" || pub["active_node_id"] != b || fake.Writes() != before {
		t.Fatalf("恢复没有清除暂停或直接改了 DNS: %v", pub)
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	if publication(c, gid)["auto_failback_paused"] != false {
		t.Fatal("恢复未持久化")
	}
}

func TestEntryManualSwitchProtectsPendingMembersAndRecoversQueue(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	a := memberOf(c, gid)
	b, d := addNode(c, "入口 B")["id"].(string), addNode(c, "入口 C")["id"].(string)
	saveMembers(c, gid, 200, a, b, d)
	entered := make(chan struct{})
	var writes atomic.Int32
	fake.Write = func(ctx context.Context) error {
		if writes.Add(1) == 2 {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, a, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	manualSwitch(c, gid, b, "1.1.1.1", 1, 202)
	waitSignal(t, entered)
	manualSwitch(c, gid, d, "4.2.2.2", 2, 202)
	saveMembers(c, gid, 409, a, d) // B 是旧在途动作，仍不能移除。
	saveMembers(c, gid, 409, a, b) // C 是最新期望，也不能移除。
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitDNSStage(t, c, gid, "服务商已写入")
	if fake.Writes() != 3 || fake.Record("example.com", "100").Value != "4.2.2.2" || publication(c, gid)["auto_failback_paused"] != true {
		t.Fatal("重启重复写入或丢失排队动作/暂停状态")
	}
	saveMembers(c, gid, 200, a, d)
	// 仍保留动作历史引用时，删除必须返回明确引用，不能泄漏存储错误。
	c.Do("DELETE", "/api/nodes/"+b, nil, 409)
}

func TestEntryManualSwitchValidationAndGates(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	a := memberOf(c, gid)
	path := "/api/entry-groups/" + gid
	manualSwitch(c, gid, a, "8.8.8.8", 0, 409)
	c.Do("PUT", path+"/managed-record", binding(account), 200)
	manualSwitch(c, gid, a, "8.8.8.8", 0, 409)
	firstPublish(c, gid, a, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	for _, body := range []map[string]any{
		{"active_node_id": a, "ipv4": "8.8.8.8", "expected_version": 1},
		{"active_node_id": a, "ipv4": "8.8.8.8", "confirm_inbound": true},
		{"active_node_id": a, "ipv4": "127.0.0.1", "expected_version": 1, "confirm_inbound": true},
		{"active_node_id": "missing", "ipv4": "8.8.8.8", "expected_version": 1, "confirm_inbound": true},
	} {
		c.Do("POST", path+"/switch", body, 400)
	}
	for _, suffix := range []string{"/switch", "/resume-failback"} {
		h.Client().Do("POST", path+suffix, map[string]int{"expected_version": 1}, 401)
		r := c.Request("POST", path+suffix, map[string]int{"expected_version": 1}, map[string]string{"Origin": "https://other.example"})
		r.Body.Close()
		if r.StatusCode != 403 {
			t.Fatal("跨源入口动作被放行")
		}
	}
	c.Do("POST", path+"/resume-failback", map[string]any{}, 400)
	if fake.Writes() != 1 || publication(c, gid)["desired_version"] != float64(1) {
		t.Fatal("非法请求产生了入口动作")
	}
}

func TestEntryManualSwitchPersistenceFailureIsAtomicAndVersionIsExclusive(t *testing.T) {
	fake := clusterharness.NewDNSPod()
	fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	faults := &clusterharness.PersistenceFaults{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		faults.Base = d.Persistence
		d.Persistence, d.DNSProvider, d.Passwords = faults, fake, nodePasswords{}
	})
	c := h.Client()
	setup(c)
	login(c)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "testSecretKey"}, 201)["id"].(string)
	a, b := addNode(c, "入口 A")["id"].(string), addNode(c, "入口 B")["id"].(string)
	gid := entryGroup(c, a)["id"].(string)
	saveMembers(c, gid, 200, a, b)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, a, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	if err := faults.FailEntryActions("INSERT"); err != nil {
		t.Fatal(err)
	}
	manualSwitch(c, gid, b, "1.1.1.1", 1, 503)
	pub := publication(c, gid)
	if pub["auto_failback_paused"] != false || pub["desired_version"] != float64(1) || pub["active_node_id"] != a || fake.Writes() != 1 {
		t.Fatalf("持久化失败没有原子回滚暂停和期望: %v", pub)
	}
	if err := faults.FailEntryActions(""); err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			r := c.Request("POST", "/api/entry-groups/"+gid+"/switch", map[string]any{"active_node_id": b, "ipv4": "1.1.1.1", "expected_version": 1, "confirm_inbound": true}, nil)
			r.Body.Close()
			statuses <- r.StatusCode
		}()
	}
	counts := map[int]int{}
	for range 2 {
		counts[<-statuses]++
	}
	if counts[202] != 1 || counts[409] != 1 {
		t.Fatalf("相同期望版本接受了重复并发切换: %v", counts)
	}
	awaitDNSStage(t, c, gid, "服务商已写入")
	if fake.Writes() != 2 || publication(c, gid)["auto_failback_paused"] != true {
		t.Fatal("并发切换重复写入或没有暂停")
	}
}

func TestEntryManualSwitchKeepsPublishedMemberUntilWriteConfirmed(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var hold atomic.Bool
	var once sync.Once
	defer once.Do(func() { close(release) })
	fake.Read = func(ctx context.Context) error {
		if !hold.Load() {
			return nil
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a := memberOf(c, gid)
	b := addNode(c, "入口 B")["id"].(string)
	saveMembers(c, gid, 200, a, b)
	port := h.ReserveTCPPort()
	c.Do("POST", "/api/rules", directBody(gid, port, "127.0.0.1", echoTarget(t, "A:")), 201)
	rev := awaitRevision(t, c, a, 0)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, a, 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	// 服务商仍指向 A，切换 B 的读回尚未完成。
	hold.Store(true)
	manualSwitch(c, gid, b, "1.1.1.1", 1, 202)
	waitSignal(t, entered)
	saveMembers(c, gid, 409, b)
	if fake.Record("example.com", "100").Value != "8.8.8.8" || nodeRow(t, c, a)["desired_revision"] != rev {
		t.Fatal("DNS 尚指向旧成员时已撤销其配置")
	}
	tcpExchange(t, port, "A:")
	once.Do(func() { close(release) })
	awaitDNSStage(t, c, gid, "服务商已写入")
	saveMembers(c, gid, 200, b)
}
