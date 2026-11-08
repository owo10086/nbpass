package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// 在真实 WAL 和主库中验证 Telegram 设置复用票 01 的加密秘密存储。
func TestTelegramCredentialsAreEncryptedInDatabaseAndWAL(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	const token = "123456:telegram-storage-secret-abcdefghijklmnop"
	const chat = "-100555566667777"
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "telegram-storage-password"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "telegram-storage-password"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	response := diskRequest(t, p, "PUT", "/api/settings/telegram", map[string]any{"enabled": true, "bot_token": token, "chat_id": chat}, cookie, 200)
	if strings.Contains(response.Body.String(), token) || strings.Contains(response.Body.String(), chat) {
		t.Fatal("保存响应泄露秘密")
	}
	data, err := p.Secrets().Get(context.Background(), "telegram.settings")
	var config telegramConfig
	if err != nil || json.Unmarshal(data, &config) != nil || config.Token != token || config.ChatID != chat {
		t.Fatal("秘密未正确保存并解密")
	}
	var ciphertext []byte
	if err := p.db.QueryRow("SELECT ciphertext FROM secrets WHERE name='telegram.settings'").Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	forbidden := map[string][]byte{"Bot token": []byte(token), "聊天目标": []byte(chat)}
	path := filepath.Join(dir, "panel.db")
	wal := assertNoPlaintext(t, path+"-wal", forbidden)
	assertStoredCredentialsPresent(t, wal, ciphertext)
	if _, err := p.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	database := assertNoPlaintext(t, path, forbidden)
	assertStoredCredentialsPresent(t, database, ciphertext)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openDiskPanel(t, dir)
	data, err = p.Secrets().Get(context.Background(), "telegram.settings")
	if err != nil || json.Unmarshal(data, &config) != nil || config.Token != token || config.ChatID != chat {
		t.Fatal("重建后秘密不可恢复")
	}
}

func TestOfflineMigrationPreservesVersionSevenData(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, migration := range migrations[:7] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{
		"INSERT INTO nodes(id,name,created_at,desired_revision,applied_revision) VALUES('kept','已有节点',0,7,7)",
		"INSERT INTO entry_groups(id,name) VALUES('g','已有入口组')",
		"INSERT INTO entry_group_members VALUES('g','kept',0)",
		"INSERT INTO events(node_id,source,event_id,type,occurred_at,received_at,config_revision,message) VALUES('kept','node','kept','sync_failed',1,2,7,'已有事件')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var count int
	if err := p.db.QueryRow("SELECT count(*) FROM entry_group_members m JOIN nodes n ON m.node_id=n.id JOIN events e ON e.node_id=n.id WHERE e.message='已有事件' AND n.desired_revision=7 AND n.applied_revision=7").Scan(&count); err != nil || count != 1 {
		t.Fatalf("旧配置/事件丢失: %d %v", count, err)
	}
	if _, err := p.db.Exec("INSERT INTO offline_incidents(node_id,offline_at) VALUES('kept',123)"); err != nil {
		t.Fatal(err)
	}
}
