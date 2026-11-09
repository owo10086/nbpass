package panel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"nbpass/internal/platform"
)

func TestPanelPersistsCurrentMigrationVersion(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		p := openDiskPanel(t, dir)
		var version int
		if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != len(migrations) {
			t.Errorf("user_version = %d, want current migration %d", version, len(migrations))
		}
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPanelRejectsNewerDatabaseWithoutChangingVersion(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	newer := len(migrations) + 1
	if _, err := p.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", newer)); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	core, err := Open(context.Background(), Config{PublicOrigin: "https://panel.test"}, diskDependencies(dir))
	if core != nil {
		core.Close()
	}
	if err == nil || core != nil {
		t.Fatal("panel accepted a database from a newer migration")
	}
	db, err := (platform.SQLite{Path: filepath.Join(dir, "panel.db")}).Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != newer {
		t.Errorf("rejected database was changed: user_version = %d, want %d", version, newer)
	}
}

func TestPanelDatabaseAndWALNeverContainPlaintextCredentials(t *testing.T) {
	dir := t.TempDir()
	p := openDiskPanel(t, dir)
	const password = "administrator-plaintext-marker-01"
	const name = "credential"
	values := []string{"secret-plaintext-marker-before-update", "secret-plaintext-marker-after-update"}
	diskRequest(t, p, "POST", "/api/auth/setup", map[string]string{"password": password}, nil, 201)
	response := diskRequest(t, p, "POST", "/api/auth/login", map[string]string{"password": password}, nil, 200)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || len(cookies[0].Value) != 43 {
		t.Fatal("login did not return a session token")
	}
	cookie := cookies[0]
	rawToken, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(rawToken) != 32 {
		t.Fatal("session token is not 32 bytes")
	}
	for _, value := range values {
		diskRequest(t, p, "PUT", "/api/secrets/"+name, map[string]string{"value": value}, cookie, 200)
	}
	var hash string
	if err := p.db.QueryRow("SELECT password_hash FROM administrator WHERE id = 1").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != 12 {
		t.Fatalf("stored password hash: cost %d, error %v; want 12", cost, err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		t.Fatal("stored password hash does not verify")
	}
	var storedDigest, ciphertext []byte
	if err := p.db.QueryRow("SELECT token_hash FROM sessions").Scan(&storedDigest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(cookie.Value))
	if !bytes.Equal(storedDigest, digest[:]) {
		t.Fatal("session must store the token digest")
	}
	if err := p.db.QueryRow("SELECT ciphertext FROM secrets WHERE name = ?", name).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) == 0 {
		t.Fatal("secret ciphertext is missing")
	}
	forbidden := map[string][]byte{
		"administrator password": []byte(password),
		"session cookie":         []byte(cookie.Value),
		"raw session bytes":      rawToken,
		"original secret":        []byte(values[0]),
		"updated secret":         []byte(values[1]),
	}
	path := filepath.Join(dir, "panel.db")
	// 正向对照证明实际凭据已经写入被扫描的 WAL，避免空文件令断言虚假通过。
	wal := assertNoPlaintext(t, path+"-wal", forbidden)
	assertStoredCredentialsPresent(t, wal, []byte(hash), storedDigest, ciphertext)
	assertNoPlaintext(t, path, forbidden)
	var busy, pages, checkpointed int
	if err := p.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &pages, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatal("database checkpoint was busy")
	}
	// 检查 checkpoint 后的主库，覆盖从 WAL 搬运到数据库文件的产物。
	database := assertNoPlaintext(t, path, forbidden)
	assertStoredCredentialsPresent(t, database, []byte(hash), storedDigest, ciphertext)
	assertNoPlaintext(t, path+"-wal", forbidden)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoPlaintext(t, path, forbidden)
	p = openDiskPanel(t, dir)
	value, err := p.Secrets().Get(context.Background(), name)
	if err != nil || string(value) != values[1] {
		t.Fatalf("persisted secret did not decrypt after reopening: %v", err)
	}
}

func diskDependencies(dir string) Dependencies {
	return Dependencies{
		Clock:       platform.RealClock{},
		Persistence: platform.SQLite{Path: filepath.Join(dir, "panel.db")},
		KeyFile:     platform.LocalKeyFile{Path: filepath.Join(dir, "panel.key")},
		Random:      rand.Reader,
	}
}

func openDiskPanel(t *testing.T, dir string) *Panel {
	t.Helper()
	p, err := Open(context.Background(), Config{PublicOrigin: "https://panel.test"}, diskDependencies(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	return p
}

func diskRequest(t *testing.T, p *Panel, method, path string, body any, cookie *http.Cookie, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "https://panel.test"+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://panel.test")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	p.Handler().ServeHTTP(response, req)
	if response.Code != wantStatus {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.Code, wantStatus, response.Body)
	}
	return response
}

func assertNoPlaintext(t *testing.T, path string, forbidden map[string][]byte) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, plaintext := range forbidden {
		if bytes.Contains(data, plaintext) {
			t.Errorf("%s contains plaintext %s", filepath.Base(path), name)
		}
	}
	return data
}

func assertStoredCredentialsPresent(t *testing.T, data []byte, credentials ...[]byte) {
	t.Helper()
	for i, credential := range credentials {
		if !bytes.Contains(data, credential) {
			t.Errorf("scanned file does not contain persisted credential %d", i)
		}
	}
}

// 从 main 的完整迁移链升级，入口手动切换追加于其后，不改已有迁移序号。
func TestEntryManualSwitchMigrationPreservesExistingMainData(t *testing.T) {
	dir := t.TempDir()
	db, err := diskDependencies(dir).Persistence.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for version, migration := range migrations[:len(migrations)-1] {
		if _, err := db.Exec(migration); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", version+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO entry_groups(id,name) VALUES('g','既有入口组')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	p := openDiskPanel(t, dir)
	var version, paused int
	var name string
	if err := p.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := p.db.QueryRow("SELECT name,auto_failback_paused FROM entry_groups WHERE id='g'").Scan(&name, &paused); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) || name != "既有入口组" || paused != 0 {
		t.Fatalf("升级丢失入口组或暂停默认值错误: version=%d name=%s paused=%d", version, name, paused)
	}
	if _, err := p.db.Exec("UPDATE entry_groups SET auto_failback_paused=1 WHERE id='g'"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = openDiskPanel(t, dir)
	if err := p.db.QueryRow("SELECT auto_failback_paused FROM entry_groups WHERE id='g'").Scan(&paused); err != nil || paused != 1 {
		t.Fatalf("迁移后重启丢失暂停状态: %d %v", paused, err)
	}
}
