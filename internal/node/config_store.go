package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"nbpass/internal/compatibility"
	"nbpass/internal/forwarding"
)

// ConfigStore 与凭据、事件分别持久化，允许注入独立的磁盘故障。
type ConfigStore interface {
	LoadConfig(context.Context) ([]byte, error)
	SaveConfig(context.Context, []byte) error
}

type configCache struct {
	Version    int              `json:"version"`
	NodeID     string           `json:"node_id"`
	ControlURL string           `json:"control_url"`
	State      forwarding.State `json:"state"`
}

func openConfigCache(ctx context.Context, credential Credentials, deps *Dependencies, engine *forwarding.Engine) error {
	if deps.ConfigStore == nil {
		return nil
	}
	data, err := deps.ConfigStore.LoadConfig(ctx)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("无法读取节点配置缓存")
	}
	var cache configCache
	if err := json.Unmarshal(data, &cache); err != nil || cache.Version != int(compatibility.StateCurrent) || cache.NodeID != credential.NodeID || cache.ControlURL != credential.ControlURL {
		return errors.New("节点配置缓存格式或身份不匹配")
	}
	return engine.Restore(cache.State)
}

func (s FileStore) LoadConfig(ctx context.Context) ([]byte, error) {
	path := filepath.Join(s.Dir, "config.enc")
	if err := safeCredential(path); err != nil {
		return nil, err
	}
	aead, err := s.aead(ctx)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 || len(data) < aead.NonceSize() {
		return nil, errors.New("配置缓存损坏或过大")
	}
	return aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("nbpass-node-config-v1"))
}

func (s FileStore) SaveConfig(ctx context.Context, data []byte) error {
	if len(data) > 2<<20 {
		return errors.New("配置缓存过大")
	}
	aead, err := s.aead(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, "config.enc")
	if err := safeCredential(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(s.Random, nonce); err != nil {
		return err
	}
	data = aead.Seal(nonce, nonce, data, []byte("nbpass-node-config-v1"))
	f, err := os.CreateTemp(s.Dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
