package node

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"nbpass/internal/forwarding"
)

// 此测试由新版用 Go overlay 注入；读取器与格式常量是固定提交的原始源码。
func TestRollbackOldReaderRejectsActualNewCache(t *testing.T) {
	engine := forwarding.NewEngine(forwarding.Dependencies{})
	defer engine.Close()
	store := FileStore{Dir: os.Getenv("NBPASS_ROLLBACK_CACHE_DIR"), Random: rand.Reader}
	err := openConfigCache(context.Background(), Credentials{NodeID: "n", ControlURL: "https://panel.example"}, &Dependencies{ConfigStore: store}, engine)
	if err == nil || !strings.Contains(err.Error(), "节点配置缓存格式或身份不匹配") {
		t.Fatalf("真实旧读取器未在 Restore 前拒绝格式 2: %v", err)
	}
	if engine.State().HighestRevision != 0 || len(engine.State().Rules) != 0 {
		t.Fatal("拒绝前进入了 Restore")
	}
}
