package panel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"regexp"
)

type SecretStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
}

type sealedSecrets struct {
	db     *sql.DB
	aead   cipher.AEAD
	random io.Reader
}

func newSecrets(db *sql.DB, key []byte, random io.Reader) (SecretStore, error) {
	if len(key) != 32 {
		return nil, errors.New("本机密钥须为 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &sealedSecrets{db: db, aead: aead, random: random}, nil
}

func (s *sealedSecrets) Put(ctx context.Context, name string, value []byte) error {
	sealed, err := s.seal(name, value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO secrets(name, ciphertext) VALUES(?, ?) ON CONFLICT(name) DO UPDATE SET ciphertext = excluded.ciphertext", name, sealed)
	return err
}

func (s *sealedSecrets) seal(name string, value []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(s.random, nonce); err != nil {
		return nil, err
	}
	// 以秘密名称作为关联数据，数据库中调换两行密文也不能调换凭据。
	return s.aead.Seal(nonce, nonce, value, []byte(name)), nil
}

func (s *sealedSecrets) Get(ctx context.Context, name string) ([]byte, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT ciphertext FROM secrets WHERE name = ?", name).Scan(&sealed); err != nil {
		return nil, err
	}
	return s.open(name, sealed)
}
func (s *sealedSecrets) open(name string, sealed []byte) ([]byte, error) {
	if len(sealed) < s.aead.NonceSize() {
		return nil, errors.New("秘密密文损坏")
	}
	return s.aead.Open(nil, sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():], []byte(name))
}

func (p *Panel) checkKey(ctx context.Context) error {
	const name = "__key_check"
	expected := []byte("nbpass local encryption key v1")
	value, err := p.secrets.Get(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return p.secrets.Put(ctx, name, expected)
	}
	if err != nil || !bytes.Equal(value, expected) {
		return errors.New("本机密钥与数据库不匹配或密文损坏，请恢复原密钥")
	}
	return nil
}

var secretName = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)

func (p *Panel) putSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !secretName.MatchString(name) {
		problem(w, 400, "秘密名称无效")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Value) == 0 || len(body.Value) > 4096 {
		problem(w, 400, "秘密须为 1–4096 字节")
		return
	}
	if err := p.secrets.Put(r.Context(), name, []byte(body.Value)); err != nil {
		problem(w, 503, "无法保存秘密")
		return
	}
	respond(w, 200, map[string]bool{"configured": true})
}

func (p *Panel) getSecret(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !secretName.MatchString(name) {
		problem(w, 400, "秘密名称无效")
		return
	}
	_, err := p.secrets.Get(r.Context(), name)
	if errors.Is(err, sql.ErrNoRows) {
		problem(w, 404, "秘密未配置")
		return
	}
	if err != nil {
		problem(w, 503, "秘密无法读取")
		return
	}
	respond(w, 200, map[string]bool{"configured": true})
}
