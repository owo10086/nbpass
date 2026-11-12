package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"nbpass/internal/controlpb"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/forwarding"
)

// 真实旧源基线：3899928362a75b58ac74ff6844652d319760b656。
// 后续源 IP / relay 票复用此旧读取器，不能把新版 StateCurrent 改为 1 来冒充旧版。
func TestActualV1ReaderRejectsNewlyPersistedCache(t *testing.T) {
	ctx := context.Background()
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
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var observations atomic.Int32
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			observations.Add(1)
			c.Close()
		}
	}()
	for _, kind := range []string{"direct", "exit"} {
		for _, checkpoint := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/checkpoint=%t", kind, checkpoint), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "node")
				store := FileStore{Dir: dir, Random: rand.Reader}
				credential := Credentials{NodeID: "n", ControlURL: "https://panel.example", Token: strings.Repeat("t", 43)}
				if err := store.Save(ctx, credential); err != nil {
					t.Fatal(err)
				}
				freePort := func() uint32 {
					l, err := net.Listen("tcp4", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					p := uint32(l.Addr().(*net.TCPAddr).Port)
					l.Close()
					return p
				}
				businessPort := freePort()
				rule := &controlpb.ForwardRule{Id: "r", Kind: controlpb.RuleKind_RULE_KIND_DIRECT, ListenPort: businessPort, TargetAddress: "127.0.0.1", TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), TargetFamily: controlpb.AddressFamily_ADDRESS_FAMILY_IPV4, AllowPrivate: true}
				snapshot := &controlpb.ConfigSnapshot{Revision: 7, OutboundIpv4: "127.0.0.1", Rules: []*controlpb.ForwardRule{rule}}
				if kind == "exit" {
					snapshot.Rules = nil
					rule.Kind = controlpb.RuleKind_RULE_KIND_TUNNEL
					snapshot.TunnelServer = rollbackTunnelServer(t, freePort(), rule)
					businessPort = snapshot.TunnelServer.ListenPort
				}
				var actual []forwarding.State
				engine := forwarding.NewEngine(forwarding.Dependencies{Persist: func(state forwarding.State) error {
					actual = append(actual, state)
					return persistConfigCache(ctx, credential, store, state)
				}})
				snapshot.Digest = forwarding.SnapshotDigest(snapshot)
				if err := engine.Apply(snapshot); err != nil {
					engine.Close()
					t.Fatal(err)
				}
				if checkpoint {
					snapshot.Revision = 9
					snapshot.Rules = nil
					if snapshot.TunnelServer != nil {
						snapshot.TunnelServer.Grants = nil
						snapshot.TunnelServer.Peers = nil
					}
					snapshot.Digest = forwarding.SnapshotDigest(snapshot)
					if err := engine.Apply(snapshot); err != nil {
						engine.Close()
						t.Fatal(err)
					}
					if err := persistConfigCache(ctx, credential, store, actual[len(actual)-2]); err != nil {
						t.Fatal(err)
					}
				}
				engine.Close()
				data, err := store.LoadConfig(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var cache configCache
				if err = json.Unmarshal(data, &cache); err != nil || cache.Version != 2 || cache.State.OutboundIPv4 != "127.0.0.1" {
					t.Fatal("actual source cache lost", cache, err)
				}
				before := observations.Load()
				cmd := exec.CommandContext(ctx, "go", "test", "-overlay", path, "-vet=off", "./internal/node", "-run", "^TestRollbackOldReaderRejectsActualNewCache$", "-count=1", "-v")
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "NBPASS_ROLLBACK_CACHE_DIR="+dir, "NBPASS_ROLLBACK_BUSINESS_PORT="+strconv.Itoa(int(businessPort)))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("真实旧代码验证失败: %v\n%s", err, out)
				}
				t.Log(strings.TrimSpace(string(out)))
				if observations.Load() != before {
					t.Fatal("old reader connected target without source binding")
				}
				t.Logf("actual old Run rejected version 2; business listener absent; target connections=%d", observations.Load()-before)
			})
		}
	}
	// install.go is unchanged from the baseline; execute its actual candidate
	// check with the old compatibility implementation.
	cmd := exec.CommandContext(ctx, "go", "test", "-overlay", path, "-vet=off", "./internal/nodeupdate", "-run", "^TestActualOldUpdaterRejectsWriterTwo$", "-count=1")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("真实旧更新器验证失败: %v\n%s", err, out)
	}
}

func rollbackTunnelServer(t *testing.T, port uint32, rule *controlpb.ForwardRule) *controlpb.TunnelServer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rollback.test"}, DNSNames: []string{"rollback.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return &controlpb.TunnelServer{ListenPort: port, Transport: controlpb.TunnelTransport_TUNNEL_TRANSPORT_WSS, ServerName: "rollback.test", CertificatePem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKeyPem: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), Peers: []*controlpb.TunnelPeer{{EntryId: "a", Credential: strings.Repeat("a", 43)}}, Grants: []*controlpb.TunnelGrant{{EntryId: "a", Rule: rule}}}
}
