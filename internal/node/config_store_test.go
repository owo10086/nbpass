package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/controlpb"
	"nbpass/internal/forwarding"
)

// 物理属性：缓存必须加密、原子替换、检查权限及 AAD，不接受损坏或过大的文件。
func TestConfigStoreEncryptsAndRejectsUnsafeOrCorruptedFiles(t *testing.T) {
	ctx := context.Background()
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	plain := []byte(`{"target":"secret-target.example:443"}`)
	if err := store.SaveConfig(ctx, plain); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir, "config.enc")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("secret-target")) {
		t.Fatal("实际配置明文落盘")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("配置文件权限不是 0600")
	}
	reopened := FileStore{Dir: store.Dir, Random: rand.Reader}
	if loaded, err := reopened.LoadConfig(ctx); err != nil || !bytes.Equal(plain, loaded) {
		t.Fatalf("配置重开失败: %s %v", loaded, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(ctx); err == nil {
		t.Fatal("接受不安全的配置权限")
	}
	if err := store.SaveConfig(ctx, plain); err == nil {
		t.Fatal("覆盖不安全权限文件")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(ctx); err == nil {
		t.Fatal("接受损坏配置密文")
	}
	if err := store.SaveEvents(ctx, plain); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(store.Dir, "events.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(ctx); err == nil {
		t.Fatal("事件密文冒充配置缓存")
	}
	if err := os.WriteFile(path, make([]byte, (2<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(ctx); err == nil {
		t.Fatal("接受超大配置文件")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, plain, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadConfig(ctx); err == nil {
		t.Fatal("跟随配置符号链接")
	}
	if err := store.SaveConfig(ctx, plain); err == nil {
		t.Fatal("覆盖配置符号链接")
	}
}

func TestConfigCacheRejectsUnknownFormatAndDifferentIdentity(t *testing.T) {
	ctx := context.Background()
	store := FileStore{Dir: filepath.Join(t.TempDir(), "node"), Random: rand.Reader}
	credential := Credentials{ControlURL: "https://control.example", NodeID: "node-a"}
	snapshot := &controlpb.ConfigSnapshot{Revision: 7}
	snapshot.Digest = forwarding.SnapshotDigest(snapshot)
	state := forwarding.State{HighestRevision: 7, HighestDigest: snapshot.Digest, AppliedRevision: 7, AppliedDigest: snapshot.Digest}
	for _, cache := range []configCache{
		{Version: 2, NodeID: credential.NodeID, ControlURL: credential.ControlURL, State: state},
		{Version: 1, NodeID: "node-b", ControlURL: credential.ControlURL, State: state},
		{Version: 1, NodeID: credential.NodeID, ControlURL: "https://other.example", State: state},
	} {
		data, err := json.Marshal(cache)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveConfig(ctx, data); err != nil {
			t.Fatal(err)
		}
		engine := forwarding.NewEngine(forwarding.Dependencies{})
		if err := openConfigCache(ctx, credential, &Dependencies{ConfigStore: store}, engine); err == nil {
			engine.Close()
			t.Fatal("接受不兼容格式或其他节点/面板的配置")
		}
		engine.Close()
	}
}

func TestManyRuleErrorsFitReportWithoutLosingFailuresOrBreakingUTF8(t *testing.T) {
	report := &controlpb.NodeReport{ProtocolVersion: 1, NodeId: strings.Repeat("n", 43), ProcessInstance: strings.Repeat("p", 43), AttemptedRevision: 5, AttemptedDigest: strings.Repeat("a", 64), ApplyError: strings.Repeat("错", 300)}
	for range 600 {
		report.RuleResults = append(report.RuleResults, &controlpb.RuleApplyResult{RuleId: strings.Repeat("r", 43), ApplyError: "端口 12345 监听失败: " + strings.Repeat("失败😀", 200)})
	}
	if err := limitConfigReport(report); err != nil {
		t.Fatal(err)
	}
	if size := proto.Size(report); size > 64<<10 {
		t.Fatalf("配置结果超过 gRPC 上限: %d", size)
	}
	for _, result := range report.RuleResults {
		if !strings.HasPrefix(result.ApplyError, "端口 12345 监听失败") || !utf8.ValidString(result.ApplyError) || strings.ContainsRune(result.ApplyError, '\ufffd') {
			t.Fatal("错误被漏报或截断 UTF-8")
		}
	}
}
