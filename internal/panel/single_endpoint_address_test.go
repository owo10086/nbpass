package panel

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/forwarding"
)

func TestSingleEndpointMigrationRemovesDedicatedAddresses(t *testing.T) {
	ctx := context.Background()
	db, err := diskDependencies(t.TempDir()).Persistence.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, migration := range migrations[:51] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO nodes(id,name,created_at) VALUES('b','B',1),('c','C',1)`,
		`INSERT INTO tunnel_exits(node_id,address,listen_port,transport,server_name,certificate_pem,private_key_ciphertext,public_port,address_version) VALUES('b','10.0.0.8',22001,'wss','stable','cert',X'1234',2443,'version'),('c','exit.test',22002,'wss','stable-c','cert',X'1234',NULL,'')`,
		`INSERT INTO tunnel_addresses VALUES('b','ix','旧专线','10.0.0.2',3443,0)`,
		`INSERT INTO exit_groups(id,name,strategy) VALUES('g','组','primary_backup')`,
		`INSERT INTO exit_group_members(group_id,node_id,position,address_id) VALUES('g','c',0,'public'),('g','b',1,'ix')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	p := &Panel{db: db}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	groups, err := loadExitGroups(ctx, db)
	if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Members, []string{"c", "b"}) || !reflect.DeepEqual(groups[0].MemberAddresses, []memberAddress{{"c", "public"}, {"b", "public"}}) {
		t.Fatal("升级未保留成员主备顺序", groups, err)
	}
	var address, name, version string
	var port int
	if err := db.QueryRow(`SELECT address,public_port,server_name,address_version FROM tunnel_exits WHERE node_id='b'`).Scan(&address, &port, &name, &version); err != nil || address != "10.0.0.8" || port != 2443 || name != "stable" || version != "version" {
		t.Fatal("升级改变主地址或证书身份", address, port, name, version, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='tunnel_addresses'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("专线存储未删除", count, err)
	}
	if _, err := db.Exec(`UPDATE exit_group_members SET address_id='ix' WHERE node_id='b'`); err == nil {
		t.Fatal("升级后仍允许选择专线地址")
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("重复启动迁移失败", err)
	}
}

func TestSingleEndpointAPIRejectsRemovedDedicatedAddresses(t *testing.T) {
	_, request := ixAddressAPI(t)
	id := request("POST", "/api/nodes", map[string]string{"name": "B"}, 201)["id"].(string)
	body := map[string]any{"address": "10.0.0.2", "listen_port": 22001, "transport": "wss"}
	saved := request("PUT", "/api/nodes/"+id+"/tunnel", body, 200)
	if _, exists := saved["dedicated_addresses"]; exists {
		t.Fatal("端点响应仍暴露已删除功能", saved)
	}
	body["dedicated_addresses"] = []any{map[string]any{"name": "旧专线", "address": "10.0.0.3"}}
	request("PUT", "/api/nodes/"+id+"/tunnel", body, 400)
	request("POST", "/api/exit-groups", map[string]any{"name": "组", "strategy": "primary_backup", "member_addresses": []memberAddress{{id, "ix"}}}, 400)
	request("POST", "/api/exit-groups", map[string]any{"name": "组", "strategy": "primary_backup", "members": []string{id}}, 201)
}

func TestSingleEndpointUpgradeRecompilesSnapshotsBeforeSameProcessReconnect(t *testing.T) {
	for _, compileFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "retry_after_compile_failure"}[compileFailure], func(t *testing.T) {
			p, entry, exit, _, _ := tunnelAuthorizationProtocolFixture(t)
			ctx := context.Background()
			message, err := p.controlMessage(ctx, entry)
			if err != nil {
				t.Fatal(err)
			}
			old := message.Snapshot
			endpoint := old.ExitGroups[0].Exits[0]
			endpoint.AddressId, endpoint.Address, endpoint.Port = "ix", "10.0.0.3", 3443
			endpoint.EndpointConfigDigest = forwarding.EndpointConfigDigest(endpoint)
			old.Digest = forwarding.SnapshotDigest(old)
			data, err := proto.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			data, err = p.secrets.(*sealedSecrets).seal("node-snapshot:"+entry, data)
			if err != nil {
				t.Fatal(err)
			}
			// Restore the version-51 schema and a real encrypted old endpoint snapshot.
			var pendingColumn int
			if err := p.db.QueryRow(`SELECT count(*) FROM pragma_table_info('node_snapshots') WHERE name='address_upgrade_pending'`).Scan(&pendingColumn); err != nil {
				t.Fatal(err)
			}
			if pendingColumn > 0 {
				if _, err := p.db.Exec(`ALTER TABLE node_snapshots DROP COLUMN address_upgrade_pending`); err != nil {
					t.Fatal(err)
				}
			}
			_, createAddresses, _ := strings.Cut(ixAddressesMigration, ";")
			_, createAddressTriggers, _ := strings.Cut(ixMemberAddressesMigration, ";")
			for _, statement := range []string{
				`DROP TRIGGER exit_member_address_insert; DROP TRIGGER exit_member_address_update;`,
				createAddresses, createAddressTriggers,
				`PRAGMA user_version=51`,
				`UPDATE nodes SET process_instance='current'`,
			} {
				if _, err := p.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.db.Exec(`INSERT INTO tunnel_addresses VALUES(?,'ix','旧专线','10.0.0.3',3443,0)`, exit); err != nil {
				t.Fatal(err)
			}
			if _, err := p.db.Exec(`UPDATE exit_group_members SET address_id='ix' WHERE node_id=?`, exit); err != nil {
				t.Fatal(err)
			}
			if _, err := p.db.Exec(`UPDATE node_snapshots SET snapshot=?,digest=?,encrypted=1 WHERE node_id=?`, data, old.Digest, entry); err != nil {
				t.Fatal(err)
			}
			var privateKey []byte
			if compileFailure {
				if err := p.db.QueryRow("SELECT private_key_ciphertext FROM tunnel_exits WHERE node_id=?", exit).Scan(&privateKey); err != nil {
					t.Fatal(err)
				}
				if _, err := p.db.Exec("UPDATE tunnel_exits SET private_key_ciphertext=X'00' WHERE node_id=?", exit); err != nil {
					t.Fatal(err)
				}
			}
			config, deps := p.config, p.deps
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if compileFailure {
				failed, err := Open(ctx, config, deps)
				if err == nil {
					failed.Close()
					t.Fatal("旧快照编译失败时面板仍启动")
				}
				db, err := deps.Persistence.Open(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var pending int
				if err := db.QueryRow("SELECT count(*) FROM node_snapshots WHERE address_upgrade_pending=1").Scan(&pending); err != nil || pending == 0 {
					db.Close()
					t.Fatal("编译失败丢失重试标记", pending, err)
				}
				if _, err := db.Exec("UPDATE tunnel_exits SET private_key_ciphertext=? WHERE node_id=?", privateKey, exit); err != nil {
					db.Close()
					t.Fatal(err)
				}
				db.Close()
			}
			reopened, err := Open(ctx, config, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			tx, err := reopened.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			changes, err := reopened.admitTunnelAuthorization(ctx, tx, entry, false, 1, false)
			if err != nil || len(changes) != 0 {
				t.Fatal("同进程重连不应触发重编译", changes, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			current, err := reopened.controlMessage(ctx, entry)
			if err != nil {
				t.Fatal(err)
			}
			endpoint = current.Snapshot.ExitGroups[0].Exits[0]
			if endpoint.Address != "127.0.0.1" || endpoint.Port != 22001 || endpoint.AddressId != "" && endpoint.AddressId != "public" || current.Snapshot.Revision <= old.Revision {
				t.Fatal("升级仍下发旧专线地址", endpoint, current.Snapshot.Revision, old.Revision)
			}
			revision := current.Snapshot.Revision
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			again, err := Open(ctx, config, deps)
			if err != nil {
				t.Fatal(err)
			}
			defer again.Close()
			message, err = again.controlMessage(ctx, entry)
			if err != nil || message.Snapshot.Revision != revision {
				t.Fatal("重复启动改变快照版本", message, err)
			}
			var pending int
			if err := again.db.QueryRow("SELECT count(*) FROM node_snapshots WHERE address_upgrade_pending=1").Scan(&pending); err != nil || pending != 0 {
				t.Fatal("成功升级未清除标记", pending, err)
			}
		})
	}
}
