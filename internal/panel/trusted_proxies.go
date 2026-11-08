package panel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

const maxTrustedProxies = 64

func proxyPrefix(value string) (netip.Prefix, error) {
	if address, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil && address.Zone() == "" {
		address = address.Unmap()
		return netip.PrefixFrom(address, address.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || prefix.Addr().Is4In6() {
		return netip.Prefix{}, errors.New("受信任代理必须是 IP 地址或 CIDR（IPv4 映射地址请使用 IPv4 CIDR）")
	}
	return prefix.Masked(), nil
}

func (p *Panel) trustedProxies(ctx context.Context) ([]netip.Prefix, error) {
	rows, err := p.db.QueryContext(ctx, "SELECT prefix FROM auth_trusted_proxies ORDER BY prefix")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]netip.Prefix, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		prefix, err := proxyPrefix(value)
		if err != nil {
			return nil, err
		}
		values = append(values, prefix)
	}
	return values, rows.Err()
}

func (p *Panel) getTrustedProxies(w http.ResponseWriter, r *http.Request) {
	prefixes, err := p.trustedProxies(r.Context())
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	values := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		values = append(values, prefix.String())
	}
	respond(w, 200, map[string]any{"trusted_proxies": values})
}

func (p *Panel) putTrustedProxies(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TrustedProxies []string `json:"trusted_proxies"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.TrustedProxies == nil || len(body.TrustedProxies) > maxTrustedProxies {
		problem(w, 400, "请提供受信任代理列表（最多 64 项，可使用空列表）")
		return
	}
	values := make(map[string]bool)
	for _, value := range body.TrustedProxies {
		prefix, err := proxyPrefix(value)
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		values[prefix.String()] = true
	}
	tx, err := p.db.BeginTx(r.Context(), nil)
	if err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM auth_trusted_proxies"); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	for value := range values {
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO auth_trusted_proxies(prefix) VALUES(?)", value); err != nil {
			problem(w, 503, "存储暂不可用")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		problem(w, 503, "存储暂不可用")
		return
	}
	p.getTrustedProxies(w, r)
}

// 只读取 X-Forwarded-For；其他来源头不参与信任判断。代理必须覆盖或追加真实的直连来源。
func (p *Panel) loginSource(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "", errors.New("来源地址无效")
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || peer.Zone() != "" {
		return "", errors.New("来源地址无效")
	}
	peer = peer.Unmap()
	prefixes, err := p.trustedProxies(r.Context())
	if err != nil {
		return "", err
	}
	trusted := func(address netip.Addr) bool {
		for _, prefix := range prefixes {
			if prefix.Contains(address) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) {
		return peer.String(), nil
	}
	chain := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	current := peer
	for i := len(chain) - 1; i >= 0 && trusted(current); i-- {
		// 不检查左侧无关前缀的总长度：客户端可伪造该前缀，不能因此
		// 改变可信代理已经追加的真实来源，也不能把它改算为代理本身。
		if len(chain)-i > 32 {
			return peer.String(), nil
		}
		address, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil || address.Zone() != "" {
			return peer.String(), nil
		}
		current = address.Unmap()
	}
	return current.String(), nil
}
