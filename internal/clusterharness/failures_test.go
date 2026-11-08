package clusterharness_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"nbpass/internal/clusterharness"
	"nbpass/internal/panel"
)

type interruptedRandom struct {
	mu     sync.Mutex
	failed bool
}

func (r *interruptedRandom) Read(value []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed {
		return 0, errors.New("测试台注入随机源故障")
	}
	return rand.Read(value)
}

func (r *interruptedRandom) fail(failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = failed
}

func TestRandomFailureCannotCreateSessionOrOverwriteSecret(t *testing.T) {
	random := &interruptedRandom{}
	h := clusterharness.New(t, func(deps *panel.Dependencies) { deps.Random = random })
	c := h.Client()
	setup(c)
	random.fail(true)
	c.Do("POST", "/api/auth/login", map[string]string{"password": password}, 500)
	c.Do("GET", "/api/overview", nil, 401)
	random.fail(false)
	login(c)
	random.fail(true)
	c.Do("PUT", "/api/secrets/credential", map[string]string{"value": "must-not-be-stored"}, 503)
	c.Do("GET", "/api/secrets/credential", nil, 404)
	random.fail(false)
	c.Do("PUT", "/api/secrets/credential", map[string]string{"value": "stored-credential"}, 200)
}

// 故障通过持久化依赖的公开数据库句柄注入，不接触面板内部状态。
type interruptedPersistence struct {
	base panel.Persistence
	db   *sql.DB
}

func (p *interruptedPersistence) Open(ctx context.Context) (*sql.DB, error) {
	db, err := p.base.Open(ctx)
	p.db = db
	return db, err
}

func TestPersistenceFailureDoesNotReportSuccessfulMutation(t *testing.T) {
	persistence := &interruptedPersistence{}
	h := clusterharness.New(t, func(deps *panel.Dependencies) {
		persistence.base = deps.Persistence
		deps.Persistence = persistence
	})
	c := h.Client()
	setup(c)
	login(c)
	if err := persistence.db.Close(); err != nil {
		t.Fatal(err)
	}
	c.Do("PUT", "/api/secrets/credential", map[string]string{"value": "must-not-be-stored"}, 503)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c.Do("GET", "/api/secrets/credential", nil, 404)
	c.Do("GET", "/api/overview", nil, 200)
}

func TestSessionExpirationIsStillEnforcedAfterRestart(t *testing.T) {
	h := clusterharness.New(t)
	c := h.Client()
	setup(c)
	login(c)
	h.Clock.Advance(24 * time.Hour)
	if err := h.Restart(); err != nil {
		t.Fatal(err)
	}
	c.Do("GET", "/api/overview", nil, 401)
	login(c)
	c.Do("GET", "/api/overview", nil, 200)
}
