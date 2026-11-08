package dnswatch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"golang.org/x/net/dns/dnsmessage"
)

// HTTP 可注入以验证真实编解码；默认不继承环境代理，也不跟随重定向。
type Client struct{ HTTP *http.Client }

var defaultHTTP = &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DoH 不允许重定向") }}

func (c Client) Lookup(ctx context.Context, domain string, resolver Resolver) ([]string, error) {
	name, err := dnsmessage.NewName(domain + ".")
	if err != nil {
		return nil, err
	}
	var nonce [2]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: binary.BigEndian.Uint16(nonce[:]), RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	wire, err := query.Pack()
	if err != nil {
		return nil, err
	}
	var data []byte
	switch resolver.Kind {
	case "dns":
		data, err = dnsExchange(ctx, "udp4", resolver.Address, wire)
	case "doh":
		data, err = c.dohExchange(ctx, resolver.Address, wire)
	default:
		return nil, errors.New("解析器类型无效")
	}
	if err != nil {
		return nil, err
	}
	var answer dnsmessage.Message
	if err = answer.Unpack(data); err != nil {
		return nil, errors.New("DNS 应答无效")
	}
	if resolver.Kind == "dns" && answer.Response && answer.ID == query.ID && answer.Truncated {
		data, err = dnsExchange(ctx, "tcp4", resolver.Address, wire)
		if err != nil {
			return nil, err
		}
		if err = answer.Unpack(data); err != nil {
			return nil, errors.New("DNS TCP 应答无效")
		}
	}
	if !answer.Response || answer.Truncated || answer.ID != query.ID || answer.RCode != dnsmessage.RCodeSuccess || len(answer.Questions) != 1 || answer.Questions[0].Type != dnsmessage.TypeA || answer.Questions[0].Class != dnsmessage.ClassINET || !strings.EqualFold(answer.Questions[0].Name.String(), name.String()) {
		return nil, errors.New("DNS 应答问题或状态无效")
	}
	// 只接受问题及其 CNAME 链上的 A，不混入其他域名的答案或 AAAA。
	names := map[string]bool{strings.ToLower(name.String()): true}
	for range 16 {
		changed := false
		for _, resource := range answer.Answers {
			if cname, ok := resource.Body.(*dnsmessage.CNAMEResource); ok && resource.Header.Class == dnsmessage.ClassINET && names[strings.ToLower(resource.Header.Name.String())] && !names[strings.ToLower(cname.CNAME.String())] {
				names[strings.ToLower(cname.CNAME.String())] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	ips := []string{}
	for _, resource := range answer.Answers {
		if a, ok := resource.Body.(*dnsmessage.AResource); ok && resource.Header.Class == dnsmessage.ClassINET && names[strings.ToLower(resource.Header.Name.String())] {
			ips = append(ips, net.IP(a.A[:]).String())
		}
	}
	if len(ips) == 0 {
		return nil, errors.New("DNS 未返回 A 记录")
	}
	return Addresses(ips)
}

// 直接发送绝对域名的 A 查询，绕过 hosts、系统 DNS 和 search/ndots；UDP 截断时回退 TCP。
func dnsExchange(ctx context.Context, network, address string, wire []byte) ([]byte, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, errors.New("DNS 连接失败")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if network == "tcp4" {
		packet := make([]byte, 2, len(wire)+2)
		binary.BigEndian.PutUint16(packet, uint16(len(wire)))
		packet = append(packet, wire...)
		if _, err = io.Copy(conn, bytes.NewReader(packet)); err != nil {
			return nil, errors.New("DNS TCP 查询失败")
		}
		var size [2]byte
		if _, err = io.ReadFull(conn, size[:]); err != nil {
			return nil, errors.New("DNS TCP 长度无效")
		}
		data := make([]byte, int(binary.BigEndian.Uint16(size[:])))
		if _, err = io.ReadFull(conn, data); err != nil {
			return nil, errors.New("DNS TCP 应答不完整")
		}
		return data, nil
	}
	if _, err = conn.Write(wire); err != nil {
		return nil, errors.New("DNS UDP 查询失败")
	}
	data := make([]byte, 64<<10)
	n, err := conn.Read(data)
	if err != nil {
		return nil, errors.New("DNS UDP 采样失败")
	}
	return data[:n], nil
}
func (c Client) dohExchange(ctx context.Context, address string, wire []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", address, bytes.NewReader(wire))
	if err != nil {
		return nil, errors.New("DoH 地址无效")
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	client := c.HTTP
	if client == nil {
		client = defaultHTTP
	}
	copyClient := *client
	copyClient.CheckRedirect = defaultHTTP.CheckRedirect
	response, err := copyClient.Do(req)
	if err != nil {
		return nil, errors.New("DoH 采样失败")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || strings.Split(response.Header.Get("Content-Type"), ";")[0] != "application/dns-message" {
		return nil, errors.New("DoH 响应无效")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("DoH 响应过大或不完整")
	}
	return data, nil
}
