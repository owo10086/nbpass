package panel

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"os"
	"path/filepath"
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

// Exercise the production starting point in one migration transaction. Keep the
// original columns, including nullable values and encrypted bytes, in comparison
// so a later ALTER cannot hide data loss behind a handful of selected fields.
func TestIXFinalMigration46To51AndFreshInstallation(t *testing.T) {
	for _, initial := range []int{46, 0} {
		t.Run(fmt.Sprintf("from_%d", initial), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			deps := diskDependencies(dir)
			key, err := deps.KeyFile.LoadOrCreate(ctx, deps.Random)
			if err != nil {
				t.Fatal(err)
			}
			db, err := deps.Persistence.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			p := &Panel{db: db}
			if initial == 0 {
				if err := p.migrate(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				for i, migration := range migrations[:46] {
					if _, err := db.Exec(migration); err != nil {
						t.Fatalf("migration %d: %v", i+1, err)
					}
				}
				if _, err := db.Exec("PRAGMA user_version=46"); err != nil {
					t.Fatal(err)
				}
			}
			for _, statement := range []string{
				`INSERT INTO nodes(id,name,created_at,token_hash,desired_revision,applied_revision,attempted_revision,applied_digest,process_instance,sort_order) VALUES('a','入口',1,X'0102',987,985,986,'旧摘要','旧进程',2),('b','末跳',2,X'0304',777,776,777,'出口摘要','出口进程',1)`,
				`INSERT INTO secrets VALUES('旧密钥材料',X'9876543210')`,
				`INSERT INTO entry_groups(id,name) VALUES('eg','入口组')`,
				`INSERT INTO entry_group_members VALUES('eg','a',1)`,
				`INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext,address_version) VALUES('b','10.1.2.3',22001,'wss','stable-identity',X'30313233',X'11223344','old-address-version')`,
				`INSERT INTO exit_groups(id,name,strategy) VALUES('xg','出口组','primary_backup')`,
				`INSERT INTO exit_group_members(group_id,node_id,position) VALUES('xg','b',1)`,
				`INSERT INTO tunnel_pairs VALUES('a','b',X'55667788')`,
				`INSERT INTO forward_rules(id,name,entry_group_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,exit_group_id,sort_order) VALUES('t','旧单跳','eg','tunnel',24001,'example.test','ipv4',443,0,1,'xg',5)`,
				`INSERT INTO forward_rules(id,name,entry_node_id,kind,listen_port,target_address,target_family,target_port,allow_private,enabled,sort_order) VALUES('d','停用直连','a','direct',24002,'127.0.0.1','ipv4',8080,1,0,NULL)`,
				`INSERT INTO node_snapshots VALUES('a',X'8899aabb','旧快照摘要',1)`,
				`INSERT INTO node_rule_reports VALUES('a',986,'旧摘要',1)`,
				`INSERT INTO node_rule_results VALUES('a','t','eg',24001,985,'旧应用错误')`,
				`INSERT INTO retired_control_instances VALUES('a','已退休进程')`,
				`INSERT INTO rule_authorizations VALUES('t',910,'旧隧道授权指纹'),('d',911,'停用仍保留授权高水位')`,
				`INSERT INTO tunnel_authorization_confirmations VALUES('b','a','t',910)`,
				`INSERT INTO rule_traffic_totals VALUES('t',123456789,987654321),('d',11,22)`,
				`INSERT INTO rule_traffic_cursors VALUES('a','旧进程','t',120,23,123,456),('a','更旧进程','d',60,19,11,22)`,
				`INSERT INTO rule_traffic_realtime VALUES('a','t','旧进程',23,4,1234,1235),('a','d','更旧进程',19,0,1000,1001)`,
				`INSERT INTO rule_traffic_sources VALUES('a','t'),('a','d')`,
				`INSERT INTO recovery_nodes VALUES('a',999,123,456,0)`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatalf("%s: %v", statement, err)
				}
			}
			tables := []string{"nodes", "secrets", "entry_groups", "entry_group_members", "tunnel_exits", "exit_groups", "exit_group_members", "tunnel_pairs", "forward_rules", "node_snapshots", "node_rule_reports", "node_rule_results", "retired_control_instances", "rule_authorizations", "tunnel_authorization_confirmations", "rule_traffic_totals", "rule_traffic_cursors", "rule_traffic_realtime", "rule_traffic_sources", "recovery_nodes"}
			columns := map[string][]string{}
			before := map[string]string{}
			dump := func(table string) string {
				t.Helper()
				selected := "*"
				if cols := columns[table]; len(cols) > 0 {
					selected = strings.Join(cols, ",")
				}
				rows, err := db.Query("SELECT " + selected + " FROM " + table + " ORDER BY 1,2")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				cols, err := rows.Columns()
				if err != nil {
					t.Fatal(err)
				}
				columns[table] = cols
				values := [][]any{}
				for rows.Next() {
					row := make([]any, len(cols))
					args := make([]any, len(cols))
					for i := range row {
						args[i] = &row[i]
					}
					if err := rows.Scan(args...); err != nil {
						t.Fatal(err)
					}
					values = append(values, row)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(values)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			for _, table := range tables {
				before[table] = dump(table)
			}
			if err := p.migrate(ctx); err != nil {
				t.Fatal(err)
			}
			// Close/reopen the actual SQLite file; no background reconcile can rewrite
			// the fixture and conceal the migration's own effects.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = deps.Persistence.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range tables {
				if after := dump(table); after != before[table] {
					t.Fatalf("%s data changed: %s -> %s", table, before[table], after)
				}
			}
			var version, enabled int
			if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 51 {
				t.Fatal(version, err)
			}
			if err := db.QueryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
				t.Fatal(enabled, err)
			}
			var outbound, addressID string
			var public sql.NullInt64
			var chain sql.NullString
			if err := db.QueryRow("SELECT n.outbound_ipv4,e.public_port,m.address_id,r.chain_id FROM nodes n JOIN tunnel_exits e ON e.node_id=n.id JOIN exit_group_members m ON m.node_id=n.id JOIN forward_rules r ON r.exit_group_id=m.group_id WHERE r.id='t'").Scan(&outbound, &public, &addressID, &chain); err != nil || outbound != "" || public.Valid || addressID != "public" || chain.Valid {
				t.Fatal(outbound, public, addressID, chain, err)
			}
			currentKey, err := os.ReadFile(filepath.Join(dir, "panel.key"))
			if err != nil || !bytes.Equal(key, currentKey) {
				t.Fatal("panel key changed", err)
			}
			for _, table := range []string{"tunnel_addresses", "forward_chains", "forward_chain_hops", "node_tunnel_grant_results"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal(table, count, err)
				}
			}
			for _, statement := range []string{
				`INSERT INTO tunnel_addresses VALUES('b','ix','专线','ix.example.test',3443,0)`,
				`UPDATE exit_group_members SET address_id='ix' WHERE node_id='b'`,
				`INSERT INTO forward_chains VALUES('chain','新链')`,
				`INSERT INTO forward_chain_hops VALUES('chain',1,'xg')`,
				`UPDATE forward_rules SET chain_id='chain',exit_group_id='' WHERE id='t'`,
				`INSERT INTO node_tunnel_grant_results VALUES('b','a','t',910,777,'')`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(statement, err)
				}
			}
			for _, statement := range []string{
				`DELETE FROM forward_chains WHERE id='chain'`,
				`DELETE FROM exit_groups WHERE id='xg'`,
				`DELETE FROM tunnel_addresses WHERE node_id='b' AND id='ix'`,
				`INSERT INTO node_tunnel_grant_results VALUES('missing','a','t',910,777,'')`,
				`INSERT INTO node_tunnel_grant_results VALUES('b','missing','t',910,777,'')`,
				`INSERT INTO node_tunnel_grant_results VALUES('b','a','missing',910,777,'')`,
			} {
				if _, err := db.Exec(statement); err == nil {
					t.Fatal("reference protection missing", statement)
				}
			}
			rows, err := db.Query("PRAGMA foreign_key_check")
			if err != nil {
				t.Fatal(err)
			}
			if rows.Next() {
				t.Fatal("foreign key corruption")
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if _, err := db.Exec("DELETE FROM forward_rules"); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"rule_authorizations", "tunnel_authorization_confirmations", "rule_traffic_totals", "rule_traffic_cursors", "rule_traffic_realtime", "rule_traffic_sources", "node_tunnel_grant_results"} {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("cascade", table, count, err)
				}
			}
			t.Logf("schema 51; %d original tables/columns preserved; public defaults, IX references and all rule child cascades verified", len(tables))
		})
	}
}
