package node

import (
	"context"
	"crypto/rand"
	"errors"
	"nbpass/internal/platform"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

	var controls atomic.Int32
	deps := Dependencies{Clock: platform.RealClock{}, Store: store, ConfigStore: store, Random: rand.Reader, DialContext: func(context.Context, string) (net.Conn, error) {
		controls.Add(1)
		return nil, errors.New("control intentionally offline")
	}, Forwarding: forwarding.Dependencies{CheckListen: func(network, _ string) (net.Listener, error) { return net.Listen(network, "127.0.0.1:0") }}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = Run(ctx, Config{ControlURL: "https://panel.example"}, deps)
	if err == nil || !strings.Contains(err.Error(), "节点配置缓存格式或身份不匹配") || controls.Load() != 0 {
		t.Fatalf("old Run did not exit before control: %v connections=%d", err, controls.Load())
	}
	assertNoBusiness := func() {
		t.Helper()
		c, err := net.DialTimeout("tcp4", "127.0.0.1:"+os.Getenv("NBPASS_ROLLBACK_BUSINESS_PORT"), 100*time.Millisecond)
		if err == nil {
			c.Close()
			t.Fatal("old business listener running")
		}
	}
	assertNoBusiness()
	original, err := os.ReadFile(filepath.Join(store.Dir, "config.enc"))
	if err != nil {
		t.Fatal(err)
	}
	isolated := filepath.Join(store.Dir, "config.enc.isolated")
	if err = os.Rename(filepath.Join(store.Dir, "config.enc"), isolated); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if err = Run(ctx2, Config{ControlURL: "https://panel.example"}, deps); err != nil {
		t.Fatal("isolated offline empty startup", err)
	}
	assertNoBusiness()
	preserved, err := os.ReadFile(isolated)
	if err != nil || string(preserved) != string(original) {
		t.Fatal("manual isolation altered cache", err)
	}
	if _, err = os.Stat(filepath.Join(store.Dir, "config.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty startup created fallback config", err)
	}
	t.Logf("old Run returned format/identity rejection; isolated offline Run returned nil, control attempts=%d, no business listener", controls.Load())
}
