package node

import (
	"context"
	"errors"
	"io"
	"net/http"

	"nbpass/internal/publicip"
)

// PublicIPv4Source 仅查询出站公网 IPv4，不证明该地址支持直接入站。
type PublicIPv4Source struct {
	Client *http.Client
	URL    string
}

// 不继承环境代理，避免误报代理出口 IP；请求期限由节点的注入时钟控制。
var defaultPublicIPv4 = PublicIPv4Source{
	URL: "https://api.ipify.org",
	Client: &http.Client{
		Transport:     &http.Transport{ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	},
}

func (s PublicIPv4Source) Query(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := s.Client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("公网 IPv4 查询失败")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65))
	if err != nil || len(data) > 64 {
		return "", errors.New("公网 IPv4 查询响应无效")
	}
	return publicip.Parse(string(data))
}
