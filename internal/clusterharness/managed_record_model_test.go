package clusterharness_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"nbpass/internal/clusterharness"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/node"
	"nbpass/internal/panel"
)

func TestManagedDNSRebindAfterRestoreCanBeReconciledAndPublished(t *testing.T) {
	dns := clusterharness.NewDNSPod()
	dns.Set("example.com", dnsprovider.Record{ID: "100", Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "8.8.8.8", TTL: 600, Enabled: true})
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.DNSProvider = dns })
	c := h.Client()
	setup(c)
	login(c)
	created := addNode(c, "入口")
	id := created["id"].(string)
	n := h.StartNode(created["installation_ticket"].(string), func(_ *node.Config, d *node.Dependencies) {
		d.PublicIPv4 = func(context.Context) (string, error) { return "8.8.8.8", nil }
	})
	awaitNode(t, c, id, connected)
	group := entryGroup(c, id)["id"].(string)
	account := c.Do("POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "token_id": "123", "token": "secret"}, 201)["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+group+"/managed-record", binding(account), 200)
	backup := filepath.Join(h.Dir, "before-publication.db")
	if err := h.Snapshot(backup); err != nil {
		t.Fatal(err)
	}
	n.Disconnect()
	if err := h.RestoreSnapshot(backup); err != nil {
		t.Fatal(err)
	}
	c = h.Client()
	login(c)
	n.Restore()
	n.Restart()
	awaitRevision(t, c, id, 0)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	replacement := dns.Record("example.com", "100")
	replacement.ID = "200"
	replacement.Name = "replacement"
	dns.Set("example.com", replacement)
	b := binding(account)
	b["record_id"] = "200"
	c.Do("PUT", "/api/entry-groups/"+group+"/managed-record", b, 200)
	c.Do("POST", "/api/recovery/reconcile", map[string]bool{"confirm": true}, 200)
	firstPublish(c, group, id, 202)
	awaitDNSStage(t, c, group, panel.EntryWritten)
	if dns.Writes() != 1 || dns.Record("example.com", "100").TTL != 600 || dns.Record("example.com", "200").TTL != 60 {
		t.Fatal("换绑改了旧记录或未完成新记录发布", dns.Calls())
	}
}

func TestManagedDNSRejectedSwitchKeepsActualEntryMemberProtected(t *testing.T) {
	_, c, dns, account, group := dnsBrowser(t)
	a := memberOf(c, group)
	b := addNode(c, "备用")["id"].(string)
	c.Do("PUT", "/api/entry-groups/"+group, map[string]any{"name": "主备组", "members": []string{a, b}}, 200)
	c.Do("PUT", "/api/entry-groups/"+group+"/managed-record", binding(account), 200)
	firstPublish(c, group, a, 202)
	awaitDNSStage(t, c, group, panel.EntryWritten)
	dns.ModifyError = "500025"
	manualSwitch(c, group, b, "1.1.1.1", 1, 202)
	awaitDNSStage(t, c, group, panel.EntryFailed)
	c.Do("PUT", "/api/entry-groups/"+group, map[string]any{"name": "不能撤掉实际入口", "members": []string{b}}, 409)
	if dns.Record("example.com", "100").Value != "8.8.8.8" || dns.Writes() != 2 {
		t.Fatal(dns.Calls())
	}
}

func TestManagedDNSAuditRejectsPausedPrimaryStandbyRecord(t *testing.T) {
	f := entryWatchSetup(t, false, nil)
	firstPublish(f.c, f.group, f.a, 202)
	awaitDNSStage(t, f.c, f.group, panel.EntryWritten)
	r := f.dns.Record("example.com", "100")
	r.Enabled = false
	f.dns.Set("example.com", r)
	f.advance(t, 30)
	row := watchResult(t, f.c, entryWatch(f.c, f.group)["id"].(string), func(v map[string]any) bool { return strings.Contains(v["provider_error"].(string), "启用") })
	if row["provider_error"] == "" || f.dns.Writes() != 1 || publication(f.c, f.group)["desired_version"] != float64(1) {
		t.Fatal(row, f.dns.Calls())
	}
}
