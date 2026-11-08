package node

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func (s FileStore) LoadSelfChecks(ctx context.Context) ([]byte, error) {
	path := filepath.Join(s.Dir, "self-check.enc")
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
	data, err := io.ReadAll(io.LimitReader(f, (64<<20)+65))
	if err != nil {
		return nil, err
	}
	if len(data) > (64<<20)+aead.NonceSize()+aead.Overhead() || len(data) < aead.NonceSize() {
		return nil, errors.New("自测缓存损坏或过大")
	}
	return aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("nbpass-node-self-check-v1"))
}

func (s FileStore) SaveSelfChecks(ctx context.Context, data []byte) error {
	if len(data) > 64<<20 {
		return errors.New("自测缓存过大")
	}
	aead, err := s.aead(ctx)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, "self-check.enc")
	if err := safeCredential(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(s.Random, nonce); err != nil {
		return err
	}
	data = aead.Seal(nonce, nonce, data, []byte("nbpass-node-self-check-v1"))
	f, err := os.CreateTemp(s.Dir, ".self-check-*")
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
