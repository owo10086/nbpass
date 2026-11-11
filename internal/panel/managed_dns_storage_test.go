package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

// 物理存储属性白盒：真实 DNSPod 凭据必须进入已有的加密秘密存储。
func TestDNSPodCredentialsEncryptedInDatabaseAndWAL(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	const secretID = "AKID4242424242424242"
	const secretKey = "dnspodPlaintextSecretMarkerPhysicalTest"
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "test-password-dnspod"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "test-password-dnspod"}, nil, 200)
	cookies := login.Result().Cookies()
	response := diskRequest(t, p, "POST", "/api/dns-accounts", map[string]string{"name": "DNSPod", "secret_id": secretID, "secret_key": secretKey}, cookies[0], 201)
	var account dnsAccount
	if err := json.Unmarshal(response.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	var ciphertext []byte
	if err := p.db.QueryRow("SELECT ciphertext FROM secrets WHERE name=?", "dnspod."+account.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(secretKey)) || len(ciphertext) == 0 {
		t.Fatal("DNSPod 凭据未加密")
	}
	forbidden := map[string][]byte{"SecretId": []byte(secretID), "SecretKey": []byte(secretKey)}
	path := filepath.Join(dir, "panel.db")
	wal := assertNoPlaintext(t, path+"-wal", forbidden)
	assertStoredCredentialsPresent(t, wal, ciphertext)
	var busy, pages, checkpointed int
	if err := p.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &pages, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatal("checkpoint 未完成")
	}
	assertStoredCredentialsPresent(t, assertNoPlaintext(t, path, forbidden), ciphertext)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openDiskPanel(t, dir)
	credentials, err := p.dnsCredential(context.Background(), account.ID)
	if err != nil || credentials.SecretID != secretID || credentials.SecretKey != secretKey {
		t.Fatalf("重启无法解密 DNSPod 凭据: %v", err)
	}
}
