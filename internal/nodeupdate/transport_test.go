package nodeupdate

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpdaterRequiresVerifiedTLSAndRejectsRedirects(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://other.invalid/", 302) }))
	defer server.Close()
	untrusted, _ := NewHTTPS(server.URL, nil, func(context.Context) (string, error) { return "token", nil })
	if _, err := untrusted.Manifest(context.Background(), "task"); err == nil {
		t.Fatal("接受不可信 TLS")
	}
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	trusted, _ := NewHTTPS(server.URL, roots, func(context.Context) (string, error) { return "token", nil })
	if _, err := trusted.Artifact(context.Background(), "task"); err == nil {
		t.Fatal("跟随了重定向")
	}
	if _, err := NewHTTPS("http://example.com", nil, nil); err == nil {
		t.Fatal("接受明文下载")
	}
}
func TestUpdaterHTTPRejectsPathsCommandsAndExtraJSON(t *testing.T) {
	manager, _ := OpenManager(context.Background(), "", nil, nil)
	handler := Handler(manager)
	m := manifest([]byte("data"))
	data, _ := json.Marshal(m)
	for _, body := range []string{strings.TrimSuffix(string(data), "}") + `,"path":"/tmp/evil"}`, strings.TrimSuffix(string(data), "}") + `,"command":"sh"}`, string(data) + ` {}`} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "/submit", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("未拒绝不允许的请求: %d", w.Code)
		}
	}
}
func TestManagerPersistsIdempotencyAndDoesNotRetryOnNodeRestart(t *testing.T) {
	data := []byte("new")
	m := manifest(data)
	source := SourceFuncs{ManifestFunc: func(context.Context, string) (Manifest, error) { return m, nil }, ArtifactFunc: func(context.Context, string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
	var calls atomic.Int32
	path := filepath.Join(t.TempDir(), "progress.json")
	manager, err := OpenManager(context.Background(), path, source, func(_ context.Context, _ Manifest, _ []byte, emit func(string)) error {
		calls.Add(1)
		emit("switching")
		emit("reconnecting")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Submit(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		p, _ := manager.Status(context.Background())
		if p.State == "reconnecting" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("未完成")
		}
		time.Sleep(time.Millisecond)
	}
	reopened, err := OpenManager(context.Background(), path, source, func(context.Context, Manifest, []byte, func(string)) error { calls.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Submit(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("同一任务被再次执行")
	}
	other := m
	other.SHA256 = strings.Repeat("a", 64)
	if _, err = reopened.Submit(context.Background(), other); err == nil {
		t.Fatal("允许同任务换产物")
	}
}
func TestRecoverRestoresInterruptedSwitchWithoutTouchingSecurityState(t *testing.T) {
	dir := realTemp(t)
	path := filepath.Join(dir, "nbpass-node")
	os.WriteFile(path, []byte("new"), 0700)
	work := filepath.Join(dir, ".nbpass-update")
	os.Mkdir(work, 0700)
	os.WriteFile(filepath.Join(work, "backup"), []byte("old"), 0700)
	os.WriteFile(filepath.Join(work, "pending.json"), []byte(`{}`), 0600)
	calls := 0
	f := &FixedInstaller{Path: path, allowedDir: dir, Restart: func(context.Context) error { calls++; return nil }}
	if err := f.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" || calls != 1 {
		t.Fatalf("恢复失败: %s calls=%d", got, calls)
	}
}

func TestInterruptedRollbackCanBeRetriedAfterOldServiceStartFailure(t *testing.T) {
	dir := realTemp(t)
	path := filepath.Join(dir, "nbpass-node")
	os.WriteFile(path, []byte("new"), 0755)
	work := filepath.Join(dir, ".nbpass-update")
	os.Mkdir(work, 0700)
	os.WriteFile(filepath.Join(work, "backup"), []byte("old"), 0755)
	os.WriteFile(filepath.Join(work, "pending.json"), []byte(`{}`), 0600)
	starts := 0
	f := &FixedInstaller{Path: path, allowedDir: dir, Restart: func(context.Context) error {
		starts++
		if starts == 1 {
			return errors.New("temporarily failed")
		}
		return nil
	}}
	if err := f.Recover(context.Background()); err == nil {
		t.Fatal("首次重启应失败")
	}
	if err := f.Recover(context.Background()); err != nil {
		t.Fatalf("回滚不允许重试: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" || starts != 2 {
		t.Fatal("未恢复旧程序")
	}
}
