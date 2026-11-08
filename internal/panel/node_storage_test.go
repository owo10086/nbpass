package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"nbpass/internal/controlpb"
)

// 仅验证存储物理属性；业务状态与票据语义由 clusterharness 的 HTTP 测试验证。
func TestNodeTicketAndTokenAreDigestsAndNeverPlaintextOnDisk(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": "administrator-password-123"}, nil, 201)
	login := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": "administrator-password-123"}, nil, 200)
	cookie := login.Result().Cookies()[0]
	created := diskRequest(t, p, "POST", "/api/nodes", map[string]string{"name": "节点 A"}, cookie, 201)
	var installation struct {
		ID     string `json:"id"`
		Ticket string `json:"installation_ticket"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &installation); err != nil {
		t.Fatal(err)
	}
	var ticketHash []byte
	if err := p.db.QueryRow("SELECT ticket_hash FROM installation_tickets WHERE node_id = ?", installation.ID).Scan(&ticketHash); err != nil {
		t.Fatal(err)
	}
	expectedTicket := sha256.Sum256([]byte(installation.Ticket))
	if !bytes.Equal(ticketHash, expectedTicket[:]) {
		t.Fatal("安装票据未存储 SHA-256 摘要")
	}
	credential, err := (&controlService{p: p}).Enroll(context.Background(), &controlpb.EnrollRequest{InstallationTicket: installation.Ticket})
	if err != nil {
		t.Fatal(err)
	}
	var tokenHash []byte
	if err := p.db.QueryRow("SELECT token_hash FROM nodes WHERE id = ?", installation.ID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	expectedToken := sha256.Sum256([]byte(credential.Token))
	if len(credential.Token) != 43 || !bytes.Equal(tokenHash, expectedToken[:]) {
		t.Fatal("节点 token 未存储 SHA-256 摘要")
	}
	rawTicket, err := base64.RawURLEncoding.DecodeString(installation.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	rawToken, err := base64.RawURLEncoding.DecodeString(credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string][]byte{"ticket": []byte(installation.Ticket), "raw ticket": rawTicket, "token": []byte(credential.Token), "raw token": rawToken}
	path := filepath.Join(dir, "panel.db")
	wal := assertNoPlaintext(t, path+"-wal", forbidden)
	assertStoredCredentialsPresent(t, wal, ticketHash, tokenHash)
	assertNoPlaintext(t, path, forbidden)
	if _, err := p.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	assertStoredCredentialsPresent(t, assertNoPlaintext(t, path, forbidden), tokenHash)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, path, forbidden)
}
