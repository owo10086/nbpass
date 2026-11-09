package panel

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The real shell scripts run against a private filesystem. Only Linux/root,
// account management, HTTPS distribution and systemd are replaced here.
// Actual TLS enrollment remains covered by the HTTP cluster harness.
type installSandbox struct {
	t      *testing.T
	root   string
	script string
	env    []string
}

func newInstallSandbox(t *testing.T, node bool) *installSandbox {
	t.Helper()
	s := &installSandbox{t: t, root: t.TempDir()}
	var source string
	if node {
		p := &Panel{config: Config{PublicOrigin: "https://panel.example.com"}}
		w := httptest.NewRecorder()
		p.installNode(w, httptest.NewRequest("GET", "/install-node.sh", nil))
		source = w.Body.String()
	} else {
		data, err := os.ReadFile("../../scripts/install-panel.sh")
		if err != nil {
			t.Fatal(err)
		}
		source = string(data)
	}
	for _, dir := range []string{"/usr/local", "/var/lib", "/etc/systemd/system", "/etc/nbpass", "/run"} {
		source = strings.ReplaceAll(source, dir, s.root+dir)
	}
	s.script = s.root + "/installer.sh"
	s.write("/installer.sh", source, 0700)
	s.write("/mock/uname", "#!/bin/sh\ncase $1 in -s) echo Linux;; -m) echo x86_64;; esac\n", 0700)
	s.write("/mock/id", "#!/bin/sh\n[ \"$1\" != -u ] || echo 0\n", 0700)
	s.write("/mock/useradd", "#!/bin/sh\nexit 0\n", 0700)
	s.write("/mock/chown", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_ROOT/chown.calls\"\n", 0700)
	s.write("/mock/systemctl", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_ROOT/systemctl.calls\"\n", 0700)
	// stat's root-owner check is the one physical property unavailable to an
	// unprivileged macOS test. File modes themselves are checked on real files.
	s.write("/mock/stat", "#!/bin/sh\ncase $2 in %u) echo 0;; *) exit 1;; esac\n", 0700)
	s.write("/mock/curl", "#!/bin/sh\n[ \"${FAIL_DOWNLOAD:-0}\" = 0 ] || exit 22\nwhile [ $# -gt 0 ]; do\n if [ \"$1\" = -o ]; then cp \"$TEST_ROOT/download\" \"$2\"; exit; fi\n shift\ndone\nexit 1\n", 0700)
	if _, err := exec.LookPath("sha256sum"); err != nil {
		// macOS ships shasum rather than GNU sha256sum; use the same real
		// SHA256 check-file operation without requiring a developer install.
		shasum, err := exec.LookPath("shasum")
		if err != nil {
			t.Fatal(err)
		}
		s.write("/mock/sha256sum", "#!/bin/sh\nexec "+shellQuote(shasum)+" -a 256 \"$@\"\n", 0700)
	}
	s.write("/download", "#!/bin/sh\n[ \"${FAIL_ENROLL:-0}\" = 0 ] || exit 1\nwhile [ $# -gt 0 ]; do\n case $1 in -data-dir) shift; data_dir=$1;; esac\n shift\ndone\n[ -f \"$data_dir/node.key\" ] || printf key > \"$data_dir/node.key\"\nprintf credential > \"$data_dir/credentials.enc\"\nchmod 600 \"$data_dir/node.key\" \"$data_dir/credentials.enc\"\n", 0700)
	for _, dir := range []string{"/usr/local/bin", "/usr/local/libexec", "/etc/systemd/system", "/var/lib", "/run"} {
		if err := os.MkdirAll(s.root+dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	s.env = append(os.Environ(), "PATH="+s.root+"/mock:"+os.Getenv("PATH"), "TEST_ROOT="+s.root)
	return s
}

func (s *installSandbox) write(path, value string, mode os.FileMode) {
	s.t.Helper()
	p := s.root + path
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(value), mode); err != nil {
		s.t.Fatal(err)
	}
}

func (s *installSandbox) run(wantOK bool, args ...string) {
	s.t.Helper()
	c := exec.Command("sh", append([]string{s.script}, args...)...)
	c.Env = s.env
	out, err := c.CombinedOutput()
	if (err == nil) != wantOK {
		s.t.Fatalf("installer success=%v, want %v: %v\n%s", err == nil, wantOK, err, out)
	}
}

func (s *installSandbox) contents(path string) string {
	s.t.Helper()
	b, err := os.ReadFile(s.root + path)
	if err != nil {
		s.t.Fatal(err)
	}
	return string(b)
}

func (s *installSandbox) mode(path string, want os.FileMode) {
	s.t.Helper()
	info, err := os.Stat(s.root + path)
	if err != nil || info.Mode().Perm() != want {
		s.t.Fatalf("%s: info=%v err=%v; want mode %o", path, info, err, want)
	}
}

func TestPanelInstallerRepeatPreservesDataAndConfiguration(t *testing.T) {
	s := newInstallSandbox(t, false)
	s.write("/panel-binary", "#!/bin/sh\nexit 0\n", 0700)
	s.run(true, "-binary", s.root+"/panel-binary", "-public-origin", "https://panel.example.com", "-control-url", "https://control.example.com")
	s.write("/var/lib/nbpass/panel.key", "original-key", 0600)
	s.write("/var/lib/nbpass/panel.db", "original-db", 0600)
	s.write("/etc/nbpass/panel.env", "NBPASS_PUBLIC_ORIGIN=https://changed.example.com\nNBPASS_CONTROL_URL=https://control.example.com\n", 0640)
	s.write("/etc/systemd/system/nbpass.service", s.contents("/etc/systemd/system/nbpass.service")+"# local configuration\n", 0644)
	config := s.contents("/etc/nbpass/panel.env")
	unit := s.contents("/etc/systemd/system/nbpass.service")
	s.run(true, "-binary", s.root+"/panel-binary")
	if s.contents("/var/lib/nbpass/panel.key") != "original-key" || s.contents("/var/lib/nbpass/panel.db") != "original-db" || s.contents("/etc/nbpass/panel.env") != config || s.contents("/etc/systemd/system/nbpass.service") != unit {
		t.Fatal("repeat installation changed existing data or configuration")
	}
	s.mode("/etc/nbpass", 0750)
	s.mode("/etc/nbpass/panel.env", 0640)
	s.mode("/var/lib/nbpass", 0700)
	s.mode("/usr/local/bin/nbpass", 0755)
	if !strings.Contains(s.contents("/systemctl.calls"), "enable nbpass.service") || !strings.Contains(unit, "User=nbpass\n") || !strings.Contains(unit, "127.0.0.1:8081") {
		t.Fatal("panel service not registered with ordinary user and loopback control listener")
	}
	// Re-running an installer must not bypass the backup/migration CLI.
	s.write("/new-binary", "different binary", 0700)
	s.run(false, "-binary", s.root+"/new-binary")
	if s.contents("/usr/local/bin/nbpass") != "#!/bin/sh\nexit 0\n" {
		t.Fatal("installer replaced an existing panel with another version")
	}
}

func TestPanelInstallerHTTPSDownloadRequiresMatchingDigest(t *testing.T) {
	s := newInstallSandbox(t, false)
	data := s.contents("/download")
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
	s.run(true, "-binary-url", "https://dist.example.com/nbpass", "-sha256", digest, "-public-origin", "https://panel.example.com")
	if s.contents("/usr/local/bin/nbpass") != data {
		t.Fatal("published image differs from verified HTTPS download")
	}
}

func TestPanelInstallerRejectsUnverifiedDownloadAndUnsafePaths(t *testing.T) {
	for _, kind := range []string{"digest", "symlink", "bad-origin"} {
		t.Run(kind, func(t *testing.T) {
			s := newInstallSandbox(t, false)
			args := []string{"-binary-url", "https://dist.example.com/nbpass", "-sha256", strings.Repeat("0", 64), "-public-origin", "https://panel.example.com"}
			if kind == "symlink" {
				args = []string{"-binary", s.root + "/download", "-public-origin", "https://panel.example.com"}
				if err := os.Symlink(s.root+"/outside", s.root+"/var/lib/nbpass"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "bad-origin" {
				args[len(args)-1] = "https://panel.example.com\nExecStart=evil"
			}
			s.run(false, args...)
			if _, err := os.Stat(s.root + "/usr/local/bin/nbpass"); !os.IsNotExist(err) {
				t.Fatal("failed installation published a binary")
			}
		})
	}
}

func TestNodeInstallerRepeatPreservesCacheAndInstallsUpdater(t *testing.T) {
	s := newInstallSandbox(t, true)
	args := []string{"-control-url", "https://control.example.com", "-ticket", strings.Repeat("a", 43)}
	s.run(true, args...)
	s.write("/var/lib/nbpass-node/config.enc", "applied-cache", 0600)
	s.write("/var/lib/nbpass-node/rotation.enc", "dedup-journal", 0600)
	s.write("/var/lib/nbpass-update/progress.json", "update-progress", 0600)
	s.write("/etc/systemd/system/nbpass-node.service.d/limits.conf", "[Service]\nLimitNOFILE=70000\n", 0644)
	key := s.contents("/var/lib/nbpass-node/node.key")
	args[3] = strings.Repeat("b", 43) // A fresh ticket is required for reinstallation.
	s.run(true, args...)
	if s.contents("/var/lib/nbpass-node/config.enc") != "applied-cache" || s.contents("/var/lib/nbpass-node/rotation.enc") != "dedup-journal" || s.contents("/var/lib/nbpass-node/node.key") != key || s.contents("/var/lib/nbpass-update/progress.json") != "update-progress" {
		t.Fatal("repeat installation changed persistent node state")
	}
	for _, path := range []string{"/var/lib/nbpass-node", "/var/lib/nbpass-update", "/usr/local/bin/.nbpass-update"} {
		s.mode(path, 0700)
	}
	for _, path := range []string{"/usr/local/bin/nbpass-node", "/usr/local/libexec/nbpass-update"} {
		s.mode(path, 0755)
	}
	unit := s.contents("/etc/systemd/system/nbpass-node.service") + s.contents("/etc/systemd/system/nbpass-update.service")
	if strings.Contains(unit, strings.Repeat("a", 43)) || strings.Contains(unit, "-ticket") || !strings.Contains(unit, "User=root\n") {
		t.Fatal("service leaked ticket or did not explicitly assign updater root ownership")
	}
	if !strings.Contains(s.contents("/systemctl.calls"), "enable nbpass-node.service nbpass-update.service") {
		t.Fatal("both services must be enabled")
	}
	if s.contents("/etc/systemd/system/nbpass-node.service.d/limits.conf") != "[Service]\nLimitNOFILE=70000\n" {
		t.Fatal("repeat installation changed local service drop-in")
	}
}

func TestNodeInstallerFailurePreservesExistingInstallation(t *testing.T) {
	for _, failure := range []string{"FAIL_ENROLL=1", "FAIL_DOWNLOAD=1", "symlink-cache", "symlink-parent"} {
		t.Run(failure, func(t *testing.T) {
			s := newInstallSandbox(t, true)
			s.write("/usr/local/bin/nbpass-node", "old-binary", 0755)
			s.write("/etc/systemd/system/nbpass-node.service", "old-service", 0644)
			if strings.HasPrefix(failure, "FAIL_") {
				s.env = append(s.env, failure)
			} else if failure == "symlink-cache" {
				if err := os.Symlink(s.root+"/outside", s.root+"/var/lib/nbpass-node"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(s.root+"/usr/local/libexec", s.root+"/outside"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(s.root+"/outside", s.root+"/usr/local/libexec"); err != nil {
					t.Fatal(err)
				}
			}
			s.run(false, "-control-url", "https://control.example.com", "-ticket", strings.Repeat("a", 43))
			if s.contents("/usr/local/bin/nbpass-node") != "old-binary" || s.contents("/etc/systemd/system/nbpass-node.service") != "old-service" {
				t.Fatal("failure changed an existing binary or service")
			}
		})
	}
}

func TestNodeInstallerReplacesUpdaterOnSeparateFilesystem(t *testing.T) {
	s := newInstallSandbox(t, true)
	mv, err := exec.LookPath("mv")
	if err != nil {
		t.Fatal(err)
	}
	// Model libexec as a separate mount holding an executing updater: a
	// cross-device mv would fall back to copying and fail with ETXTBSY.
	s.write("/mock/mv", "#!/bin/sh\ncase \"$2\" in \"$TEST_ROOT/usr/local/bin/\"*)\n [ \"$3\" != \"$TEST_ROOT/usr/local/libexec/nbpass-update\" ] || { echo ETXTBSY >&2; exit 1; };;\nesac\nexec "+shellQuote(mv)+" \"$@\"\n", 0700)
	s.write("/usr/local/libexec/nbpass-update", "running-old-updater", 0755)
	s.run(true, "-control-url", "https://control.example.com", "-ticket", strings.Repeat("a", 43))
	if s.contents("/usr/local/libexec/nbpass-update") != s.contents("/download") {
		t.Fatal("updater was not replaced")
	}
}
