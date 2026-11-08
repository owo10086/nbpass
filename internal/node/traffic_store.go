package node

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const trafficFileLimit = 64 << 20
const trafficPlainLimit = 256 << 20

func (s FileStore) LoadTraffic(ctx context.Context) ([]byte, error) {
	path := filepath.Join(s.Dir, "traffic.enc")
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
	data, err := io.ReadAll(io.LimitReader(f, trafficFileLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > trafficFileLimit || len(data) < aead.NonceSize() {
		return nil, errors.New("流量缓存损坏或过大")
	}
	compressed, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte("nbpass-node-traffic-v1"))
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	plain, err := io.ReadAll(io.LimitReader(reader, trafficPlainLimit+1))
	if err != nil {
		return nil, err
	}
	if len(plain) > trafficPlainLimit {
		return nil, errors.New("流量缓存解压后过大")
	}
	return plain, nil
}
func (s FileStore) SaveTraffic(ctx context.Context, data []byte) error {
	if len(data) > trafficPlainLimit {
		return errors.New("流量缓存过大")
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err = writer.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	data = compressed.Bytes()
	aead, err := s.aead(ctx)
	if err != nil {
		return err
	}
	if len(data)+aead.NonceSize()+aead.Overhead() > trafficFileLimit {
		return errors.New("流量缓存过大")
	}
	path := filepath.Join(s.Dir, "traffic.enc")
	if err = safeCredential(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(s.Random, nonce); err != nil {
		return err
	}
	data = aead.Seal(nonce, nonce, data, []byte("nbpass-node-traffic-v1"))
	f, err := os.CreateTemp(s.Dir, ".traffic-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
