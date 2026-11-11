package clusterharness_test

import (
	"context"
	"encoding/json"
	"errors"
	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/panel"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDNSAccountCredentialsAreNeverReturned(t *testing.T) {
	h, c := nodeBrowser(t)
	h.Client().Do("GET", "/api/dns-accounts", nil, 401)
	body := map[string]string{"name": "DNSPod 主账号", "secret_id": "AKIDtest12345", "secret_key": "privateDnspodCredentialMarker"}
	account := c.Do("POST", "/api/dns-accounts", body, 201)
	if account["id"] == "" || account["configured"] != true {
		t.Fatalf("账号未保存: %v", account)
	}
	for range 2 {
		data, _ := json.Marshal(c.Do("GET", "/api/dns-accounts", nil, 200))
		if strings.Contains(string(data), body["secret_key"]) || strings.Contains(string(data), body["secret_id"]) {
			t.Fatalf("账号凭据回显: %s", data)
		}
		if err := h.Restart(); err != nil {
			t.Fatal(err)
		}
	}
	c.Do("POST", "/api/dns-accounts", map[string]string{"name": "缺少凭据"}, 400)
	c.Do("POST", "/api/dns-accounts", map[string]string{"name": "旧版 Token", "token_id": "12345", "token": "testSecretKey"}, 400)
	c.Do("POST", "/api/dns-accounts", map[string]string{"name": "含分隔符", "secret_id": "AKIDtest12345", "secret_key": "test,SecretKey"}, 400)
}

func dnsBrowser(t *testing.T) (*clusterharness.Harness, *clusterharness.Client, *clusterharness.DNSPod, string, string) {
	t.Helper()
	fake := clusterharness.NewDNSPod()
	fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = fake })
	c := h.Client()
	setup(c)
	login(c)
	a := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "privateDnspodCredentialMarker"}, 201)
	n := addNode(c, "活动入口")
	h.StartNode(n["installation_ticket"].(string))
	awaitNode(t, c, n["id"].(string), connected)
	gid := entryGroup(c, n["id"].(string))["id"].(string)
	return h, c, fake, a["id"].(string), gid
}
func binding(account string) map[string]string {
	return map[string]string{"account_id": account, "domain": "example.com", "record_id": "100", "line_id": "0"}
}
func TestManagedDNSBindingReadsValueWarnsAndOwnsOnlyOneRecord(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	other := dnsprovider.Record{ID: "101", Name: "entry", Type: "AAAA", Line: "默认", LineID: "0", Value: "2001:db8::1", TTL: 300, Enabled: true}
	fake.Set("example.com", other)
	b := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	if b["current_value"] != "9.9.9.9" || len(b["warnings"].([]any)) != 1 {
		t.Fatalf("绑定未读回或未提示: %v", b)
	}
	if fake.Writes() != 0 {
		t.Fatal("绑定时擅自发布")
	}
	gid2 := entryGroup(c, addNode(c, "另一个入口")["id"].(string))["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid2+"/managed-record", binding(account), 409)
	// 换一个本地账号别名也不能抢同一条服务商记录。
	alias := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "相同凭据的别名", "secret_id": "AKIDtest12345", "secret_key": "privateDnspodCredentialMarker"}, 201)
	aliasBinding := binding(alias["id"].(string))
	aliasBinding["domain"] = "EXAMPLE.COM."
	c.Do("PUT", "/api/entry-groups/"+gid2+"/managed-record", aliasBinding, 409)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	b = c.Do("GET", "/api/entry-groups/"+gid+"/managed-record", nil, 200)
	if b["current_value"] != "9.9.9.9" || fake.Record("example.com", "101") != other {
		t.Fatalf("重启后绑定或其他记录变化: %v", b)
	}
}
func TestManagedDNSRejectsUnsupportedRecords(t *testing.T) {
	_, c, fake, account, gid := dnsBrowser(t)
	original := fake.Record("example.com", "100")
	for _, kind := range []string{"CNAME", "AAAA"} {
		r := original
		r.Type = kind
		fake.Set("example.com", r)
		c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 400)
	}
	fake.Set("example.com", original)
	another := original
	another.ID = "102"
	another.Line = "电信"
	another.LineID = "10=0"
	fake.Set("example.com", another)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 400)
	if fake.Writes() != 0 {
		t.Fatal("非法绑定修改了 DNS")
	}
}

func firstPublish(c *clusterharness.Client, gid, nodeID string, status int) map[string]any {
	return c.Do("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": nodeID, "ipv4": "8.8.8.8", "confirm_inbound": true}, status)
}
func memberOf(c *clusterharness.Client, gid string) string {
	for _, row := range c.Do("GET", "/api/entry-groups", nil, 200)["entry_groups"].([]any) {
		g := row.(map[string]any)
		if g["id"] == gid {
			return g["members"].([]any)[0].(string)
		}
	}
	panic("入口组不存在")
}
func publication(c *clusterharness.Client, gid string) map[string]any {
	return c.Do("GET", "/api/entry-groups/"+gid+"/publication", nil, 200)
}
func awaitDNSStage(t *testing.T, c *clusterharness.Client, gid, stage string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		pub := publication(c, gid)
		if a, ok := pub["action"].(map[string]any); ok && a["stage"] == stage {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("未达到 %s: %v", stage, pub)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestManagedDNSFirstPublishChangesOnlyManagedRecord(t *testing.T) {
	_, c, fake, account, gid := dnsBrowser(t)
	other := dnsprovider.Record{ID: "101", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "1.1.1.1", TTL: 300, Enabled: true}
	fake.Set("example.com", other)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	id := memberOf(c, gid)
	c.Do("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": id, "ipv4": "8.8.8.8"}, 400)
	c.Do("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": "missing", "ipv4": "8.8.8.8", "confirm_inbound": true}, 400)
	c.Do("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": id, "ipv4": "127.0.0.1", "confirm_inbound": true}, 400)
	first := firstPublish(c, gid, id, 202)
	action := awaitDNSStage(t, c, gid, "服务商已写入")
	if action["id"] != first["id"] || action["old_value"] != "9.9.9.9" || action["new_value"] != "8.8.8.8" || action["old_ttl"] != float64(600) || action["version"] != float64(1) {
		t.Fatalf("动作未持久化: %v", action)
	}
	r := fake.Record("example.com", "100")
	if r.Value != "8.8.8.8" || r.TTL != 60 || fake.Record("example.com", "101") != other || fake.Writes() != 1 {
		t.Fatalf("发布范围或 TTL 错误: %v", r)
	}
	firstPublish(c, gid, id, 409)
	if publication(c, gid)["active_node_id"] != id {
		t.Fatal("活动入口未保存")
	}
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 409)
}
func TestManagedDNSLostResponseReconcilesWithoutDuplicateWrite(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	fake.Write = func(context.Context) error { return context.DeadlineExceeded }
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, memberOf(c, gid), 202)
	awaitDNSStage(t, c, gid, "结果未确认")
	h.Clock.Advance(5 * time.Second)
	awaitDNSStage(t, c, gid, "服务商已写入")
	if fake.Writes() != 1 {
		t.Fatalf("响应丢失后重复写: %d", fake.Writes())
	}
	history := c.Do("GET", "/api/entry-groups/"+gid+"/actions", nil, 200)["actions"].([]any)
	if len(history) != 1 {
		t.Fatalf("恢复创建了新动作: %v", history)
	}
	events, _ := json.Marshal(c.Do("GET", "/api/events", nil, 200))
	if !strings.Contains(string(events), "结果未确认") || strings.Contains(string(events), "private-dnspod") {
		t.Fatalf("事件缺少阶段或泄露凭据: %s", events)
	}
}
func TestManagedDNSRestartDuringWriteReadsBeforeContinuing(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	entered := make(chan struct{})
	fake.Write = func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, memberOf(c, gid), 202)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("未开始写入")
	}
	// 请求已提交，关闭核心使在途调用取消；服务商实际上已写入。
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitDNSStage(t, c, gid, "服务商已写入")
	if fake.Writes() != 1 {
		t.Fatalf("重建核心后重复写入: %v", fake.Calls())
	}
	calls := fake.Calls()
	if calls[len(calls)-1] != "read" {
		t.Fatalf("重启没有读回核对: %v", calls)
	}
}

func TestManagedDNSPersistenceFailureNeverStartsExternalWrite(t *testing.T) {
	for _, operation := range []string{"INSERT", "UPDATE"} {
		t.Run(operation, func(t *testing.T) {
			fake := clusterharness.NewDNSPod()
			fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
			faults := &clusterharness.PersistenceFaults{}
			h := clusterharness.New(t, func(d *panel.Dependencies) {
				faults.Base = d.Persistence
				d.Persistence = faults
				d.DNSProvider = fake
				d.Passwords = nodePasswords{}
			})
			c := h.Client()
			setup(c)
			login(c)
			account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "testSecretKey"}, 201)["id"].(string)
			id := addNode(c, "入口")["id"].(string)
			gid := entryGroup(c, id)["id"].(string)
			c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
			if err := faults.FailEntryActions(operation); err != nil {
				t.Fatal(err)
			}
			status := 202
			if operation == "INSERT" {
				status = 503
			}
			firstPublish(c, gid, id, status)
			for range 3 {
				h.Clock.Advance(5 * time.Second)
				time.Sleep(10 * time.Millisecond)
			}
			if fake.Writes() != 0 || fake.Record("example.com", "100").Value != "9.9.9.9" {
				t.Fatal("动作持久化失败仍产生外部副作用")
			}
			if err := faults.FailEntryActions(""); err != nil {
				t.Fatal(err)
			}
			if operation == "INSERT" {
				if publication(c, gid)["desired_version"] != float64(0) {
					t.Fatal("失败事务推进了期望版本")
				}
				firstPublish(c, gid, id, 202)
			} else {
				h.Clock.Advance(5 * time.Second)
			}
			awaitDNSStage(t, c, gid, "服务商已写入")
		})
	}
}
func TestManagedDNSRestartAfterCompletionPersistenceFailure(t *testing.T) {
	fake := clusterharness.NewDNSPod()
	fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	faults := &clusterharness.PersistenceFaults{}
	h := clusterharness.New(t, func(d *panel.Dependencies) {
		faults.Base = d.Persistence
		d.Persistence = faults
		d.DNSProvider = fake
		d.Passwords = nodePasswords{}
	})
	c := h.Client()
	setup(c)
	login(c)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "testSecretKey"}, 201)["id"].(string)
	id := addNode(c, "入口")["id"].(string)
	gid := entryGroup(c, id)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	injected := make(chan error, 1)
	fake.Write = func(context.Context) error { err := faults.FailEntryActions("UPDATE"); injected <- err; return err }
	firstPublish(c, gid, id, 202)
	select {
	case err := <-injected:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未到达服务商写入")
	}
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	awaitDNSStage(t, c, gid, "服务商已写入")
	if fake.Writes() != 1 {
		t.Fatal("完成阶段持久化失败后重启重复写入")
	}
}

func TestManagedDNSPendingActionProtectsBindingAndMembership(t *testing.T) {
	_, c, fake, account, gid := dnsBrowser(t)
	entered := make(chan struct{})
	fake.Write = func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, memberOf(c, gid), 202)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("写入未开始")
	}
	c.Do("DELETE", "/api/entry-groups/"+gid, nil, 409)
	replacement := fake.Record("example.com", "100")
	replacement.ID = "200"
	replacement.Name = "replacement"
	fake.Set("example.com", replacement)
	change := binding(account)
	change["record_id"] = "200"
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", change, 409)
	if b := c.Do("GET", "/api/entry-groups/"+gid+"/managed-record", nil, 200); b["record_id"] != "100" || fake.Writes() != 1 {
		t.Fatal("动作在途时换绑或重发了记录", b, fake.Calls())
	}
	other := addNode(c, "其他成员")["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+gid, map[string]any{"name": "入口组", "members": []string{other}}, 409)
}

func TestManagedDNSWriteTimeoutUsesInjectedClockAndHidesProviderErrors(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	entered := make(chan struct{})
	failRead := atomic.Bool{}
	fake.Read = func(context.Context) error {
		if failRead.Load() {
			return errors.New("private-dnspod-credential-marker")
		}
		return nil
	}
	fake.Write = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return errors.New("Authorization: private-dnspod-credential-marker")
	}
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	firstPublish(c, gid, memberOf(c, gid), 202)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("未开始写入")
	}
	h.Clock.Advance(10 * time.Second)
	awaitDNSStage(t, c, gid, "结果未确认")
	// 未确认时读取也失败，不能重新写入；第三方原始错误不得进入状态或事件。
	failRead.Store(true)
	for range 2 {
		h.Clock.Advance(5 * time.Second)
		time.Sleep(15 * time.Millisecond)
	}
	for _, path := range []string{"/api/entry-groups/" + gid + "/publication", "/api/entry-groups/" + gid + "/actions", "/api/events", "/api/dns-accounts"} {
		body, _ := json.Marshal(c.Do("GET", path, nil, 200))
		if strings.Contains(string(body), "private-dnspod-credential-marker") {
			t.Fatalf("第三方凭据泄露: %s", path)
		}
	}
	if fake.Writes() != 1 {
		t.Fatal("读回失败时重复写入")
	}
	failRead.Store(false)
	h.Clock.Advance(5 * time.Second)
	awaitDNSStage(t, c, gid, "服务商已写入")
}

func TestManagedDNSFirstPublishIsExclusiveAndGroupsRunIndependently(t *testing.T) {
	_, c, fake, account, gid := dnsBrowser(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var writes atomic.Int32
	fake.Write = func(ctx context.Context) error {
		if writes.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	id := memberOf(c, gid)
	statuses := make(chan int, 6)
	for range 6 {
		go func() {
			resp := c.Request("POST", "/api/entry-groups/"+gid+"/publish", map[string]any{"active_node_id": id, "ipv4": "8.8.8.8", "confirm_inbound": true}, nil)
			resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	successes := 0
	for range 6 {
		status := <-statuses
		if status == 202 {
			successes++
		} else if status != 409 {
			t.Fatalf("并发首次发布意外响应: %d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("首次发布被接收 %d 次", successes)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("未开始写入")
	}
	second := fake.Record("example.com", "100")
	second.ID = "200"
	second.Name = "other"
	second.Value = "9.9.9.9"
	second.TTL = 600
	fake.Set("example.com", second)
	id2 := addNode(c, "另组入口")["id"].(string)
	gid2 := entryGroup(c, id2)["id"].(string)
	b := binding(account)
	b["record_id"] = "200"
	c.Do("PUT", "/api/entry-groups/"+gid2+"/managed-record", b, 200)
	firstPublish(c, gid2, id2, 202)
	awaitDNSStage(t, c, gid2, "服务商已写入")
	if publication(c, gid)["action"].(map[string]any)["stage"] != "期望已变更" {
		t.Fatal("阻塞中的调用被提前完成")
	}
	once.Do(func() { close(release) })
	awaitDNSStage(t, c, gid, "服务商已写入")
	if fake.Writes() != 2 {
		t.Fatalf("并发动作重复执行: %v", fake.Calls())
	}
}
func TestManagedDNSProviderRecordChangesPausePublication(t *testing.T) {
	h, c, fake, account, gid := dnsBrowser(t)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", binding(account), 200)
	original := fake.Record("example.com", "100")
	changed := original
	changed.Type = "CNAME"
	changed.Value = "other.example.com"
	fake.Set("example.com", changed)
	firstPublish(c, gid, memberOf(c, gid), 202)
	deadline := time.Now().Add(5 * time.Second)
	for publication(c, gid)["action"].(map[string]any)["last_error"] == "" {
		if time.Now().After(deadline) {
			t.Fatal("服务商记录变化未显示核对错误")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if fake.Writes() != 0 {
		t.Fatal("覆盖了绑定后被修改类型的记录")
	}
	fake.Set("example.com", original)
	h.Clock.Advance(5 * time.Second)
	awaitDNSStage(t, c, gid, "服务商已写入")
	id := memberOf(c, gid)
	result := c.Do("PUT", "/api/entry-groups/"+gid, map[string]any{"name": "改名后的入口组", "members": []string{id}}, 200)
	if result["publication_status"] != "服务商已写入" {
		t.Fatalf("编辑入口组错误清空发布状态: %v", result)
	}
}
