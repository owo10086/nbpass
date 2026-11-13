package clusterharness_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"nbpass/internal/clusterharness"
	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
	"nbpass/internal/node"
	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

// This is a real slice-02 executable, never today's implementation with a removed declaration.
// Fresh clones can rebuild the fixed historical source without checking out a branch/worktree.
func ix02Baseline(t *testing.T) string {
	t.Helper()
	const commit = "946783879ab679baba2299704e75fcf398a86d7e"
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "bin", "nbpass-node-ix02")
	want := ""
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		want = "bed243861b9d6a25657cbf1a016a7c85ed29cc334491464c5f76cacc1cb03058"
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		binary += "-linux-amd64"
		want = "ef3ad98e58b6ed50b048f5604e7ad351e8b6f2725e4e1babfa4f41f353551b81"
	}
	if data, err := os.ReadFile(binary); err == nil && want != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatal("真实02基线哈希改变")
		}
		t.Logf("真实02节点 source=%s sha256=%s", commit, want)
		return binary
	}
	source := t.TempDir()
	archive := exec.Command("git", "archive", commit, "api", "internal", "cmd", "go.mod", "go.sum")
	archive.Dir = root
	data, err := archive.Output()
	if err != nil {
		t.Fatal("导出真实02源码", err)
	}
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(source, header.Name)
		if !filepath.IsLocal(header.Name) {
			t.Fatal("非法源码路径")
		}
		if header.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			t.Fatal("非普通源码文件", header.Name)
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(file, reader)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	binary = filepath.Join(source, "nbpass-node-ix02")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", binary, "./cmd/nbpass-node")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建真实02源码: %s %v", out, err)
	}
	t.Logf("从真实02源码 %s 构建节点", commit)
	return binary
}

func TestIXMemberReal02NodePublicCompatibilityAndUpgradeConfirmation(t *testing.T) {
	binary := ix02Baseline(t)
	identity, err := exec.Command(binary, "--update-check").CombinedOutput()
	if err != nil || !bytes.Contains(identity, []byte(compatibility.IXAddressDialing)) || bytes.Contains(identity, []byte(compatibility.IXMemberIdentity)) {
		t.Fatal("基线不是实际02能力", string(identity), err)
	}
	h := clusterharness.New(t, func(d *panel.Dependencies) { d.Passwords = nodePasswords{}; d.Clock = platform.RealClock{} })
	c := h.Client()
	setup(c)
	login(c)
	a, b, backup := addNode(c, "真实02入口"), addNode(c, "公网主端点"), addNode(c, "专线备用端点")
	aid, bid, cid := a["id"].(string), b["id"].(string), backup["id"].(string)
	reserve, listen := directPorts(t)
	// The external process owns its sockets, so release reservations immediately before its binds.
	freePort := func() int {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return port
	}
	c.Do("PUT", "/api/nodes/"+aid+"/listener-ports", map[string]int{"tunnel_listen_port": freePort(), "check_port": freePort()}, 200)
	for _, n := range []map[string]any{b, backup} {
		h.StartNode(n["installation_ticket"].(string), listen, func(_ *node.Config, d *node.Dependencies) { d.Clock = platform.RealClock{} })
		awaitNode(t, c, n["id"].(string), connected)
	}
	bport, cport := reserve(), reserve()
	saved := c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": "127.0.0.1", "listen_port": bport, "transport": "wss"}, 200)
	c.Do("PUT", "/api/nodes/"+cid+"/tunnel", map[string]any{"address": "localhost", "listen_port": cport, "transport": "wss"}, 200)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.Control.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--control-url", h.Control.URL, "--ticket", a["installation_ticket"].(string), "--data-dir", filepath.Join(dir, "node"), "--ca-file", ca)
	// Write no application log file; only capture process diagnostics in memory after it exits.
	var processOutput bytes.Buffer
	cmd.Stdout, cmd.Stderr = &processOutput, &processOutput
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cmd.Process.Signal(os.Interrupt)
			cmd.Wait()
			if t.Failed() {
				t.Log(processOutput.String())
			}
		})
	}
	t.Cleanup(stop)
	var mu sync.Mutex
	latest := map[string]*controlpb.ConfigSnapshot{}
	gate := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(gate) })
	var holdBackup bool
	h.BeforeControlSend(func(ctx context.Context, id string, m *controlpb.PanelMessage) error {
		if m.Snapshot == nil {
			return nil
		}
		mu.Lock()
		latest[id] = proto.Clone(m.Snapshot).(*controlpb.ConfigSnapshot)
		hold := holdBackup && id == cid && len(m.Snapshot.GetTunnelServer().GetGrants()) > 0
		mu.Unlock()
		if hold {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-gate:
			}
		}
		return nil
	})
	awaitNode(t, c, aid, connected)
	g := c.Do("POST", "/api/exit-groups", map[string]any{"name": "公网组", "strategy": "primary_backup", "members": []string{bid}}, 201)["id"].(string)
	port := freePort()
	body := directBody(entryGroup(c, aid)["id"].(string), port, "127.0.0.1", echoTarget(t, "02:"))
	body["kind"], body["exit_group_id"] = "tunnel", g
	rule := c.Do("POST", "/api/rules", body, 201)["id"].(string)
	mapped, count := ixMappedProxy(t, bport)
	for _, mode := range []string{"ipv4", "domain", "mapped"} {
		t.Run(mode, func(t *testing.T) {
			address := "127.0.0.1"
			var external any
			if mode != "ipv4" {
				address = "localhost"
			}
			if mode == "mapped" {
				external = mapped
			}
			c.Do("PUT", "/api/nodes/"+bid+"/tunnel", map[string]any{"address": address, "listen_port": bport, "public_port": external, "transport": "wss"}, 200)
			awaitRule(t, c, rule, "正常")
			tcpExchange(t, port, "02:")
			mu.Lock()
			snapshot := proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot)
			mu.Unlock()
			for _, group := range snapshot.ExitGroups {
				for _, ep := range group.Exits {
					if ep.AddressId != "" {
						t.Fatal("给真实02节点下发身份字段280", ep)
					}
					if ep.EndpointConfigDigest == "" {
						t.Fatal("02摘要被丢弃")
					}
				}
			}
		})
	}
	if count.Load() == 0 {
		t.Fatal("真实02节点未走映射代理")
	}
	// Hold the backup grant before adding it. Candidate authorization still waits
	// for durable confirmation across a real old-process upgrade.
	mu.Lock()
	holdBackup = true
	mu.Unlock()
	c.Do("PUT", "/api/exit-groups/"+g, map[string]any{"name": "主备组", "strategy": "primary_backup", "members": []string{bid, cid}}, 200)
	awaitRule(t, c, rule, "正常")
	tcpExchange(t, port, "02:")
	mu.Lock()
	old := proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot)
	mu.Unlock()
	for _, group := range old.ExitGroups {
		for _, ep := range group.Exits {
			if ep.AddressId != "" {
				t.Fatal("真实02收到了新地址身份字段", ep)
			}
		}
	}
	// Upgrade the actual process, preserving its credentials. C's new grant delivery is held before receipt.
	mu.Lock()
	holdBackup = true
	mu.Unlock()
	stop()
	h.StartNode("", func(_ *node.Config, d *node.Dependencies) {
		d.Clock = platform.RealClock{}
		d.Store = node.FileStore{Dir: filepath.Join(dir, "node"), Random: rand.Reader}
	})
	awaitNode(t, c, aid, connected)
	awaitRule(t, c, rule, "正常")
	tcpExchange(t, port, "02:")
	mu.Lock()
	upgraded := proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot)
	mu.Unlock()
	found := false
	for _, r := range upgraded.Rules {
		if r.Id == rule {
			found = true
			if len(r.AuthorizedExitIds) != 1 || r.AuthorizedExitIds[0] != bid {
				t.Fatal("备用未落盘确认已获候选授权", r)
			}
		}
	}
	if !found {
		t.Fatal("升级误撤正常主路径")
	}
	release.Do(func() { close(gate) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		current := proto.Clone(latest[aid]).(*controlpb.ConfigSnapshot)
		mu.Unlock()
		ready := false
		for _, r := range current.Rules {
			if r.Id == rule && len(r.AuthorizedExitIds) == 2 {
				ready = true
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("备用新授权确认后未加入候选")
		}
		time.Sleep(time.Millisecond)
	}
	if saved["server_name"] == "" || !strings.Contains(string(identity), "ix_address_dialing") {
		t.Fatal("地址配置丢失服务器身份")
	}
}
