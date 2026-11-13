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
	if err := json.Unmarshal(data, &cache); err != nil || cache.NodeID != credential.NodeID || cache.ControlURL != credential.ControlURL {
		return errors.New("节点配置缓存格式或身份不匹配")
	}
	switch cache.Version {
	case 1: // Absent IX fields retain the original semantics and revocation boundary.
	case 2:
	default:
		return errors.New("节点配置缓存格式或身份不匹配")
	}
	return engine.RestoreContext(ctx, cache.State)
}

func persistConfigCache(ctx context.Context, credential Credentials, store ConfigStore, state forwarding.State) error {
	data, err := json.Marshal(configCache{Version: int(compatibility.StateCurrent), NodeID: credential.NodeID, ControlURL: credential.ControlURL, State: state})
	if err != nil {
		return err
	}
	return store.SaveConfig(ctx, data)
}

// 换节点或换控制地址重装时，旧身份的配置、事件、流量、换 IP 与自测缓存不能带入新身份。
// 同一身份重置 token 重装全部保留；凭据缺失时无法证明缓存归属，一并清理。
// 已是“新凭据 + 旧配置缓存”的机器按缓存身份判断，重跑安装即可恢复。
func clearForeignState(ctx context.Context, deps Dependencies, hasPrevious bool, previous, next Credentials) error {
	store, ok := deps.Store.(NodeStateStore)
	if !ok {
		return nil
	}
	foreign := !hasPrevious || previous.NodeID != next.NodeID || previous.ControlURL != next.ControlURL
	configs := deps.ConfigStore
	if configs == nil {
		configs, _ = deps.Store.(ConfigStore)
	}
	if !foreign && configs != nil {
		// 读不出或格式不符的缓存留给启动校验报错，不在这里当作他人身份删除。
		if data, err := configs.LoadConfig(ctx); err == nil {
			var cache configCache
			if json.Unmarshal(data, &cache) == nil {
				foreign = cache.NodeID != next.NodeID || cache.ControlURL != next.ControlURL
			}
		}
	}
	if !foreign {
		return nil
	}
	return store.ClearNodeState(ctx)
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
