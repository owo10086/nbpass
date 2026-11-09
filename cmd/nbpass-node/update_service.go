package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"nbpass/internal/node"
	"nbpass/internal/nodeupdate"
)

const updateStateDir = "/var/lib/nbpass-update"
const healthPath = "/run/nbpass-node/health.json"

type localHealth struct {
	PID      int       `json:"pid"`
	Instance string    `json:"instance"`
	Version  string    `json:"version"`
	SHA256   string    `json:"sha256"`
	Ready    bool      `json:"ready"`
	At       time.Time `json:"at"`
}

func executableSHA() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
func writeLocalHealth() func(string, string, bool) {
	hash := executableSHA()
	return func(instance, version string, ready bool) {
		// A stale health record is never sufficient: updater also verifies the systemd MainPID.
		data, _ := json.Marshal(localHealth{PID: os.Getpid(), Instance: instance, Version: version, SHA256: hash, Ready: ready, At: time.Now().UTC()})
		f, err := os.CreateTemp(filepath.Dir(healthPath), ".health-*")
		if err != nil {
			return
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(data); err != nil {
			f.Close()
			return
		}
		if err = f.Close(); err == nil {
			_ = os.Rename(f.Name(), healthPath)
		}
	}
}
func fixedSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/systemctl", args...).Output()
}
func restartNode(ctx context.Context) error {
	_, err := fixedSystemctl(ctx, "restart", "nbpass-node.service")
	return err
}
func candidateCheck(ctx context.Context, path string) (nodeupdate.Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "-update-check").Output()
	if err != nil {
		return nodeupdate.Identity{}, err
	}
	if len(output) > 4096 {
		return nodeupdate.Identity{}, errors.New("候选自检响应过大")
	}
	var i nodeupdate.Identity
	err = json.Unmarshal(output, &i)
	return i, err
}
func waitLocalHealth(m nodeupdate.Manifest) func(context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		for {
			raw, err := os.ReadFile(healthPath)
			var h localHealth
			if err == nil && json.Unmarshal(raw, &h) == nil && h.Ready && h.Version == m.Version && h.SHA256 == m.SHA256 && time.Since(h.At) >= 0 && time.Since(h.At) < 5*time.Second {
				pid, err := fixedSystemctl(ctx, "show", "--property=MainPID", "--value", "nbpass-node.service")
				var n int
				if err == nil {
					fmt.Sscanf(string(pid), "%d", &n)
				}
				// Bind health to the actual running image, including the case of equal software versions.
				if n > 0 && h.PID == n {
					image, err := os.ReadFile(fmt.Sprintf("/proc/%d/exe", n))
					if err == nil {
						sum := sha256.Sum256(image)
						if hex.EncodeToString(sum[:]) == m.SHA256 {
							return nil
						}
					}
				}
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return errors.New("新程序未在 3 分钟内通过本地业务自检")
			case <-timer.C:
			}
		}
	}
}
func runUpdateService(ctx context.Context, origin string, roots *x509.CertPool) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return errors.New("更新服务仅支持 Linux amd64")
	}
	for _, dir := range []string{updateStateDir, filepath.Dir(nodeupdate.SocketPath)} {
		mode := os.FileMode(0700)
		if dir == filepath.Dir(nodeupdate.SocketPath) {
			mode = 0750
		}
		if err := os.MkdirAll(dir, mode); err != nil {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm() != mode {
			return errors.New("更新服务目录必须为权限 0700 的真实目录")
		}
	}
	store := node.FileStore{Dir: "/var/lib/nbpass-node", Random: rand.Reader}
	source, err := nodeupdate.NewHTTPS(origin, roots, func(ctx context.Context) (string, error) { c, e := store.Load(ctx); return c.Token, e })
	if err != nil {
		return err
	}
	installer := &nodeupdate.FixedInstaller{Path: nodeupdate.InstallPath, Check: candidateCheck, Restart: restartNode}
	if err = installer.Recover(ctx); err != nil {
		return err
	}
	apply := func(ctx context.Context, m nodeupdate.Manifest, data []byte, emit func(string)) error {
		installer.Healthy = waitLocalHealth(m)
		return installer.Apply(ctx, m, data, emit)
	}
	manager, err := nodeupdate.OpenManager(ctx, filepath.Join(updateStateDir, "progress.json"), source, apply)
	if err != nil {
		return err
	}
	defer manager.Wait()
	if info, err := os.Lstat(nodeupdate.SocketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("更新服务路径不是 socket")
		}
		if err = os.Remove(nodeupdate.SocketPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", nodeupdate.SocketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(nodeupdate.SocketPath)
	if err = os.Chmod(nodeupdate.SocketPath, 0660); err != nil {
		return err
	}
	server := &http.Server{Handler: nodeupdate.Handler(manager), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	go func() { <-ctx.Done(); server.Close() }()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
