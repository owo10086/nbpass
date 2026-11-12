package clusterharness_test

import (
	"fmt"
	"nbpass/internal/dnsprovider"
	"nbpass/internal/panel"
	"testing"
)

func (r *chainRig) publish(t *testing.T, mode string) {
	t.Helper()
	account := r.c.Do("POST", "/api/dns-accounts", map[string]string{"name": "chain DNS", "secret_id": "AKIDtest12345", "secret_key": "testSecretKey"}, 201)["id"].(string)
	for i := 0; i < r.entries; i++ {
		r.dns.Set("example.com", dnsprovider.Record{ID: fmt.Sprint(100 + i), Name: "entry", Type: "A", Line: "默认", LineID: "0", Value: "9.9.9.9", TTL: 60, Enabled: false})
		if mode == "all" {
			bindMember(r.c, r.group, r.ids[i], account, fmt.Sprint(100+i), 200)
		}
		path := "/api/nodes/" + r.ids[i] + "/ip-rotation"
		r.c.Do("PUT", path, map[string]any{"url": "https://vendor.example/change", "method": "POST"}, 200)
		r.c.Do("PUT", path+"/automatic", map[string]bool{"enabled": true}, 200)
	}
	if mode == "all" {
		publishAll(r.c, r.group, map[string]string{r.ids[0]: "8.8.8.8", r.ids[1]: "1.1.1.1"}, 202)
	} else {
		r.c.Do("PUT", "/api/entry-groups/"+r.group+"/managed-record", binding(account), 200)
		firstPublish(r.c, r.group, r.ids[0], 202)
	}
	awaitDNSStage(t, r.c, r.group, panel.EntryWritten)
}
func (r *chainRig) noRotation(t *testing.T) {
	t.Helper()
	if r.rotationCalls.Load() != 0 {
		t.Fatal("private chain failure caused public IP rotation")
	}
	for _, id := range r.ids {
		if v := r.c.Do("GET", "/api/nodes/"+id+"/ip-rotation", nil, 200); v["action"] != nil {
			t.Fatal("unrelated rotation action", v)
		}
	}
}
func TestChainEntryGroupPrivateFailurePublicationMatrix(t *testing.T) {
	for _, mode := range []string{"primary_backup", "all"} {
		t.Run(mode, func(t *testing.T) {
			r := newChainRig(t, 2, []int{1, 1}, mode)
			r.ready(t, true, true)
			r.publish(t, mode)
			initial := r.dns.Writes()
			r.links[0][2].block(true)
			r.ready(t, false, true)
			assertTCPFailure(t, int(r.entryPorts[0].Load()))
			r.exchange(t, 1)
			r.advance(t, 20)
			if r.dns.Writes() != initial {
				t.Fatal("readiness alone triggered external action")
			}
			r.noRotation(t)
			if mode == "primary_backup" {
				action := manualSwitch(r.c, r.group, r.ids[1], "1.1.1.1", 1, 202)
				manualSwitch(r.c, r.group, r.ids[1], "1.1.1.1", 1, 409)
				awaitDNSStage(t, r.c, r.group, panel.EntryWritten)
				pub := publication(r.c, r.group)
				if pub["active_node_id"] != r.ids[1] || pub["desired_version"] != float64(2) || r.dns.Record("example.com", "100").Value != "1.1.1.1" || r.dns.Writes() != initial+1 || action["id"] == "" {
					t.Fatal("coordinator did not commit exactly one switch", pub, action)
				}
			} else {
				for i, id := range r.ids[:2] {
					m := allPolicyMember(t, r.c, r.group, id)
					if m["expected_enabled"] != true || m["record"].(map[string]any)["current_enabled"] != true || !r.dns.Record("example.com", fmt.Sprint(100+i)).Enabled {
						t.Fatal("unready member was withdrawn", m)
					}
				}
			}
			writes := r.dns.Writes()
			version := publication(r.c, r.group)["desired_version"]
			r.links[2][3].block(true)
			r.ready(t, false, false)
			r.advance(t, 20)
			for _, id := range r.ids[:2] {
				if entryState(t, r.c, r.group, id)["candidate"] != false {
					t.Fatal("broken suffix retained candidate")
				}
			}
			if r.dns.Writes() != writes || publication(r.c, r.group)["desired_version"] != version {
				t.Fatal("no candidate changed last publication")
			}
			r.noRotation(t)
			r.links[0][2].recover()
			r.links[2][3].recover()
			r.ready(t, true, true)
			r.exchange(t, 0)
			r.exchange(t, 1)
			r.nodes[2].Disconnect()
			awaitNode(t, r.c, r.ids[2], func(v map[string]any) bool { return v["connected"] == false })
			r.ready(t, false, false)
			r.exchange(t, 0)
			r.exchange(t, 1)
			r.advance(t, 20)
			if r.dns.Writes() != writes {
				t.Fatal("relay control loss changed DNS despite cached working business")
			}
			r.noRotation(t)
		})
	}
}

func TestChainEntryGroupOfflineAutomaticCandidateAndMinimumKept(t *testing.T) {
	for _, mode := range []string{"primary_backup", "all"} {
		for _, suffixReady := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/suffix_%t", mode, suffixReady), func(t *testing.T) {
				r := newChainRig(t, 2, []int{1, 1}, mode)
				r.ready(t, true, true)
				r.publish(t, mode)
				startChecker(t, r.h, r.c, 0, 1, nil, allOfflineChecker(nil))
				if !suffixReady {
					r.links[2][3].block(true)
					r.ready(t, false, false)
				}
				before := r.dns.Writes()
				r.nodes[0].Disconnect()
				awaitNode(t, r.c, r.ids[0], func(v map[string]any) bool { return v["connected"] == false })
				r.advance(t, 105)
				if mode == "primary_backup" {
					f := &offlineEntryRig{h: r.h, c: r.c, group: r.group}
					if suffixReady {
						awaitOfflineCheck(t, f, "submitted")
						awaitDNSStage(t, r.c, r.group, panel.EntryWritten)
						if publication(r.c, r.group)["active_node_id"] != r.ids[1] || r.dns.Record("example.com", "100").Value != "1.1.1.1" || r.dns.Writes() != before+1 {
							t.Fatal("automatic failover did not choose current ready entry")
						}
					} else {
						awaitOfflineCheck(t, f, "no_candidate")
						if r.dns.Writes() != before || r.dns.Record("example.com", "100").Value != "8.8.8.8" {
							t.Fatal("no candidate failed to preserve last DNS")
						}
					}
				} else {
					f := &allPolicyRig{h: r.h, c: r.c, dns: r.dns, g: r.group, ids: r.ids}
					// Await the real checker decision and coordinator write; advancing
					// the policy deadline alone does not finish asynchronous DNS work.
					f.offlineCheck(t, 0, "unreachable")
					r.advance(t, 1)
					f.awaitEnabled(t, 0, false)
					if r.dns.Writes() != before+1 {
						t.Fatal("offline withdrawal did not write exactly once")
					}
					if m := allPolicyMember(t, r.c, r.group, r.ids[0]); m["expected_enabled"] != false || m["record"].(map[string]any)["current_enabled"] != false {
						t.Fatal("confirmed offline member not withdrawn", m)
					}
					r.nodes[1].Disconnect()
					awaitNode(t, r.c, r.ids[1], func(v map[string]any) bool { return v["connected"] == false })
					r.advance(t, 105)
					f.offlineCheck(t, 1, "unreachable")
					r.advance(t, 1)
					waitOfflineEvents(t, r.c, "?type=entry_member_kept", 1)
					f.awaitEnabled(t, 1, true)
					if m := allPolicyMember(t, r.c, r.group, r.ids[1]); m["expected_enabled"] != true || !r.dns.Record("example.com", "101").Enabled {
						t.Fatal("minimum kept barrier removed last published member", m)
					}
				}
				writes := r.dns.Writes()
				r.advance(t, 15)
				if r.dns.Writes() != writes {
					t.Fatal("repeated evidence duplicated external action")
				}
				r.noRotation(t)
			})
		}
	}
}
