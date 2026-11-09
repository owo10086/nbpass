package nodeupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"

	"nbpass/internal/compatibility"
	"nbpass/internal/controlpb"
)

type Identity struct {
	Compatibility *controlpb.Compatibility `json:"compatibility,omitempty"`
	Version       string                   `json:"version"`
	Platform      string                   `json:"platform"`
}

// FixedInstaller owns its directory and never restores the mutable node data directory.
// Candidate and backup are private root-owned files on the installation filesystem.
type FixedInstaller struct {
	Path       string
	allowedDir string // Only package tests relocate the fixed installation directory.
	Check      func(context.Context, string) (Identity, error)
	Restart    func(context.Context) error
	Healthy    func(context.Context) error
}

func safeParents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || !ok || stat.Uid != 0 {
			return errors.New("安装目录必须为不可由其他用户写入的真实目录")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
func regular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("拒绝符号链接或非普通文件")
	}
	return nil
}
func (f *FixedInstaller) paths() (string, error) {
	dir := filepath.Dir(InstallPath)
	if f.allowedDir != "" {
		dir = f.allowedDir
	}
	if f.Path != filepath.Join(dir, "nbpass-node") {
		return "", errors.New("只允许固定节点安装路径")
	}
	checkDir := dir
	if f.allowedDir != "" {
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return "", errors.New("测试安装目录不安全")
		}
		checkDir = ""
	}
	if err := func() error {
		if checkDir == "" {
			return nil
		}
		return safeParents(checkDir)
	}(); err != nil {
		return "", err
	}
	if err := regular(f.Path); err != nil {
		return "", err
	}
	if f.allowedDir == "" {
		info, err := os.Lstat(f.Path)
		if err != nil {
			return "", err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return "", errors.New("节点产物必须归 root 所有且不可由其他用户写入")
		}
	}
	work := filepath.Join(dir, ".nbpass-update")
	if err := os.Mkdir(work, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(work)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || (f.allowedDir == "" && (!ok || stat.Uid != 0)) {
		return "", errors.New("更新暂存目录必须为权限 0700 的真实目录")
	}
	return work, nil
}
func writeSync(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	err = file.Chmod(mode)
	if err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (f *FixedInstaller) rollback(ctx context.Context, work string) error {
	if err := regular(filepath.Join(work, "backup")); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(work, "backup"))
	if err != nil {
		return err
	}
	restore := filepath.Join(work, "restore")
	if err := regular(restore); err == nil {
		if err = os.Remove(restore); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = writeSync(restore, data, 0755); err != nil {
		return err
	}
	if err = os.Rename(restore, f.Path); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(f.Path)); err != nil {
		return err
	}
	if f.Restart == nil {
		return errors.New("缺少重启接口")
	}
	if err := f.Restart(ctx); err != nil {
		return errors.New("旧程序已恢复，但重启失败")
	}
	if err := os.Remove(filepath.Join(work, "pending.json")); err != nil {
		return err
	}
	return syncDir(work)
}

// Recover restores a switched binary after updater interruption; mutable security state is retained.
func (f *FixedInstaller) Recover(ctx context.Context) error {
	work, err := f.paths()
	if err != nil {
		return err
	}
	if err = regular(filepath.Join(work, "pending.json")); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return f.rollback(ctx, work)
}
func (f *FixedInstaller) Apply(ctx context.Context, m Manifest, data []byte, emit func(string)) error {
	if err := Verify(m, data); err != nil {
		return err
	}
	work, err := f.paths()
	if err != nil {
		return err
	}
	if _, err = os.Lstat(filepath.Join(work, "pending.json")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("存在未恢复的安装事务")
	}
	// Remove only known regular remnants of an interrupted pre-switch preparation.
	for _, name := range []string{"candidate", "backup"} {
		p := filepath.Join(work, name)
		if err := regular(p); err == nil {
			if err = os.Remove(p); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	candidate := filepath.Join(work, "candidate")
	if err = writeSync(candidate, data, 0755); err != nil {
		return err
	}
	defer os.Remove(candidate)
	if f.Check == nil {
		return errors.New("缺少候选程序自检")
	}
	identity, err := f.Check(ctx, candidate)
	if err != nil || identity.Version != m.Version || identity.Platform != m.Platform {
		return errors.New("候选程序版本、平台或自检不符")
	}
	if identity.Compatibility != nil && !compatibility.CompatibleState(identity.Compatibility) {
		return errors.New("候选程序的本地状态读写格式与当前及回滚程序不兼容")
	}
	// Verify the same private candidate after executing its self-check. No untrusted directory
	// or caller-provided pathname participates between this check and rename.
	actual, err := os.ReadFile(candidate)
	if err != nil {
		return err
	}
	if err = Verify(m, actual); err != nil {
		return err
	}
	old, err := os.ReadFile(f.Path)
	if err != nil {
		return err
	}
	if err = writeSync(filepath.Join(work, "backup"), old, 0755); err != nil {
		return err
	}
	journal, _ := json.Marshal(m)
	if err = writeSync(filepath.Join(work, "pending.json"), journal, 0600); err != nil {
		return err
	}
	if err = syncDir(work); err != nil {
		return err
	}
	emit("switching")
	if err = os.Rename(candidate, f.Path); err != nil {
		return err
	}
	if err = syncDir(filepath.Dir(f.Path)); err != nil {
		return err
	}
	fail := errors.New("缺少重启或业务自检接口")
	if f.Restart != nil && f.Healthy != nil {
		fail = f.Restart(ctx)
		if fail == nil {
			fail = f.Healthy(ctx)
		}
	}
	if fail != nil {
		emit("rolling_back")
		if err = f.rollback(context.WithoutCancel(ctx), work); err != nil {
			return errors.New("回滚未完成，需要人工检查固定安装路径与服务")
		}
		emit("rolled_back")
		return ErrRolledBack
	}
	if err = os.Remove(filepath.Join(work, "pending.json")); err != nil {
		return err
	}
	if err = syncDir(work); err != nil {
		return err
	}
	emit("reconnecting")
	return nil
}
