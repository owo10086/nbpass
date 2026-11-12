package node

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"nbpass/internal/forwarding"
)

// 真实旧源基线：3899928362a75b58ac74ff6844652d319760b656。
// 后续源 IP / relay 票复用此旧读取器，不能把新版 StateCurrent 改为 1 来冒充旧版。
func TestActualV1ReaderRejectsNewlyPersistedCache(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "node")
	store := FileStore{Dir: dir, Random: rand.Reader}
	credential := Credentials{NodeID: "n", ControlURL: "https://panel.example"}
	state := forwarding.State{HighestRevision: 9, HighestDigest: strings.Repeat("a", 64), AppliedRevision: 7, AppliedDigest: strings.Repeat("b", 64)}
	if err := persistConfigCache(ctx, credential, store, state); err != nil {
		t.Fatal(err)
	}
	data, err := store.LoadConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cache configCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.Version != 2 {
		t.Fatalf("未实际写格式 2: %s %v", data, err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(cwd))
	fixture := filepath.Join(cwd, "testdata", "state-v1")
	overlay := map[string]any{"Replace": map[string]string{
		filepath.Join(cwd, "node.go"):                                           filepath.Join(fixture, "node.go"),
		filepath.Join(cwd, "config_rollback_test.go"):                           filepath.Join(fixture, "empty_test.go"),
		filepath.Join(cwd, "config_store.go"):                                   filepath.Join(fixture, "config_store.go"),
		filepath.Join(cwd, "config_store_test.go"):                              filepath.Join(fixture, "reader_test.go"),
		filepath.Join(root, "internal", "compatibility", "compatibility.go"):    filepath.Join(fixture, "compatibility.go"),
		filepath.Join(root, "internal", "nodeupdate", "rollback_probe_test.go"): filepath.Join(fixture, "updater_test.go"),
	}}
	raw, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "overlay.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "go", "test", "-overlay", path, "-vet=off", "./internal/node", "-run", "^TestRollbackOldReaderRejectsActualNewCache$", "-count=1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "NBPASS_ROLLBACK_CACHE_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("真实旧代码验证失败: %v\n%s", err, out)
	}
	// install.go is unchanged from the baseline; execute its actual candidate
	// check with the old compatibility implementation.
	cmd = exec.CommandContext(ctx, "go", "test", "-overlay", path, "-vet=off", "./internal/nodeupdate", "-run", "^TestActualOldUpdaterRejectsWriterTwo$", "-count=1")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("真实旧更新器验证失败: %v\n%s", err, out)
	}
}
