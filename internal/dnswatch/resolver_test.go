package dnswatch

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// 验证真实 RFC 8484 编解码；错误 DNS 状态、AAAA 和错误问题不能成为 A 样本。
func TestDoHWireAdapter(t *testing.T) {
	for _, tc := range []struct {
		name          string
		rcode         dnsmessage.RCode
		answer        string
		wrongQuestion bool
		uppercase     bool
		wantError     bool
	}{
		{name: "A", answer: "8.8.8.8"},
		{name: "问题大小写", answer: "8.8.8.8", uppercase: true},
		{name: "NXDOMAIN", rcode: dnsmessage.RCodeNameError, wantError: true},
		{name: "NODATA", wantError: true},
		{name: "错问题", answer: "8.8.8.8", wrongQuestion: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/dns-message" {
					t.Error("不是 RFC 8484 请求")
				}
				data, _ := io.ReadAll(r.Body)
				var msg dnsmessage.Message
				if err := msg.Unpack(data); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if len(msg.Questions) != 1 || msg.Questions[0].Name.String() != "watch.example." || msg.Questions[0].Type != dnsmessage.TypeA {
					t.Error("查询了错误问题")
				}
				if tc.uppercase {
					msg.Questions[0].Name, _ = dnsmessage.NewName("WATCH.EXAMPLE.")
				}
				msg.Response = true
				msg.RCode = tc.rcode
				if tc.wrongQuestion {
					msg.Questions[0].Name, _ = dnsmessage.NewName("other.example.")
				}
				if tc.answer != "" {
					msg.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: msg.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{8, 8, 8, 8}}}}
				}
				data, _ = msg.Pack()
				w.Header().Set("Content-Type", "application/dns-message")
				_, _ = w.Write(data)
			}))
			defer server.Close()
			result, err := (Client{HTTP: server.Client()}).Lookup(context.Background(), "watch.example", Resolver{Kind: "doh", Address: server.URL + "/dns-query"})
			if (err != nil) != tc.wantError {
				t.Fatalf("采样错误状态: %v %v", result, err)
			}
			if !tc.wantError && (len(result) != 1 || result[0] != "8.8.8.8") {
				t.Fatalf("地址未解码: %v", result)
			}
		})
	}
}

// 指定公共 DNS 的查询必须绕过本机 hosts/search；localhost 也必须真正进入指定的 DNS 连接。
func TestDNSWireAdapterDoesNotUseHosts(t *testing.T) {
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	done := make(chan struct{})
	defer func() { socket.Close(); <-done }()
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		n, peer, err := socket.ReadFrom(buffer)
		if err != nil {
			return
		}
		var message dnsmessage.Message
		if message.Unpack(buffer[:n]) != nil {
			return
		}
		message.Response = true
		message.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: message.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{8, 8, 8, 8}}}}
		data, _ := message.Pack()
		_, _ = socket.WriteTo(data, peer)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := (Client{}).Lookup(ctx, "localhost", Resolver{Kind: "dns", Address: socket.LocalAddr().String()})
	if err != nil || len(result) != 1 || result[0] != "8.8.8.8" {
		t.Fatalf("DNS 证据被本机 hosts 替代: %v %v", result, err)
	}
}
