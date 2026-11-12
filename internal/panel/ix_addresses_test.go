package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"reflect"
	"strings"
	"testing"
)

func TestIXAddressMigrationPreservesVersion46(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range migrations[:46] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"INSERT INTO nodes(id,name,created_at) VALUES('b','旧服务器',1)",
		"INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext,address_version) VALUES('b','10.0.0.8',22001,'wss','stable','cert',X'1234','version')",
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	p := openDiskPanel(t, dir)
	defer p.Close()
	var address, name, cert, version string
	var port int
	var public sql.NullInt64
	var key []byte
	if err := p.db.QueryRow("SELECT address,listen_port,server_name,certificate_pem,private_key_ciphertext,address_version,public_port FROM tunnel_exits WHERE node_id='b'").Scan(&address, &port, &name, &cert, &key, &version, &public); err != nil {
		t.Fatal(err)
	}
	if address != "10.0.0.8" || port != 22001 || name != "stable" || cert != "cert" || version != "version" || !reflect.DeepEqual(key, []byte{0x12, 0x34}) || public.Valid {
		t.Fatal("升级改变旧公网槽/身份", address, port, public)
	}
	var count int
	if err := p.db.QueryRow("SELECT count(*) FROM tunnel_addresses").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	for _, q := range []string{
		"INSERT INTO tunnel_addresses VALUES('missing','a','IX','10.0.0.1',NULL,0)",
		"INSERT INTO tunnel_addresses VALUES('b','public','IX','10.0.0.1',NULL,0)",
		"UPDATE tunnel_exits SET public_port=0 WHERE node_id='b'",
		"INSERT INTO tunnel_addresses VALUES('b','a','IX','10.0.0.1',65536,0)",
	} {
		if _, err := p.db.Exec(q); err == nil {
			t.Fatal("约束未生效", q)
		}
	}
	if _, err := p.db.Exec("INSERT INTO tunnel_addresses VALUES('b','a','IX','10.0.0.1',NULL,0)"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"INSERT INTO tunnel_addresses VALUES('b','a','重复','10.0.0.2',NULL,1)", "INSERT INTO tunnel_addresses VALUES('b','c','重复位置','10.0.0.2',NULL,0)"} {
		if _, err := p.db.Exec(q); err == nil {
			t.Fatal("未约束稳定身份/顺序", q)
		}
	}
}

func ixAddressAPI(t *testing.T) (*Panel, func(string, string, any, int) map[string]any) {
	t.Helper()
	p := openDiskPanel(t, t.TempDir())
	t.Cleanup(func() { p.Close() })
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	request := func(method, path string, body any, code int) map[string]any {
		t.Helper()
		v := map[string]any{}
		if err := json.Unmarshal(diskRequest(t, p, method, path, body, cookie, code).Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	return p, request
}

func TestIXAddressAPIFormPreservesOmittedFieldsAndStableIDs(t *testing.T) {
	_, request := ixAddressAPI(t)
	id := request("POST", "/api/nodes", map[string]string{"name": "B"}, 201)["id"].(string)
	path := "/api/nodes/" + id + "/tunnel"
	base := map[string]any{"address": "  EXIT.Example.Test. ", "listen_port": 22001, "transport": "wss", "public_port": 2443, "dedicated_addresses": []any{map[string]any{"name": " 沪日 IX ", "address": " IX.Example.Test. ", "public_port": 3443}, map[string]any{"name": "内网", "address": "10.0.0.2"}}}
	saved := request("PUT", path, base, 200)
	if saved["configured"] != true || saved["address"] != "exit.example.test" || saved["public_port"] != float64(2443) {
		t.Fatal("未返回规范化公开配置", saved)
	}
	addresses := saved["dedicated_addresses"].([]any)
	first := addresses[0].(map[string]any)
	if first["id"] == "" || first["id"] == "public" || first["name"] != "沪日 IX" || first["address"] != "ix.example.test" || addresses[1].(map[string]any)["public_port"] != nil {
		t.Fatal(addresses)
	}
	for key := range saved {
		if strings.Contains(key, "private") || strings.Contains(key, "credential") {
			t.Fatal("公开响应泄漏秘密", key)
		}
	}
	base = map[string]any{"address": "127.0.0.1", "listen_port": 22001, "transport": "wss"}
	preserved := request("PUT", path, base, 200)
	if preserved["public_port"] != float64(2443) || !reflect.DeepEqual(preserved["dedicated_addresses"], addresses) || preserved["certificate_pem"] != saved["certificate_pem"] {
		t.Fatal("省略字段清空配置/身份", preserved)
	}
	base["public_port"] = nil
	base["dedicated_addresses"] = []any{addresses[1], addresses[0]}
	reordered := request("PUT", path, base, 200)
	if reordered["public_port"] != nil || reordered["dedicated_addresses"].([]any)[1].(map[string]any)["id"] != first["id"] {
		t.Fatal("重排改变稳定 ID", reordered)
	}
	base["dedicated_addresses"] = []any{}
	cleared := request("PUT", path, base, 200)
	if len(cleared["dedicated_addresses"].([]any)) != 0 {
		t.Fatal("显式空列表未清空")
	}
	listed := request("GET", "/api/tunnel-exits", nil, 200)["exits"].([]any)[0]
	delete(cleared, "configured")
	if !reflect.DeepEqual(listed, cleared) {
		t.Fatal("保存与回读不一致", listed, cleared)
	}
}

func TestIXAddressAPIRejectsInvalidOrForeignIDsAndListenerConflicts(t *testing.T) {
	p, request := ixAddressAPI(t)
	id := request("POST", "/api/nodes", map[string]string{"name": "B"}, 201)["id"].(string)
	other := request("POST", "/api/nodes", map[string]string{"name": "C"}, 201)["id"].(string)
	body := map[string]any{"address": "127.0.0.1", "listen_port": 22001, "transport": "wss", "dedicated_addresses": []any{map[string]any{"name": "IX", "address": "10.0.0.1"}}}
	saved := request("PUT", "/api/nodes/"+id+"/tunnel", body, 200)
	first := saved["dedicated_addresses"].([]any)[0]
	body["dedicated_addresses"] = []any{first}
	request("PUT", "/api/nodes/"+other+"/tunnel", body, 400)
	body["dedicated_addresses"] = []any{first, first}
	request("PUT", "/api/nodes/"+id+"/tunnel", body, 400)
	for _, bad := range []any{map[string]any{"id": "public", "name": "IX", "address": "10.0.0.1"}, map[string]any{"id": "unknown", "name": "IX", "address": "10.0.0.1"}, map[string]any{"name": " ", "address": "10.0.0.1"}, map[string]any{"name": strings.Repeat("中", 67), "address": "10.0.0.1"}} {
		body["dedicated_addresses"] = []any{bad}
		request("PUT", "/api/nodes/"+id+"/tunnel", body, 400)
	}
	delete(body, "dedicated_addresses")
	for _, address := range []string{"::1", "::ffff:127.0.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "https://exit.test", "exit.test:443", "-exit.test"} {
		body["address"] = address
		request("PUT", "/api/nodes/"+id+"/tunnel", body, 400)
		body["address"] = "127.0.0.1"
		body["dedicated_addresses"] = []any{map[string]any{"name": "IX", "address": address}}
		request("PUT", "/api/nodes/"+id+"/tunnel", body, 400)
		delete(body, "dedicated_addresses")
	}
	for _, port := range []any{0, -1, 65536, 1.5, "443"} {
		body["public_port"] = port
		request("PUT", "/api/nodes/"+id+"/tunnel", body, 400)
		delete(body, "public_port")
		body["dedicated_addresses"] = []any{map[string]any{"name": "IX", "address": "10.0.0.1", "public_port": port}}
		request("PUT", "/api/nodes/"+id+"/tunnel", body, 400)
		delete(body, "dedicated_addresses")
	}
	// Only real local listeners reserve ports; a public NAT port may coincide with business.
	request("POST", "/api/rules", map[string]any{"name": "业务", "entry_node_id": id, "kind": "direct", "listen_port": 23001, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)
	body["public_port"] = 23001
	request("PUT", "/api/nodes/"+id+"/tunnel", body, 200)
	for _, port := range []int{23001, 18080} {
		body["listen_port"] = port
		request("PUT", "/api/nodes/"+id+"/tunnel", body, 409)
	}
	var actual int
	if err := p.db.QueryRow("SELECT listen_port FROM tunnel_exits WHERE node_id=?", id).Scan(&actual); err != nil || actual != 22001 {
		t.Fatal("冲突未回滚", actual, err)
	}
}

func TestIXAddressSnapshotGatesDialerAndSeparatesMappedPort(t *testing.T) {
	p, request := ixAddressAPI(t)
	entry := request("POST", "/api/nodes", map[string]string{"name": "A"}, 201)["id"].(string)
	exit := request("POST", "/api/nodes", map[string]string{"name": "B"}, 201)["id"].(string)
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_authorization_version=1,process_instance='current'"); err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes/" + exit + "/tunnel"
	body := map[string]any{"address": "127.0.0.1", "listen_port": 22001, "transport": "wss"}
	request("PUT", path, body, 200)
	group := request("POST", "/api/exit-groups", map[string]any{"name": "组", "members": []string{exit}, "strategy": "primary_backup"}, 201)["id"]
	request("POST", "/api/rules", map[string]any{"name": "隧道", "entry_node_id": entry, "kind": "tunnel", "exit_group_id": group, "listen_port": 22002, "target_address": "8.8.8.8", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)
	initial, _ := p.controlMessage(context.Background(), entry)
	if len(initial.Snapshot.ExitGroups) != 1 {
		t.Fatal("旧普通 IPv4 配置被阻塞")
	}
	if initial.Snapshot.ExitGroups[0].Exits[0].GetEndpointConfigDigest() != "" {
		t.Fatal("改变旧快照形状")
	}
	body["address"] = "exit.example.test"
	body["public_port"] = 2443
	request("PUT", path, body, 200)
	blocked, _ := p.controlMessage(context.Background(), entry)
	if len(blocked.Snapshot.ExitGroups) != 0 || len(blocked.Snapshot.Rules) != 0 {
		t.Fatal("给旧拨号者下发新地址语义", blocked)
	}
	views := request("GET", "/api/rules", nil, 200)["rules"].([]any)[0].(map[string]any)
	blockers := views["configuration_blockers"].([]any)
	if len(blockers) != 1 || blockers[0].(map[string]any)["capability"] != "ix_address_dialing" || blockers[0].(map[string]any)["address_id"] != "public" {
		t.Fatal(views)
	}
	c := nodeCompatibility{ProcessInstance: "current", ControlProtocol: 2, Node: compatibility.Current(), Capabilities: []string{"tunnel_authorization", compatibility.IXAddressDialing}}
	c.Node.Capabilities = append(c.Node.Capabilities, compatibility.IXAddressDialing)
	if err := p.commitForwarding(context.Background(), func(tx *sql.Tx) error { return p.saveNodeCompatibility(context.Background(), tx, entry, c) }); err != nil {
		t.Fatal(err)
	}
	message, _ := p.controlMessage(context.Background(), entry)
	if len(message.Snapshot.ExitGroups) != 1 {
		t.Fatal("仅拨号能力仍被成员身份能力阻塞", message)
	}
	ep := message.Snapshot.ExitGroups[0].Exits[0]
	server, _ := p.controlMessage(context.Background(), exit)
	if ep.Address != "exit.example.test" || ep.Port != 2443 || server.Snapshot.TunnelServer.ListenPort != 22001 || ep.GetEndpointConfigDigest() == "" {
		t.Fatal("拨号/监听或摘要不正确", ep, server)
	}
	oldDigest := ep.GetEndpointConfigDigest()
	body["public_port"] = 2444
	request("PUT", path, body, 200)
	message, _ = p.controlMessage(context.Background(), entry)
	if message.Snapshot.ExitGroups[0].Exits[0].GetEndpointConfigDigest() == oldDigest {
		t.Fatal("端口修改未推进摘要")
	}
}

// A delayed success for an earlier mapped endpoint must neither confirm rotation
// nor make the current rule ready; samples already stored before an edit are bound too.
func TestIXAddressHealthRequiresCurrentEndpointDigest(t *testing.T) {
	p, entry, exit, _, _, _ := exitAddressProtocolFixture(t)
	ctx := context.Background()
	c := nodeCompatibility{ProcessInstance: "current", ControlProtocol: 2, Node: compatibility.Current(), Capabilities: []string{"tunnel_authorization", compatibility.IXAddressDialing}}
	c.Node.Capabilities = append(c.Node.Capabilities, compatibility.IXAddressDialing)
	if err := p.commitForwarding(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("UPDATE nodes SET process_instance='current' WHERE id=?", entry); err != nil {
			return err
		}
		if err := p.saveNodeCompatibility(ctx, tx, entry, c); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE tunnel_exits SET public_port=2443 WHERE node_id=?", exit)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	message, err := p.controlMessage(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	digest := message.Snapshot.ExitGroups[0].Exits[0].EndpointConfigDigest
	if digest == "" {
		t.Fatal("missing digest")
	}
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	now := p.deps.Clock.Now()
	if _, err = p.db.Exec("INSERT OR REPLACE INTO probe_realtime(node_id,process_instance,sequence,received_at,values_json) VALUES(?,'current',1,?,'{}')", entry, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	for _, sampleDigest := range []string{"", "previous-config", digest} {
		h := &controlpb.ExitHealth{ExitId: exit, AddressVersion: "address-a", EndpointConfigDigest: sampleDigest, State: "healthy", LastProbe: "success", CheckedAtUnix: now.Unix()}
		if _, err = p.db.Exec("UPDATE exit_address_progress SET handshaken_at=0 WHERE entry_id=?", entry); err != nil {
			t.Fatal(err)
		}
		tx, err := p.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err = p.storeExitHealth(ctx, tx, entry, []*controlpb.ExitHealth{h}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var at int64
		if err = p.db.QueryRow("SELECT handshaken_at FROM exit_address_progress WHERE entry_id=?", entry).Scan(&at); err != nil {
			t.Fatal(err)
		}
		if (at > 0) != (sampleDigest == digest) {
			t.Fatal("旧摘要确认新端点", sampleDigest, at)
		}
		// Readiness must recheck previously persisted evidence after a config mutation.
		raw, _ := json.Marshal([]*controlpb.ExitHealth{h})
		if _, err = p.db.Exec("UPDATE probe_realtime SET exit_health_json=? WHERE node_id=?", raw, entry); err != nil {
			t.Fatal(err)
		}
		var view map[string]any
		if err = json.Unmarshal(diskRequest(t, p, "GET", "/api/nodes/"+entry+"/exit-health", nil, cookie, 200).Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if (len(view["exits"].([]any)) > 0) != (sampleDigest == digest) {
			t.Fatal("页面把旧摘要显示为当前健康", view)
		}
		inputs, err := p.loadEntryReadinessInputs(ctx, entry, now)
		if err != nil {
			t.Fatal(err)
		}
		if inputs.health[[3]string{exit, "public", digest}] != (sampleDigest == digest) {
			t.Fatal("就绪复用旧配置成功", sampleDigest, inputs.health)
		}
	}
}
