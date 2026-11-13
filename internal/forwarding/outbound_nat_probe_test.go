package forwarding

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOutboundNATProbeUsesBoundHTTPSAndValidatesResponse(t *testing.T) {
	local := outboundTestLocalIPv4(t).String()
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"public", "89.43.141.167\n", 200, false},
		{"private", "10.20.151.167", 200, true},
		{"ipv6", "2001:4860:4860::8888", 200, true},
		{"oversize", strings.Repeat("x", 65), 200, true},
		{"status", "89.43.141.167", 503, true},
		{"redirect", "", 302, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(chan string, 16)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.RemoteAddr
				if r.URL.Path == "/redirected" {
					t.Error("probe followed redirect")
				}
				w.Header().Set("Location", "/redirected")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			value, err := queryOutboundIPv4At(ctx, local, server.URL, roots)
			if (err != nil) != tc.wantError {
				t.Fatalf("value=%q err=%v", value, err)
			}
			if !tc.wantError && value != "89.43.141.167" {
				t.Fatal("wrong observed public IP", value)
			}
			select {
			case remote := <-seen:
				host, _, _ := net.SplitHostPort(remote)
				if host != local {
					t.Fatal("probe did not bind local source", remote)
				}
			default:
				t.Fatal("probe never reached HTTPS observer")
			}
		})
	}
}

func TestOutboundNATProbeCannotFallBackFromUnavailableSource(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unavailable source reached observer") }))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := queryOutboundIPv4At(ctx, "192.0.2.253", server.URL, roots); err == nil {
		t.Fatal("unavailable source was accepted")
	}
}
