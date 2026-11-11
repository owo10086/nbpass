package clusterharness_test

import (
	"context"
	"strings"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

// nameBrowser 同 dnsBrowser，但入口节点上报了公网地址，面板才能为新建的记录填占位值。
func nameBrowser(t *testing.T) (*clusterharness.Client, *clusterharness.DNSPod, string, string) {
	t.Helper()
	fake := clusterharness.NewDNSPod()
	fake.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = fake })
	c := h.Client()
	setup(c)
	login(c)
	a := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "privateDnspodCredentialMarker"}, 201)
	n := addNode(c, "活动入口")
	h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
		deps.PublicIPv4 = func(context.Context) (string, error) { return "223.5.5.5", nil }
	})
	awaitNode(t, c, n["id"].(string), func(row map[string]any) bool { return row["public_ipv4"] == "223.5.5.5" })
	return c, fake, a["id"].(string), entryGroup(c, n["id"].(string))["id"].(string)
}

func byName(account, name string) map[string]any {
	return map[string]any{"account_id": account, "domain": "example.com", "name": name, "type": "A"}
}

func recordsNamed(fake *clusterharness.DNSPod, name string) []dnsprovider.Record {
	records, _ := fake.FindRecords(context.Background(), dnsprovider.Credentials{}, "example.com", name)
	return records
}

// 名字不存在时面板新建一条暂停的默认线路记录，首次发布写值并明确启用。
func TestManagedRecordByNameCreatesPausedRecordThenFirstPublishEnables(t *testing.T) {
	c, fake, account, gid := nameBrowser(t)
	b := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", byName(account, "App.example.com."), 200)
	created := recordsNamed(fake, "app")
	if b["created"] != true || b["name"] != "app" || len(created) != 1 || created[0].Enabled || created[0].Type != "A" || created[0].LineID != "0" || b["record_id"] != created[0].ID {
		t.Fatalf("新建结果不对: %v %+v", b, created)
	}
	firstPublish(c, gid, memberOf(c, gid), 202)
	awaitDNSStage(t, c, gid, "服务商已写入")
	r := fake.Record("example.com", created[0].ID)
	if r.Value != "8.8.8.8" || !r.Enabled || r.TTL != 60 {
		t.Fatalf("首次发布未写值或未启用: %+v", r)
	}
	if fake.Record("example.com", "100").Value != "9.9.9.9" {
		t.Fatal("动了其他记录")
	}
}

// 唯一同名记录直接接管；多条时返回候选，由管理员指定或选择新建。
func TestManagedRecordByNameAdoptsOrAsksWhichOne(t *testing.T) {
	_, c, fake, account, gid := dnsBrowser(t)
	b := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", byName(account, "entry"), 200)
	if b["record_id"] != "100" || b["created"] != nil || fake.Writes() != 0 {
		t.Fatalf("未直接接管唯一记录: %v", b)
	}
	fake.Set("example.com", dnsprovider.Record{ID: "102", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "1.1.1.1", TTL: 600, Enabled: true})
	gid2 := entryGroup(c, addNode(c, "另一个入口")["id"].(string))["id"].(string)
	choice := c.Do("PUT", "/api/entry-groups/"+gid2+"/managed-record", byName(account, "entry"), 409)
	if len(choice["candidates"].([]any)) != 2 {
		t.Fatalf("多条同名记录未列出候选: %v", choice)
	}
	pick := byName(account, "entry")
	pick["record_id"] = "102"
	if b = c.Do("PUT", "/api/entry-groups/"+gid2+"/managed-record", pick, 200); b["record_id"] != "102" {
		t.Fatalf("未接管指定记录: %v", b)
	}
	pick["record_id"] = "999"
	c.Do("PUT", "/api/entry-groups/"+gid2+"/managed-record", pick, 400)
	if fake.Writes() != 0 {
		t.Fatal("接管时写了服务商")
	}
}

// 同名的 CNAME 必须经管理员确认才改成 A，记录 ID 不变且改后暂停；面板不删除任何记录。
func TestManagedRecordByNameConvertsConflictOnlyAfterConfirmation(t *testing.T) {
	c, fake, account, gid := nameBrowser(t)
	fake.Set("example.com", dnsprovider.Record{ID: "200", Name: "web", Type: "CNAME", Line: "默认", LineID: "0", Value: "cdn.example.net", TTL: 600, Enabled: true})
	choice := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", byName(account, "web"), 409)
	if len(choice["conflicts"].([]any)) != 1 || fake.Writes() != 0 {
		t.Fatalf("类型冲突未要求确认: %v", choice)
	}
	confirm := byName(account, "web")
	confirm["convert_record_id"] = "200"
	b := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", confirm, 200)
	r := fake.Record("example.com", "200")
	if b["record_id"] != "200" || r.Type != "A" || r.Enabled || !strings.Contains(strings.Join(anyStrings(b["warnings"]), ""), "cdn.example.net") {
		t.Fatalf("改类型接管不对: %v %+v", b, r)
	}
	if len(recordsNamed(fake, "web")) != 1 {
		t.Fatal("改类型时新建或删除了记录")
	}
}

// 新建回包丢失时按名字读回认领唯一新记录，不重复新建。
func TestManagedRecordByNameLostCreateResponseIsClaimedOnce(t *testing.T) {
	c, fake, account, gid := nameBrowser(t)
	fake.Create = func(context.Context) error { return context.DeadlineExceeded }
	b := c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", byName(account, "lost"), 200)
	records := recordsNamed(fake, "lost")
	if len(records) != 1 || b["record_id"] != records[0].ID || fake.Writes() != 1 {
		t.Fatalf("回包丢失后未认领或重复新建: %v %+v", b, records)
	}
}

// 入口节点同样可按名字新建；新建后须由管理员首次发布。
func TestNodeRecordByNameCreatesPausedRecord(t *testing.T) {
	c, fake, account, gid := nameBrowser(t)
	node := memberOf(c, gid)
	b := c.Do("PUT", "/api/nodes/"+node+"/managed-record", byName(account, "hk1"), 200)
	records := recordsNamed(fake, "hk1")
	if b["created"] != true || len(records) != 1 || records[0].Enabled {
		t.Fatalf("节点记录新建不对: %v %+v", b, records)
	}
	cname := byName(account, "hk2")
	cname["type"] = "CNAME"
	c.Do("PUT", "/api/nodes/"+node+"/managed-record", cname, 400)
}

// 节点还没有公网地址时不新建记录，也不碰服务商。
func TestManagedRecordByNameRefusesCreateWithoutNodeAddress(t *testing.T) {
	_, c, fake, account, gid := dnsBrowser(t)
	c.Do("PUT", "/api/entry-groups/"+gid+"/managed-record", byName(account, "app"), 409)
	if fake.Writes() != 0 || len(recordsNamed(fake, "app")) != 0 {
		t.Fatal("没有地址时仍新建了记录")
	}
}

// 全部发布成员按组内已有的名字绑定：没有就新建，其他成员已占用的同名记录不会被抢。
func TestAllPublishMemberBindsByName(t *testing.T) {
	c, fake, account, legacy := nameBrowser(t)
	n1, n2 := memberOf(c, legacy), addNode(c, "成员二")["id"].(string)
	g := allGroup(c, n1, n2)
	b := c.Do("PUT", memberBindingPath(g, n1), byName(account, "entry"), 200)
	if b["record_id"] != "100" || fake.Writes() != 0 {
		t.Fatalf("未接管唯一同名记录：%v", b)
	}
	// 唯一的同名记录已归成员一，成员二不能再接管。
	c.Do("PUT", memberBindingPath(g, n2), byName(account, "entry"), 409)
	other := byName(account, "other")
	c.Do("PUT", memberBindingPath(g, n2), other, 400)
	g2 := allGroup(c, n1)
	b = c.Do("PUT", memberBindingPath(g2, n1), byName(account, "pool"), 200)
	if r := fake.Record("example.com", b["record_id"].(string)); b["created"] != true || r.Enabled || r.Value != "223.5.5.5" {
		t.Fatalf("成员记录新建不对：%v %+v", b, r)
	}
}
