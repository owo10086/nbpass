package clusterharness_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"nbpass/internal/forwarding"
	"nbpass/internal/node"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type chainCacheCapture struct {
	node.ConfigStore
	mu     sync.Mutex
	writes [][]byte
}

func (s *chainCacheCapture) SaveConfig(ctx context.Context, b []byte) error {
	if e := s.ConfigStore.SaveConfig(ctx, b); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, append([]byte{}, b...))
	if len(s.writes) > 2 {
		s.writes = s.writes[len(s.writes)-2:]
	}
	return nil
}
func (s *chainCacheCapture) last(t *testing.T, checkpoint bool) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	i := len(s.writes) - 1
	if checkpoint {
		i--
	}
	if i < 0 {
		t.Fatal("missing actual persisted checkpoint")
	}
	return append([]byte{}, s.writes[i]...)
}
func chainActualOldBinary(t *testing.T) string {
	t.Helper()
	const commit = "3899928362a75b58ac74ff6844652d319760b656"
	root, e := filepath.Abs("../..")
	if e != nil {
		t.Fatal(e)
	}
	archive := exec.Command("git", "archive", commit, "api", "internal", "cmd", "go.mod", "go.sum")
	archive.Dir = root
	b, e := archive.Output()
	if e != nil {
		t.Fatal(e)
	}
	source := t.TempDir()
	extract := exec.Command("tar", "-xf", "-", "-C", source)
	extract.Stdin = bytes.NewReader(b)
	if out, e := extract.CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
	binary := filepath.Join(source, "nbpass-node-state-v1")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", binary, "./cmd/nbpass-node")
	build.Dir = source
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatal(string(out), e)
	}
	b, e = os.ReadFile(binary)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("actual historical node source=%s sha256=%x", commit, sha256.Sum256(b))
	return binary
}
func TestForwardChainActualOldReaderRejectsHTTPNodeCachesAndCheckpoints(t *testing.T) {
	stores := make([]node.FileStore, 3)
	captures := make([]*chainCacheCapture, 3)
	r := newChainRig(t, 1, []int{1, 1}, "primary_backup", func(i int, d *node.Dependencies) {
		stores[i] = d.Store.(node.FileStore)
		captures[i] = &chainCacheCapture{ConfigStore: stores[i]}
		d.ConfigStore = captures[i]
	})
	r.c.Do("PUT", "/api/nodes/"+r.ids[2]+"/outbound", map[string]string{"outbound_ipv4": "127.0.0.1"}, 200)
	awaitRule(t, r.c, r.rule, "正常")
	r.exchange(t, 0)
	finals := make([][]byte, 3)
	for i := range captures {
		finals[i] = captures[i].last(t, false)
		var cache struct {
			Version int              `json:"version"`
			State   forwarding.State `json:"state"`
		}
		if e := json.Unmarshal(finals[i], &cache); e != nil {
			t.Fatal(e)
		}
		if cache.Version != 2 {
			t.Fatal("HTTP node wrote old format")
		}
		if i == 1 && (len(cache.State.Rules) != 0 || len(cache.State.ExitGroups) != 1 || len(cache.State.TunnelGrantResults) != 1 || cache.State.TunnelServer.Grants[0].NextExitGroupId == "") {
			t.Fatal("relay-only cache lost actual chain state")
		}
		if i == 2 && cache.State.OutboundIPv4 != "127.0.0.1" {
			t.Fatal("terminal cache lost source binding")
		}
	}
	r.body["enabled"] = false
	r.c.Do("PUT", "/api/rules/"+r.rule, r.body, 200)
	awaitRule(t, r.c, r.rule, "正常")
	r.advance(t, 2)
	checkpoints := make([][]byte, 3)
	for i := range captures {
		checkpoints[i] = captures[i].last(t, true)
		r.nodes[i].Stop()
	}
	binary := chainActualOldBinary(t)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.h.Control.Certificate().Raw}), 0600); e != nil {
		t.Fatal(e)
	}
	for i := range stores {
		for _, checkpoint := range []bool{false, true} {
			name := []string{"entry", "relay_only", "terminal_source"}[i]
			if checkpoint {
				name += "_revocation_checkpoint"
			}
			t.Run(name, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "node")
				store := node.FileStore{Dir: dir, Random: rand.Reader}
				creds, e := stores[i].Load(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				if e = store.Save(context.Background(), creds); e != nil {
					t.Fatal(e)
				}
				raw := finals[i]
				if checkpoint {
					raw = checkpoints[i]
				}
				if e = store.SaveConfig(context.Background(), raw); e != nil {
					t.Fatal(e)
				}
				before := r.targetCalls[2].Load()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "--control-url", creds.ControlURL, "--data-dir", dir, "--ca-file", ca)
				out, e := cmd.CombinedOutput()
				if e == nil || !strings.Contains(string(out), "节点配置缓存格式或身份不匹配") {
					t.Fatal("actual old reader did not reject before Restore", e, string(out))
				}
				if r.targetCalls[2].Load() != before {
					t.Fatal("old reader reached target")
				}
				t.Logf("unchanged old executable: %v; rejected before listeners/control", e)
			})
		}
	}
}
