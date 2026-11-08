package node

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"nbpass/internal/platform"
)

// FileStore 用专用本机密钥加密可恢复凭据，目录 0700、文件 0600。
type FileStore struct {
	Dir    string
	Random io.Reader
}

func (s FileStore) aead(ctx context.Context) (cipher.AEAD, error) {
	if s.Random == nil {
		return nil, errors.New("凭据存储缺少随机源")
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("节点缓存目录必须为权限 0700 的目录")
	}
	key, err := (platform.LocalKeyFile{Path: filepath.Join(s.Dir, "node.key")}).LoadOrCreate(ctx, s.Random)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func safeCredential(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("节点凭据必须为权限 0600 的普通文件")
	}
	return nil
}

func (s FileStore) Load(ctx context.Context) (Credentials, error) {
	var value Credentials
	path := filepath.Join(s.Dir, "credentials.enc")
	if err := safeCredential(path); err != nil {
		return value, err
	}
	aead, err := s.aead(ctx)
	if err != nil {
		return value, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	if len(data) < aead.NonceSize() {
		return value, errors.New("节点凭据损坏")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("nbpass-node-credentials-v1"))
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(plain, &value); err != nil {
		return value, err
	}
	return value, nil
}

func (s FileStore) Save(ctx context.Context, value Credentials) error {
	aead, err := s.aead(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, "credentials.enc")
	if err := safeCredential(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(s.Random, nonce); err != nil {
		return err
	}
	data = aead.Seal(nonce, nonce, data, []byte("nbpass-node-credentials-v1"))
	file, err := os.CreateTemp(s.Dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
