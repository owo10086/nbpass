package nodeupdate

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// HTTPS authenticates both resources at a fixed origin; redirects are never followed.
type HTTPS struct {
	Origin string
	Token  func(context.Context) (string, error)
	Client *http.Client
}

func NewHTTPS(origin string, roots *x509.CertPool, token func(context.Context) (string, error)) (*HTTPS, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("更新源必须是固定 HTTPS 源地址")
	}
	return &HTTPS{Origin: origin, Token: token, Client: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("更新下载不允许重定向") }}}, nil
}
func (s *HTTPS) get(ctx context.Context, id, resource string) (io.ReadCloser, error) {
	if !taskPattern.MatchString(id) {
		return nil, errors.New("任务 ID 无效")
	}
	token, err := s.Token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", s.Origin+"/node-upgrades/"+id+"/"+resource, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, errors.New("升级资源不可用")
	}
	return resp.Body, nil
}
func (s *HTTPS) Manifest(ctx context.Context, id string) (Manifest, error) {
	var m Manifest
	body, err := s.get(ctx, id, "manifest")
	if err != nil {
		return m, err
	}
	defer body.Close()
	d := json.NewDecoder(io.LimitReader(body, 4096))
	d.DisallowUnknownFields()
	err = d.Decode(&m)
	return m, err
}
func (s *HTTPS) Artifact(ctx context.Context, id string) (io.ReadCloser, error) {
	return s.get(ctx, id, "artifact")
}

type UnixClient struct{ Client *http.Client }

func NewUnixClient() *UnixClient {
	return &UnixClient{Client: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath)
	}}}}
}
func (c *UnixClient) call(ctx context.Context, method, path string, m any) (Progress, error) {
	var p Progress
	var body io.Reader
	if m != nil {
		data, _ := json.Marshal(m)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://updater"+path, body)
	if err != nil {
		return p, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return p, errors.New("独立更新服务拒绝任务")
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&p)
	return p, err
}
func (c *UnixClient) Submit(ctx context.Context, m Manifest) (Progress, error) {
	return c.call(ctx, "POST", "/submit", m)
}
func (c *UnixClient) Status(ctx context.Context) (Progress, error) {
	return c.call(ctx, "GET", "/status", nil)
}
