package clusterharness_test

import (
	"context"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

type cnameRig struct {
	h                *clusterharness.Harness
	c                *clusterharness.Client
	dns              *clusterharness.DNSPod
	account, gid     string
	first, secondary string
}

// newCNAMERig 准备两个上报不同公网地址的入口节点，组成主备入口组，首选入口在前。
func newCNAMERig(t *testing.T) *cnameRig {
	t.Helper()
	r := &cnameRig{dns: clusterharness.NewDNSPod()}
	r.h = clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = r.dns })
	r.c = r.h.Client()
	setup(r.c)
	login(r.c)
	r.account = r.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": "AKIDtest12345", "secret_key": "privateDnspodCredentialMarker"}, 201)["id"].(string)
	start := func(name, ip string) string {
		n := addNode(r.c, name)
		r.h.StartNode(n["installation_ticket"].(string), func(_ *node.Config, deps *node.Dependencies) {
			deps.PublicIPv4 = func(context.Context) (string, error) { return ip, nil }
		})
		awaitNode(t, r.c, n["id"].(string), func(row map[string]any) bool { return row["public_ipv4"] == ip })
		return n["id"].(string)
	}
	r.first, r.secondary = start("入口 A", "223.5.5.5"), start("入口 B", "223.6.6.6")
	r.gid = entryGroup(r.c, r.first)["id"].(string)
	saveMembers(r.c, r.gid, 200, r.first, r.secondary)
	return r
}

// publishNode 为节点按名字新建节点记录，确认地址后首次发布，直到记录已发布。
func (r *cnameRig) publishNode(t *testing.T, id, name, ip string) {
	t.Helper()
	base := "/api/nodes/" + id
	r.c.Do("PUT", base+"/managed-record", byName(r.account, name), 200)
	r.c.Do("POST", base+"/sync-address", map[string]any{"ipv4": ip, "confirm_inbound": true}, 200)
	deadline := time.Now().Add(6 * time.Second)
	for {
		pub := r.c.Do("GET", base+"/publication", nil, 200)
		a, _ := pub["action"].(map[string]any)
		if pub["published"] == true && a != nil && a["stage"] == panel.EntryWritten {
			return
		}
		if a == nil || a["stage"] == panel.EntryWritten {
			r.c.Do("POST", base+"/publish", map[string]any{"expected_version": pub["desired_version"], "confirm_inbound": true}, 202)
		}
		if time.Now().After(deadline) {
			t.Fatalf("节点记录未首次发布: %v", pub)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func cnameBinding(account, name string) map[string]any {
	b := byName(account, name)
	b["type"] = "CNAME"
	return b
}

// CNAME 组只指向成员自己的节点记录：绑定时新建暂停的 CNAME 指向首选入口，首次发布启用，切换只改目标域名；
// 期望解析结果仍是活动入口地址，活动入口的节点记录不能解绑，没有节点记录的成员不能成为目标。
func TestCNAMEGroupFollowsActiveNodeRecord(t *testing.T) {
	r := newCNAMERig(t)
	c := r.c
	c.Do("PUT", "/api/entry-groups/"+r.gid+"/managed-record", cnameBinding(r.account, "www"), 409)
	if r.dns.Writes() != 0 {
		t.Fatal("首选入口没有节点记录时仍写了服务商")
	}
	r.publishNode(t, r.first, "a", "223.5.5.5")
	b := c.Do("PUT", "/api/entry-groups/"+r.gid+"/managed-record", cnameBinding(r.account, "www"), 200)
	created := recordsNamed(r.dns, "www")
	if b["type"] != "CNAME" || b["created"] != true || len(created) != 1 || created[0].Type != "CNAME" || created[0].Value != "a.example.com" || created[0].Enabled {
		t.Fatalf("CNAME 新建不对: %v %+v", b, created)
	}
	c.Do("POST", "/api/entry-groups/"+r.gid+"/publish", map[string]any{"active_node_id": r.first, "confirm_inbound": true}, 202)
	awaitDNSStage(t, c, r.gid, panel.EntryWritten)
	www := r.dns.Record("example.com", created[0].ID)
	if pub := publication(c, r.gid); www.Value != "a.example.com" || !www.Enabled || www.TTL != 60 || pub["expected_value"] != "223.5.5.5" {
		t.Fatalf("首次发布未启用 CNAME 或期望不是活动入口地址: %+v %v", www, pub)
	}
	// 入口 B 没有节点记录，不能成为 CNAME 目标。
	manualSwitch(c, r.gid, r.secondary, "223.6.6.6", 1, 409)
	r.publishNode(t, r.secondary, "b", "223.6.6.6")
	manualSwitch(c, r.gid, r.secondary, "", 1, 202) // 留空时取节点记录地址。
	awaitDNSStage(t, c, r.gid, panel.EntryWritten)
	www = r.dns.Record("example.com", created[0].ID)
	if pub := publication(c, r.gid); www.Type != "CNAME" || www.Value != "b.example.com" || pub["expected_value"] != "223.6.6.6" {
		t.Fatalf("切换未改 CNAME 目标: %+v %v", www, pub)
	}
	// 地址与节点记录不一致时不能切过去。
	manualSwitch(c, r.gid, r.first, "1.1.1.1", 2, 409)
	c.Do("DELETE", "/api/nodes/"+r.secondary+"/managed-record", nil, 409)
	c.Do("PUT", "/api/nodes/"+r.secondary+"/managed-record", byName(r.account, "b2"), 409)
	c.Do("DELETE", "/api/nodes/"+r.first+"/managed-record", nil, 200)
	manualSwitch(c, r.gid, r.first, "223.5.5.5", 2, 409)
	if r.dns.Record("example.com", created[0].ID).Value != "b.example.com" {
		t.Fatal("被拒绝的切换改了记录")
	}
}

// 服务商侧 CNAME 目标被改动时，核对按活动入口的节点记录写回。
func TestCNAMEGroupAuditWritesBackTargetDrift(t *testing.T) {
	r := newCNAMERig(t)
	r.publishNode(t, r.first, "a", "223.5.5.5")
	b := r.c.Do("PUT", "/api/entry-groups/"+r.gid+"/managed-record", cnameBinding(r.account, "www"), 200)
	r.c.Do("POST", "/api/entry-groups/"+r.gid+"/publish", map[string]any{"active_node_id": r.first, "confirm_inbound": true}, 202)
	awaitDNSStage(t, r.c, r.gid, panel.EntryWritten)
	id := b["record_id"].(string)
	drifted := r.dns.Record("example.com", id)
	drifted.Value = "cdn.example.net"
	r.dns.Set("example.com", drifted)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && publication(r.c, r.gid)["desired_version"] != float64(2) {
		r.h.Clock.Advance(10 * time.Second)
		time.Sleep(10 * time.Millisecond)
	}
	pub := publication(r.c, r.gid)
	awaitDNSStage(t, r.c, r.gid, panel.EntryWritten)
	if pub["action"].(map[string]any)["kind"] != "writeback" || pub["expected_value"] != "223.5.5.5" || r.dns.Record("example.com", id).Value != "a.example.com" {
		t.Fatalf("CNAME 目标偏差未写回: %v %+v", pub, r.dns.Record("example.com", id))
	}
}

// 同名已有 A 记录时绑定 CNAME 须经确认改类型；CNAME 记录不能绑给节点或全部发布成员。
func TestCNAMEGroupConvertsExistingARecordAfterConfirmation(t *testing.T) {
	r := newCNAMERig(t)
	r.publishNode(t, r.first, "a", "223.5.5.5")
	r.dns.Set("example.com", dnsprovider.Record{ID: "300", Name: "www", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 600, Enabled: true})
	writes := r.dns.Writes()
	choice := r.c.Do("PUT", "/api/entry-groups/"+r.gid+"/managed-record", cnameBinding(r.account, "www"), 409)
	if len(choice["conflicts"].([]any)) != 1 || r.dns.Writes() != writes {
		t.Fatalf("同名 A 未要求确认: %v", choice)
	}
	confirm := cnameBinding(r.account, "www")
	confirm["convert_record_id"] = "300"
	b := r.c.Do("PUT", "/api/entry-groups/"+r.gid+"/managed-record", confirm, 200)
	rec := r.dns.Record("example.com", "300")
	if b["type"] != "CNAME" || rec.Type != "CNAME" || rec.Value != "a.example.com" || rec.Enabled {
		t.Fatalf("改类型不对: %v %+v", b, rec)
	}
	r.c.Do("PUT", "/api/nodes/"+r.secondary+"/managed-record", cnameBinding(r.account, "b"), 400)
}
