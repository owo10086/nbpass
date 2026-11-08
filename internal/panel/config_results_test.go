package panel

import (
	"context"
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 协议物理边界：错误快照、独立报告及迟到失败不能改写逐规则应用确认。
func TestPartialRuleReportIsolationAndLateFailure(t *testing.T) {
	p := openDiskPanel(t, t.TempDir())
	defer p.Close()
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	_, digest, err := p.randomCredential()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO nodes(id,name,token_hash,created_at,desired_revision) VALUES('n','入口',?,0,2)", digest); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO entry_groups(id,name) VALUES('g','入口组')"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO entry_group_members(group_id,node_id,position) VALUES('g','n',0)"); err != nil {
		t.Fatal(err)
	}
	snapshot := &controlpb.ConfigSnapshot{Revision: 2}
	for i, id := range []string{"good", "bad"} {
		rule := forwarding.Rule{ID: id, Name: id, EntryGroupID: "g", Kind: forwarding.Direct, ListenPort: 12340 + i, TargetAddress: "8.8.8.8", TargetFamily: forwarding.IPv4, TargetPort: 443, Enabled: true}
		if _, err := p.db.Exec(`INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled) VALUES(?,?,?,?,?,?,?,?,?,?)`, rule.ID, rule.Name, rule.EntryGroupID, rule.Kind, rule.ListenPort, rule.TargetAddress, rule.TargetFamily, rule.TargetPort, false, true); err != nil {
			t.Fatal(err)
		}
		snapshot.Rules = append(snapshot.Rules, rule.Config())
	}
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	data, err := proto.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT INTO node_snapshots(node_id,snapshot,digest) VALUES('n',?,?)", data, snapshot.Digest); err != nil {
		t.Fatal(err)
	}
	session := &controlSession{done: make(chan struct{}), instance: "current"}
	p.controls["n"] = session
	send := func(r *controlpb.NodeReport) {
		t.Helper()
		if _, err := p.acceptReport(context.Background(), "n", digest, session, r); err != nil {
			t.Fatal(err)
		}
	}
	send(&controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current"})
	partial := &controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current", AttemptedRevision: 2, AttemptedDigest: snapshot.Digest, ApplyError: "端口被占用", RevocationsApplied: true, RuleResults: []*controlpb.RuleApplyResult{{RuleId: "good", AppliedRevision: 2}, {RuleId: "bad", ApplyError: "端口 12341 被占用"}}}
	send(partial)
	assertStatuses := func(good, bad string) {
		t.Helper()
		response := diskRequest(t, p, "GET", "/api/rules", nil, cookie, 200)
		var body struct {
			Rules []ruleView `json:"rules"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		statuses := map[string]string{}
		for _, r := range body.Rules {
			statuses[r.ID] = r.SyncStatus
		}
		if statuses["good"] != good || statuses["bad"] != bad {
			t.Fatalf("逐规则状态被污染: %v", statuses)
		}
	}
	assertStatuses("正常", "同步失败")
	for _, kind := range []string{"old", "future", "digest", "probe", "events"} {
		report := proto.Clone(partial).(*controlpb.NodeReport)
		report.ApplyError = "错误覆盖"
		report.RuleResults[0].AppliedRevision, report.RuleResults[0].ApplyError = 0, "错误覆盖"
		report.RuleResults[1].AppliedRevision, report.RuleResults[1].ApplyError = 2, ""
		switch kind {
		case "old":
			report.AttemptedRevision = 1
		case "future":
			report.AttemptedRevision = 3
		case "digest":
			report.AttemptedDigest = "wrong"
		case "probe":
			report.ProbeOnly = true
		case "events":
			report.EventsOnly = true
		}
		send(report)
		assertStatuses("正常", "同步失败")
		nodeResponse := diskRequest(t, p, "GET", "/api/nodes", nil, cookie, 200)
		var nodeBody struct {
			Nodes []nodeView `json:"nodes"`
		}
		if err := json.Unmarshal(nodeResponse.Body.Bytes(), &nodeBody); err != nil {
			t.Fatal(err)
		}
		if nodeBody.Nodes[0].ApplyError != "端口被占用" {
			t.Fatalf("%s 报告污染节点汇总错误: %+v", kind, nodeBody.Nodes[0])
		}
	}
	send(&controlpb.NodeReport{ProtocolVersion: 1, ProcessInstance: "current", AppliedRevision: 2, AppliedDigest: snapshot.Digest})
	assertStatuses("正常", "正常")
	send(partial)
	assertStatuses("正常", "正常")
}
