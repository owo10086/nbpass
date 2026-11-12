package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"google.golang.org/protobuf/proto"
	"nbpass/internal/forwarding"
	"net/http"
	"path/filepath"
	"testing"

	"nbpass/internal/controlpb"
)

// 存储和分发物理属性：节点对秘密、私钥和含秘密快照不能以明文进入 DB/WAL。
func TestTunnelSecretsArePairScopedAndEncryptedIncludingSnapshots(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	defer p.Close()
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	cookie := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200).Result().Cookies()[0]
	request := func(method, path string, body any, code int) map[string]any {
		t.Helper()
		r := diskRequest(t, p, method, path, body, cookie, code)
		v := map[string]any{}
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	ids := []string{}
	for _, name := range []string{"entry1", "entry2", "exit1", "exit2", "unrelated"} {
		ids = append(ids, request("POST", "/api/nodes", map[string]string{"name": name}, 201)["id"].(string))
	}
	eg := []string{}
	xg := []string{}
	// 本测试检查已支持逐流授权节点的秘密隔离；未准入能力的节点不得拿到孤立凭据。
	if _, err := p.db.Exec("UPDATE nodes SET tunnel_authorization_version=1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		eg = append(eg, request("POST", "/api/entry-groups", map[string]any{"name": "入口组", "members": []string{ids[i]}}, 201)["id"].(string))
		request("PUT", "/api/nodes/"+ids[i+2]+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": 18001 + i, "transport": "wss"}, http.StatusOK)
		xg = append(xg, request("POST", "/api/exit-groups", map[string]any{"name": "出口组", "members": []string{ids[i+2]}, "strategy": "primary_backup"}, 201)["id"].(string))
	}
	for i := 0; i < 2; i++ {
		for j := 0; j < 2; j++ {
			request("POST", "/api/rules", map[string]any{"name": "隧道", "entry_group_id": eg[i], "exit_group_id": xg[j], "kind": "tunnel", "listen_port": 19000 + j, "target_address": "example.test", "target_family": "ipv4", "target_port": 443, "enabled": true}, 201)
		}
	}
	forbidden := map[string][]byte{}
	seen := map[string]bool{}
	for i, id := range ids[:4] {
		message, err := p.controlMessage(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		s := message.Snapshot
		if i < 2 {
			if len(s.ExitGroups) != 2 || s.TunnelServer != nil {
				t.Fatal("入口收到无关私钥或缺失出口")
			}
			for _, g := range s.ExitGroups {
				ep := g.Exits[0]
				if seen[ep.Credential] || len(ep.Credential) != 43 || ep.EntryId != id {
					t.Fatal("节点对凭据不独立")
				}
				seen[ep.Credential] = true
				forbidden["pair-"+ep.EntryId+ep.NodeId] = []byte(ep.Credential)
			}
		} else {
			if s.TunnelServer == nil || len(s.ExitGroups) != 0 {
				t.Fatal("出口未收到自己的身份或收到入口凭据")
			}
			forbidden["key-"+id] = s.TunnelServer.PrivateKeyPem
			key, _ := pem.Decode(s.TunnelServer.PrivateKeyPem)
			if key == nil {
				t.Fatal("未下发出口私钥")
			}
			forbidden["key-der-"+id] = key.Bytes
			if len(s.TunnelServer.Peers) != 2 {
				t.Fatal("出口收到多余/缺失节点对")
			}
		}
		var encrypted bool
		var stored []byte
		if err := p.db.QueryRow("SELECT snapshot,encrypted FROM node_snapshots WHERE node_id=?", id).Scan(&stored, &encrypted); err != nil || !encrypted {
			t.Fatalf("快照未加密: %v", err)
		}
		if _, err := p.secrets.(*sealedSecrets).open("node-snapshot:"+ids[4], stored); err == nil {
			t.Fatal("快照密文未绑定节点身份")
		}
	}
	message, err := p.controlMessage(context.Background(), ids[4])
	if err != nil || message.Snapshot != nil {
		t.Fatal("无关节点收到了隧道秘密")
	}
	raw := request("GET", "/api/tunnel-exits", nil, 200)
	publicData, _ := json.Marshal(raw)
	for _, secret := range forbidden {
		if bytes.Contains(publicData, secret) {
			t.Fatal("管理 API 回显完整秘密")
		}
	}
	path := filepath.Join(dir, "panel.db")
	assertNoPlaintext(t, path+"-wal", forbidden)
	assertNoPlaintext(t, path, forbidden)
	if _, err := p.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, path, forbidden)
	// 旧版不含秘密的明文快照仍可被新版本读取，首次修改后升级为加密格式。
	plain := &controlpb.ConfigSnapshot{Revision: 1}
	plain.Digest = forwarding.SnapshotDigest(plain)
	data, _ := proto.Marshal(plain)
	if _, err := p.db.Exec("INSERT INTO node_snapshots(node_id,snapshot,digest,encrypted) VALUES(?,?,?,0)", ids[4], data, plain.Digest); err != nil {
		t.Fatal(err)
	}
	message, err = p.controlMessage(context.Background(), ids[4])
	if err != nil || message.Snapshot.Digest != plain.Digest {
		t.Fatal("旧明文快照不兼容")
	}
}
