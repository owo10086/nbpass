package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// 服务与离线迁移共用进程锁；在线 backup 不取锁，仍可使用 SQLite 快照。
func lockPanelData(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "panel.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("面板服务正在运行或另一维护命令持有数据锁，请先停止服务再迁移")
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
