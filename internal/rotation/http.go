package rotation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"nbpass/internal/publicip"
)

// HTTPExecutor 的外部网络依赖可注入；校验与实际拨号共用已解析地址，防止 DNS 重绑定。
type HTTPExecutor struct {
	RootCAs     *x509.CertPool
	Resolve     func(context.Context, string) ([]netip.Addr, error)
	DialContext func(context.Context, string, string) (net.Conn, error)
}

func publicTarget(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is4() {
		_, err := publicip.Parse(ip.String())
		return err == nil
	}
	if !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range []string{"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
func (e HTTPExecutor) Do(ctx context.Context, c Config, v Variables) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := c.Validate(); err != nil {
		return errors.New("换 IP 请求配置无效")
	}
	r, err := c.Request(ctx, v)
	if err != nil {
		return errors.New("换 IP 请求模板无效")
	}
	initial := r.URL
	resolve := e.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	dial := e.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	check := func(u *url.URL) error {
		if u.Host != initial.Host || (u.Scheme != "https" && u.Scheme != "http") || (initial.Scheme == "https" && u.Scheme != "https") || u.User != nil {
			return errors.New("重定向目标未授权")
		}
		if u.Scheme == "http" && !c.allowsPrivate(u) {
			return errors.New("明文端点未授权")
		}
		addresses, err := resolve(ctx, u.Hostname())
		if err != nil || len(addresses) == 0 {
			return errors.New("接口地址解析失败")
		}
		for _, ip := range addresses {
			if !publicTarget(ip) && !c.allowsPrivate(u) {
				return errors.New("内网端点未授权")
			}
			if publicTarget(ip) && u.Scheme != "https" {
				return errors.New("公网接口必须使用 HTTPS")
			}
		}
		return nil
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: e.RootCAs, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("接口地址无效")
		}
		ips, err := resolve(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("接口地址解析失败")
		}
		// 当前端点由串行 RoundTrip 设置；禁用连接复用确保每次请求重校验。
		for _, ip := range ips {
			if !publicTarget(ip) && !c.allowsPrivate(r.URL) {
				return nil, errors.New("实际目标为未授权内网地址")
			}
			if publicTarget(ip) && r.URL.Scheme != "https" {
				return nil, errors.New("公网接口必须使用 HTTPS")
			}
		}
		return dial(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("重定向过多")
		}
		if err := check(next.URL); err != nil {
			return err
		}
		r = next
		return nil
	}}
	if err := check(r.URL); err != nil {
		return err
	}
	response, err := client.Do(r)
	if err != nil {
		return errors.New("换 IP HTTP 请求未确认")
	}
	defer response.Body.Close()
	size, err := io.Copy(io.Discard, io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || size > 64<<10 {
		return errors.New("换 IP 响应无效或超过 64 KB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("换 IP 接口未返回成功状态")
	}
	return nil
}
