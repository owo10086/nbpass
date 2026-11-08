package node

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func (s FileStore) LoadRotation(ctx context.Context) ([]byte, error) {
	path := filepath.Join(s.Dir, "rotation.enc")
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
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 || len(data) < aead.NonceSize() {
		return nil, errors.New("换 IP 动作缓存损坏或过大")
	}
	return aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("nbpass-node-rotation-v1"))
}

func (s FileStore) SaveRotation(ctx context.Context, data []byte) error {
	if len(data) > 8<<20 {
		return errors.New("换 IP 动作缓存过大")
	}
	aead, err := s.aead(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, "rotation.enc")
	if err := safeCredential(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(s.Random, nonce); err != nil {
		return err
	}
	data = aead.Seal(nonce, nonce, data, []byte("nbpass-node-rotation-v1"))
	f, err := os.CreateTemp(s.Dir, ".rotation-*")
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
