package forwarding

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"time"

	"nbpass/internal/publicip"
)

// 只探测本机可绑定地址，不修改网卡或路由。候选并发限制为 4，整轮最多
// 5 秒；所有结果完成后按地址排序选择，避免同一 NAT 下多网卡随机切换。
func (e *Engine) resolveOutboundNAT(parent context.Context, public string, ips []netip.Addr) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	addresses := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if ip.Is4() && ip.IsGlobalUnicast() {
			addresses = append(addresses, ip)
		}
	}
	slices.SortFunc(addresses, netip.Addr.Compare)
	addresses = slices.Compact(addresses)
	type observation struct {
		public   string
		err      error
		verified bool
	}
	results := make([]observation, len(addresses))
	jobs := make(chan int, len(addresses))
	for i := range addresses {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(4, len(addresses)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					results[i].err = ctx.Err()
					continue
				}
				results[i].public, results[i].err = e.deps.PublicIPv4From(ctx, addresses[i].String())
				results[i].verified = results[i].err == nil && ctx.Err() == nil
			}
		})
	}
	workers.Wait()
	var reasons []error
	for i, result := range results {
		if result.verified && result.public == public && parent.Err() == nil {
			return addresses[i].String(), nil
		}
		if len(reasons) >= 4 {
			continue
		}
		if result.err != nil {
			reasons = append(reasons, fmt.Errorf("本机 %s 探测失败: %w", addresses[i], result.err))
		} else {
			reasons = append(reasons, fmt.Errorf("本机 %s 的公网出口为 %s", addresses[i], result.public))
		}
	}
	if ctx.Err() != nil {
		reasons = append(reasons, ctx.Err())
	}
	if len(reasons) == 0 {
		reasons = append(reasons, errors.New("没有可用于 NAT 出口探测的本机 IPv4 地址"))
	}
	return "", fmt.Errorf("出站 IP %s 不是本机接口 IPv4 地址，NAT 出口核验未匹配: %w", public, errors.Join(reasons...))
}

func queryOutboundIPv4(ctx context.Context, local string) (string, error) {
	return queryOutboundIPv4At(ctx, local, "https://api.ipify.org", nil)
}

func queryOutboundIPv4At(ctx context.Context, local, endpoint string, roots *x509.CertPool) (string, error) {
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(local)}}
	transport := &http.Transport{
		// Proxy 留空，避免把代理出口误认为该本机地址的 NAT 出口。
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", address)
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("公网 IPv4 查询失败")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65))
	if err != nil {
		return "", err
	}
	if len(data) > 64 {
		return "", errors.New("公网 IPv4 查询响应过长")
	}
	return publicip.Parse(string(data))
}
