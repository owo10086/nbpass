package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

func TestIXMember02PublicIPv4RebindsEvidenceWithoutSendingIdentity(t *testing.T) {
	p, entry, exit, _, _, _ := exitAddressProtocolFixture(t)
	ctx := context.Background()
	c := nodeCompatibility{ProcessInstance: "current", ControlProtocol: 2, Node: compatibility.Current(), Capabilities: []string{compatibility.IXAddressDialing}}
	c.Node.Capabilities = []string{compatibility.IXAddressDialing}
	if err := p.commitForwarding(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE nodes SET process_instance='current' WHERE id=?", entry); err != nil {
			return err
		}
		return p.saveNodeCompatibility(ctx, tx, entry, c)
	}); err != nil {
		t.Fatal(err)
	}
	m, err := p.controlMessage(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	ep := m.Snapshot.ExitGroups[0].Exits[0]
	if ep.AddressId != "" || ep.EndpointConfigDigest == "" {
		t.Fatal("02公网应省略280但回显02形状摘要", ep.AddressId, ep.EndpointConfigDigest)
	}
	old := &controlpb.ExitHealth{ExitId: exit, LastProbe: "success", State: "healthy", EndpointConfigDigest: ep.EndpointConfigDigest, CheckedAtUnix: p.deps.Clock.Now().Unix()}
	for _, edit := range []string{"address", "port"} {
		if err = p.commitForwarding(ctx, func(tx *sql.Tx) error {
			if edit == "address" {
				_, e := tx.Exec("UPDATE tunnel_exits SET address='9.9.9.9' WHERE node_id=?", exit)
				return e
			}
			_, e := tx.Exec("UPDATE tunnel_exits SET listen_port=listen_port+1 WHERE node_id=?", exit)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		m, err = p.controlMessage(ctx, entry)
		if err != nil {
			t.Fatal(err)
		}
		if matchesEndpointHealth(m.Snapshot, old) {
			t.Fatal("02旧IPv4成功冒认新配置", edit)
		}
		ep = m.Snapshot.ExitGroups[0].Exits[0]
		if ep.AddressId != "" || ep.EndpointConfigDigest == old.EndpointConfigDigest {
			t.Fatal("改变02身份语义或未推进摘要", edit)
		}
		old.EndpointConfigDigest = ep.EndpointConfigDigest
	}
}

func TestIXMemberHealthAPINormalizesLegacyPublicIdentity(t *testing.T) {
	p, entry, exit, _, _, _ := exitAddressProtocolFixture(t)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	raw, err := json.Marshal([]*controlpb.ExitHealth{{ExitId: exit, State: "healthy", LastProbe: "success"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.db.Exec("INSERT OR REPLACE INTO probe_realtime(node_id,process_instance,sequence,received_at,values_json,exit_health_json) VALUES(?,'current',1,?,'{}',?)", entry, p.deps.Clock.Now().UnixNano(), raw); err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/nodes/"+entry+"/exit-health", nil, cookie, 200).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	exits := view["exits"].([]any)
	if len(exits) != 1 || exits[0].(map[string]any)["address_id"] != "public" {
		t.Fatal("健康API没有稳定公网标识", view)
	}
}

func TestIXMemberReadinessHealthAndPublicConfirmationNeverBorrowOtherAddress(t *testing.T) {
	p, entry, exit, _, _, snapshot := exitAddressProtocolFixture(t)
	ctx := context.Background()
	if len(snapshot.Rules) != 1 {
		t.Fatal(snapshot)
	}
	snapshot = proto.Clone(snapshot).(*controlpb.ConfigSnapshot)
	public := snapshot.ExitGroups[0].Exits[0]
	public.AddressId = "public"
	public.EndpointConfigDigest = forwarding.EndpointConfigDigest(public)
	ix := proto.Clone(public).(*controlpb.TunnelEndpoint)
	ix.AddressId, ix.Address, ix.AddressVersion = "ix", "ix.test", ""
	ix.EndpointConfigDigest = forwarding.EndpointConfigDigest(ix)
	snapshot.ExitGroups = append(snapshot.ExitGroups, &controlpb.ExitGroup{Id: "ix", Strategy: snapshot.ExitGroups[0].Strategy, Exits: []*controlpb.TunnelEndpoint{ix}})
	r := proto.Clone(snapshot.Rules[0]).(*controlpb.ForwardRule)
	r.Id, r.ExitGroupId = "ix-rule", "ix"
	r.AuthorizedExitIds = []string{exit}
	snapshot.Rules = append(snapshot.Rules, r)
	writeSnapshot := func() {
		t.Helper()
		data, err := proto.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.db.Exec("UPDATE node_snapshots SET snapshot=?,encrypted=0 WHERE node_id=?", data, entry); err != nil {
			t.Fatal(err)
		}
	}
	writeSnapshot()
	now := p.deps.Clock.Now()
	if _, err := p.db.Exec("UPDATE nodes SET process_instance='current' WHERE id=?", entry); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec("INSERT OR REPLACE INTO probe_realtime(node_id,process_instance,sequence,received_at,values_json) VALUES(?,'current',1,?,'{}')", entry, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	sample := func(ep *controlpb.TunnelEndpoint) *controlpb.ExitHealth {
		return &controlpb.ExitHealth{ExitId: exit, AddressId: ep.AddressId, EndpointConfigDigest: ep.EndpointConfigDigest, AddressVersion: ep.AddressVersion, State: "healthy", LastProbe: "success", CheckedAtUnix: now.Unix()}
	}
	store := func(hs ...*controlpb.ExitHealth) []*controlpb.ExitHealth {
		t.Helper()
		tx, err := p.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = p.storeExitHealth(ctx, tx, entry, hs); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var data []byte
		if err = p.db.QueryRow("SELECT exit_health_json FROM probe_realtime WHERE node_id=?", entry).Scan(&data); err != nil {
			t.Fatal(err)
		}
		var out []*controlpb.ExitHealth
		if err = json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	store(sample(public))
	inputs, err := p.loadEntryReadinessInputs(ctx, entry, now)
	if err != nil {
		t.Fatal(err)
	}
	if !inputs.tunnelReady(snapshot.Rules[0].Id) || inputs.tunnelReady(r.Id) {
		t.Fatal("公网成功使专线就绪")
	}
	if hs := store(sample(public), sample(ix)); len(hs) != 2 {
		t.Fatal("健康按服务器去重", hs)
	}
	old := sample(ix)
	ix.Port++
	ix.EndpointConfigDigest = forwarding.EndpointConfigDigest(ix)
	writeSnapshot()
	if hs := store(old); len(hs) != 0 {
		t.Fatal("迟到旧摘要刷新健康", hs)
	}
	inputs, err = p.loadEntryReadinessInputs(ctx, entry, now)
	if err != nil || inputs.tunnelReady(r.Id) {
		t.Fatal("迟到旧摘要使专线就绪", err)
	}
	if hs := store(sample(ix)); len(hs) != 1 {
		t.Fatal(hs)
	}
	inputs, err = p.loadEntryReadinessInputs(ctx, entry, now)
	if err != nil || !inputs.tunnelReady(r.Id) {
		t.Fatal("新摘要未就绪", err)
	}
	forged := sample(ix)
	forged.AddressId = "public"
	if matchesEndpointHealth(snapshot, forged) {
		t.Fatal("只核对摘要未核对地址")
	}
	// Even a matching dedicated handshake cannot confirm a public-IP action.
	if _, err = p.db.Exec("UPDATE exit_address_progress SET handshaken_at=0 WHERE entry_id=?", entry); err != nil {
		t.Fatal(err)
	}
	forged = sample(ix)
	forged.AddressVersion = "address-a"
	store(forged)
	var at int64
	if err = p.db.QueryRow("SELECT handshaken_at FROM exit_address_progress WHERE entry_id=?", entry).Scan(&at); err != nil || at != 0 {
		t.Fatal("专线确认了公网换址", at, err)
	}
}

func TestEndpointGroupAPIOrderAndValidation(t *testing.T) {
	_, request := ixAddressAPI(t)
	b := request("POST", "/api/nodes", map[string]string{"name": "B"}, 201)["id"].(string)
	c := request("POST", "/api/nodes", map[string]string{"name": "C"}, 201)["id"].(string)
	for i, id := range []string{b, c} {
		request("PUT", "/api/nodes/"+id+"/tunnel", map[string]any{"address": "10.0.0.2", "listen_port": 22001 + i, "transport": "wss"}, 200)
	}
	members := []any{map[string]any{"node_id": c, "address_id": "public"}, map[string]any{"node_id": b, "address_id": "public"}}
	body := map[string]any{"name": "组", "strategy": "primary_backup", "member_addresses": members}
	g := request("POST", "/api/exit-groups", body, 201)
	if !reflect.DeepEqual(g["member_addresses"], members) || !reflect.DeepEqual(g["members"], []any{c, b}) {
		t.Fatal("成员顺序丢失", g)
	}
	path := "/api/exit-groups/" + g["id"].(string)
	body["members"] = []string{c, b}
	request("PUT", path, body, 200)
	for _, bad := range []any{
		[]memberAddress{{b, "ix"}}, []memberAddress{{b, "missing"}}, []memberAddress{{b, ""}}, []memberAddress{{b, "public"}, {b, "public"}},
	} {
		request("POST", "/api/exit-groups", map[string]any{"name": "非法", "strategy": "primary_backup", "member_addresses": bad}, 400)
	}
	for _, bad := range []any{[]string{b, c}, []string{b}, []string{}, nil} {
		body["members"] = bad
		request("PUT", path, body, 400)
	}
	legacy := request("PUT", path, map[string]any{"name": "组", "strategy": "primary_backup", "members": []string{b, c}}, 200)
	if !reflect.DeepEqual(legacy["members"], []any{b, c}) {
		t.Fatal(legacy)
	}
}
