package panel

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"nbpass/internal/timing"
)

type Clock = timing.Clock

type Persistence interface {
	Open(context.Context) (*sql.DB, error)
}

type KeyFile interface {
	LoadOrCreate(context.Context, io.Reader) ([]byte, error)
}

type PasswordHasher interface {
	Hash(string) (string, error)
	Verify(string, string) bool
}

type Dependencies struct {
	Clock        Clock
	Persistence  Persistence
	KeyFile      KeyFile
	Random       io.Reader
	Passwords    PasswordHasher
	Assets       fs.FS
	NodeBinary   func() (io.ReadCloser, error)
	RegionLookup func(context.Context, string) (Region, error)
}

type Config struct {
	PublicOrigin        string
	ControlURL          string
	PasswordConcurrency int
}

type Panel struct {
	config          Config
	deps            Dependencies
	db              *sql.DB
	passwordSlots   chan struct{}
	secrets         SecretStore
	controlMu       sync.Mutex
	controls        map[string]*controlSession
	pendingControls map[string]map[*controlSession]struct{}
	startedAt       time.Time
}

func Open(ctx context.Context, config Config, deps Dependencies) (*Panel, error) {
	origin, err := url.Parse(config.PublicOrigin)
	if err != nil || origin == nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, errors.New("public-origin 必须是不带路径的 HTTPS 源地址")
	}
	host := strings.ToLower(origin.Hostname())
	port := origin.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, errors.New("public-origin 端口无效")
		}
	}
	if port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	config.PublicOrigin = "https://" + host
	if config.ControlURL == "" {
		config.ControlURL = config.PublicOrigin
	}
	controlURL, err := url.Parse(config.ControlURL)
	if err != nil || controlURL.Scheme != "https" || controlURL.Hostname() == "" || controlURL.User != nil || controlURL.Path != "" || controlURL.RawQuery != "" || controlURL.ForceQuery || controlURL.Fragment != "" {
		return nil, errors.New("control-url 必须是不带路径的 HTTPS 地址")
	}
	if deps.Clock == nil || deps.Persistence == nil || deps.KeyFile == nil || deps.Random == nil {
		return nil, errors.New("面板的外部依赖不完整")
	}
	if config.PasswordConcurrency == 0 {
		config.PasswordConcurrency = 4
	}
	if config.PasswordConcurrency < 1 {
		return nil, errors.New("密码校验并发必须大于零")
	}
	if deps.Passwords == nil {
		deps.Passwords = BcryptPasswords{}
	}
	deps.Random = &lockedReader{Reader: deps.Random}
	db, err := deps.Persistence.Open(ctx)
	if err != nil {
		return nil, err
	}
	p := &Panel{config: config, deps: deps, db: db, passwordSlots: make(chan struct{}, config.PasswordConcurrency), controls: make(map[string]*controlSession), pendingControls: make(map[string]map[*controlSession]struct{}), startedAt: deps.Clock.Now()}
	if err := p.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	key, err := deps.KeyFile.LoadOrCreate(ctx, deps.Random)
	if err != nil {
		db.Close()
		return nil, err
	}
	p.secrets, err = newSecrets(db, key, deps.Random)
	if err == nil {
		err = p.checkKey(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return p, nil
}

func (p *Panel) Close() error {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	for _, session := range p.controls {
		session.stop()
	}
	for _, pending := range p.pendingControls {
		for session := range pending {
			session.stop()
		}
	}
	return p.db.Close()
}

func (p *Panel) Secrets() SecretStore { return p.secrets }

type lockedReader struct {
	sync.Mutex
	io.Reader
}

func (r *lockedReader) Read(b []byte) (int, error) {
	r.Lock()
	defer r.Unlock()
	return r.Reader.Read(b)
}
