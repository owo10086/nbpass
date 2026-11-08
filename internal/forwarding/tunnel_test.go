package forwarding

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nbpass/internal/controlpb"
)

func tunnelTestIdentity(t *testing.T, name string, ca bool) ([]byte, []byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(5, 0, 0), BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ca {
		template.KeyUsage |= x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), cert, key
}
func TestTunnelRejectsWrongCertificateBeforeSendingCredentials(t *testing.T) {
	for _, mode := range []string{"other_key", "wrong_name", "expired", "signed_child"} {
		t.Run(mode, func(t *testing.T) {
			cert, key, root, rootKey := tunnelTestIdentity(t, "exit.test", true)
			trusted := append([]byte{}, cert...)
			switch mode {
			case "other_key":
				cert, key, _, _ = tunnelTestIdentity(t, "exit.test", false)
			case "wrong_name":
				cert, key, _, _ = tunnelTestIdentity(t, "other.test", false)
				trusted = cert
			case "expired": // 入口时钟已超出固定信任证书期限。
			case "signed_child":
				_, _, child, childKey := tunnelTestIdentity(t, "exit.test", false)
				child.SerialNumber = big.NewInt(2)
				der, err := x509.CreateCertificate(rand.Reader, child, root, &childKey.PublicKey, rootKey)
				if err != nil {
					t.Fatal(err)
				}
				cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
				keyDER, _ := x509.MarshalPKCS8PrivateKey(childKey)
				key = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
			}
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.Error(w, "不应到达", 403) }))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
			server.StartTLS()
			defer server.Close()
			_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
			n, _ := strconv.Atoi(port)
			e := NewEngine(Dependencies{})
			if mode == "expired" {
				e.deps.Now = func() time.Time { return time.Now().AddDate(6, 0, 0) }
			}
			client := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{NodeId: "exit", EntryId: "entry", Address: "127.0.0.1", Port: uint32(n), ServerName: "exit.test", CertificatePem: trusted, Credential: strings.Repeat("a", 43)}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			session, err := client.getSession(ctx)
			if session != nil {
				session.Close()
			}
			if err == nil || requests.Load() != 0 {
				t.Fatalf("错误出口证书通过 TLS 或收到凭据请求: err=%v requests=%d", err, requests.Load())
			}
		})
	}
}
func TestTunnelPairCredentialRefusalAndSessionReuse(t *testing.T) {
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	e := NewEngine(Dependencies{})
	server, err := e.startTunnelServer(&controlpb.TunnelServer{ListenPort: 0, CertificatePem: cert, PrivateKeyPem: key, Peers: []*controlpb.TunnelPeer{{EntryId: "entry", Credential: strings.Repeat("a", 43)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	port := uint32(server.listener.Addr().(*net.TCPAddr).Port)
	for _, credential := range []string{strings.Repeat("b", 43), strings.Repeat("a", 43)} {
		client := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: port, ServerName: "exit.test", CertificatePem: cert, EntryId: "entry", Credential: credential}}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		session, err := client.getSession(ctx)
		cancel()
		if credential[0] == 'b' {
			if err == nil {
				client.close()
				t.Fatal("错误节点对凭据被接受")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		second, err := client.getSession(context.Background())
		if err != nil || session != second {
			t.Fatal("未复用同一 WSS/yamux 会话")
		}
		client.close()
	}
}
func TestTunnelStreamHalfCloseAndCancellation(t *testing.T) {
	// 目标收到客户端 EOF 后才回复；缺失 yamux 半关闭会超时。
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		data, _ := io.ReadAll(conn)
		conn.Write(append([]byte("reply:"), data...))
	}()
	cert, key, _, _ := tunnelTestIdentity(t, "exit.test", false)
	rule := &controlpb.ForwardRule{Id: "r", Kind: controlpb.RuleKind_RULE_KIND_TUNNEL, TargetAddress: "127.0.0.1", TargetPort: uint32(target.Addr().(*net.TCPAddr).Port), AllowPrivate: true}
	e := NewEngine(Dependencies{})
	server, err := e.startTunnelServer(&controlpb.TunnelServer{CertificatePem: cert, PrivateKeyPem: key, Peers: []*controlpb.TunnelPeer{{EntryId: "entry", Credential: strings.Repeat("a", 43)}}, Grants: []*controlpb.TunnelGrant{{EntryId: "entry", Rule: rule}}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	client := &tunnelClient{engine: e, endpoint: &controlpb.TunnelEndpoint{Address: "127.0.0.1", Port: uint32(server.listener.Addr().(*net.TCPAddr).Port), ServerName: "exit.test", CertificatePem: cert, EntryId: "entry", Credential: strings.Repeat("a", 43)}}
	defer client.close()
	conn, err := client.dial(context.Background(), rule)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte("body"))
	conn.(interface{ CloseWrite() error }).CloseWrite()
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != "reply:body" {
		t.Fatalf("隧道半关闭: %q %v", got, err)
	}
}
