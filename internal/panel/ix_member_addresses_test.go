package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

func TestIXMemberMigrationPreserves47OrderAndConstrainsAddressOwnership(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:47] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"INSERT INTO nodes(id,name,created_at) VALUES('b','B',1),('c','C',1)",
		"INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext) VALUES('b','10.0.0.8',22001,'wss','stable','cert',X'1234'),('c','10.0.0.9',22002,'wss','stable','cert',X'1234')",
		"INSERT INTO exit_groups(id,name,strategy) VALUES('g','旧组','primary_backup')",
		"INSERT INTO exit_group_members(group_id,node_id,position) VALUES('g','c',0),('g','b',1)",
		"INSERT INTO tunnel_addresses VALUES('b','ix','专线','10.0.0.1',NULL,0)",
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	groups, err := loadExitGroups(context.Background(), p.db)
	if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Members, []string{"c", "b"}) {
		t.Fatal(groups, err)
	}
	rows, err := p.db.Query("SELECT address_id FROM exit_group_members ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if !reflect.DeepEqual(ids, []string{"public", "public"}) {
		t.Fatal(ids)
	}
	for _, q := range []string{
		"UPDATE exit_group_members SET address_id='ix' WHERE node_id='c'",
		"UPDATE exit_group_members SET address_id='missing' WHERE node_id='b'",
		"UPDATE exit_group_members SET address_id='' WHERE node_id='b'",
		"INSERT INTO exit_group_members(group_id,node_id,position,address_id) VALUES('g','b',2,'ix')",
	} {
		if _, err := p.db.Exec(q); err == nil {
			t.Fatal("未拒绝非法成员", q)
		}
	}
	if _, err = p.db.Exec("UPDATE exit_group_members SET address_id='ix' WHERE node_id='b'"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DELETE FROM tunnel_addresses WHERE node_id='b' AND id='ix'",
		"UPDATE tunnel_addresses SET node_id='c' WHERE node_id='b' AND id='ix'",
		"UPDATE tunnel_addresses SET id='renamed' WHERE node_id='b' AND id='ix'",
	} {
		if _, err := p.db.Exec(q); err == nil {
			t.Fatal("引用约束未生效", q)
		}
	}
}

func TestIXMemberSnapshotsSelectAddressesAndGateOnlyTheirConsumers(t *testing.T) {
	p, request := ixAddressAPI(t)
	a := request("POST", "/api/nodes", map[string]string{"name": "A"}, 201)["id"].(string)
	b := request("POST", "/api/nodes", map[string]string{"name": "B"}, 201)["id"].(string)
	ctx := context.Background()
	admit := func(identity bool) {
		t.Helper()
		c := nodeCompatibility{ProcessInstance: "current", ControlProtocol: 2, Node: compatibility.Current(), Capabilities: []string{compatibility.IXAddressDialing}}
		c.Node.Capabilities = []string{compatibility.IXAddressDialing}
		if identity {
			c.Node.Capabilities = append(c.Node.Capabilities, compatibility.IXMemberIdentity)
			c.Capabilities = append(c.Capabilities, compatibility.IXMemberIdentity)
		}
		if err := p.commitForwarding(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec("UPDATE nodes SET process_instance='current',tunnel_authorization_version=1"); err != nil {
				return err
			}
			return p.saveNodeCompatibility(ctx, tx, a, c)
		}); err != nil {
			t.Fatal(err)
		}
	}
	admit(false)
	saved := request("PUT", "/api/nodes/"+b+"/tunnel", map[string]any{"address": "public.test", "listen_port": 22001, "public_port": 2443, "transport": "wss", "dedicated_addresses": []any{map[string]any{"name": "IX", "address": "ix.test", "public_port": 3443}}}, 200)
	ix := saved["dedicated_addresses"].([]any)[0].(map[string]any)["id"].(string)
	for i, id := range []string{"public", ix} {
		g := request("POST", "/api/exit-groups", map[string]any{"name": id, "strategy": "primary_backup", "member_addresses": []any{map[string]any{"node_id": b, "address_id": id}}}, 201)["id"]
		request("POST", "/api/rules", map[string]any{"name": id, "entry_node_id": a, "exit_group_id": g, "kind": "tunnel", "listen_port": 22002 + i, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)
	}
	m, err := p.controlMessage(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot.ExitGroups) != 1 || m.Snapshot.ExitGroups[0].Exits[0].AddressId != "" {
		t.Fatal("02 公网形状变化或专线未门禁", m)
	}
	views := request("GET", "/api/rules", nil, 200)["rules"].([]any)
	for _, raw := range views {
		v := raw.(map[string]any)
		if v["name"] == ix {
			bs := v["configuration_blockers"].([]any)
			if len(bs) != 1 || bs[0].(map[string]any)["capability"] != compatibility.IXMemberIdentity || bs[0].(map[string]any)["address_id"] != ix {
				t.Fatal(v)
			}
		}
	}
	admit(true)
	m, err = p.controlMessage(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot.ExitGroups) != 2 {
		t.Fatal("跨组双地址未下发", m)
	}
	var credential, name string
	for _, g := range m.Snapshot.ExitGroups {
		ep := g.Exits[0]
		if ep.EndpointConfigDigest != forwarding.EndpointConfigDigest(ep) {
			t.Fatal("摘要不匹配", ep)
		}
		if ep.AddressId == ix {
			if ep.Address != "ix.test" || ep.Port != 3443 || ep.AddressVersion != "" {
				t.Fatal(ep)
			}
		} else if ep.AddressId != "public" || ep.Address != "public.test" || ep.Port != 2443 {
			t.Fatal(ep)
		}
		if credential != "" && (credential != ep.Credential || name != ep.ServerName) {
			t.Fatal("地址替换服务器凭据/证书")
		}
		credential, name = ep.Credential, ep.ServerName
	}
}

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

func TestIXMemberAPIProjectionOwnershipAndReferencedDeletionRollback(t *testing.T) {
	p, request := ixAddressAPI(t)
	b := request("POST", "/api/nodes", map[string]string{"name": "B"}, 201)["id"].(string)
	c := request("POST", "/api/nodes", map[string]string{"name": "C"}, 201)["id"].(string)
	body := map[string]any{"address": "127.0.0.1", "listen_port": 22001, "transport": "wss", "dedicated_addresses": []any{map[string]any{"name": "沪日 IX", "address": "ix.test", "public_port": 2443}}}
	saved := request("PUT", "/api/nodes/"+b+"/tunnel", body, 200)
	request("PUT", "/api/nodes/"+c+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 22002, "transport": "wss"}, 200)
	ix := saved["dedicated_addresses"].([]any)[0].(map[string]any)["id"].(string)
	members := []any{map[string]any{"node_id": c, "address_id": "public"}, map[string]any{"node_id": b, "address_id": ix}}
	groupBody := map[string]any{"name": "专线组", "strategy": "primary_backup", "member_addresses": members}
	g := request("POST", "/api/exit-groups", groupBody, 201)
	if !reflect.DeepEqual(g["member_addresses"], members) || !reflect.DeepEqual(g["members"], []any{c, b}) {
		t.Fatal("投影/顺序丢失", g)
	}
	path := "/api/exit-groups/" + g["id"].(string)
	groupBody["members"] = []string{c, b}
	request("PUT", path, groupBody, 200)
	var before uint64
	if err := p.db.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", b).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{
		[]any{map[string]any{"node_id": c, "address_id": ix}},
		[]any{map[string]any{"node_id": b, "address_id": "missing"}},
		[]any{map[string]any{"node_id": b, "address_id": ""}},
		[]any{map[string]any{"node_id": b, "address_id": "public"}, map[string]any{"node_id": b, "address_id": ix}},
	} {
		request("POST", "/api/exit-groups", map[string]any{"name": "非法组", "strategy": "primary_backup", "member_addresses": bad}, 400)
	}
	for _, old := range []any{[]string{b, c}, []string{b}, []string{}, nil} {
		groupBody["members"] = old
		request("PUT", path, groupBody, 400)
	}
	for _, bad := range []any{nil, []any{}} {
		request("PUT", path, map[string]any{"name": "空", "strategy": "primary_backup", "members": []string{b}, "member_addresses": bad}, 400)
	}
	// Referenced even without an enabled rule; editing a retained address is legal.
	body["dedicated_addresses"] = saved["dedicated_addresses"]
	request("PUT", "/api/nodes/"+b+"/tunnel", body, 200)
	body["address"] = "8.8.8.8"
	body["dedicated_addresses"] = []any{}
	conflict := request("PUT", "/api/nodes/"+b+"/tunnel", body, 409)
	if !strings.Contains(fmt.Sprint(conflict), "专线组") {
		t.Fatal("409 未指明引用组", conflict)
	}
	var after uint64
	var address string
	if err := p.db.QueryRow("SELECT desired_revision FROM nodes WHERE id=?", b).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT address FROM tunnel_exits WHERE node_id=?", b).Scan(&address); err != nil {
		t.Fatal(err)
	}
	if after != before || address != "127.0.0.1" {
		t.Fatal("失败事务推进配置", before, after, address)
	}
	// A legacy client explicitly selects public, freeing the dedicated reference.
	legacy := request("PUT", path, map[string]any{"name": "专线组", "strategy": "primary_backup", "members": []string{b, c}}, 200)
	if !reflect.DeepEqual(legacy["member_addresses"], []any{map[string]any{"node_id": b, "address_id": "public"}, map[string]any{"node_id": c, "address_id": "public"}}) {
		t.Fatal(legacy)
	}
	request("PUT", "/api/nodes/"+b+"/tunnel", body, 200)
}
