package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"nbpass/internal/panel"
	"nbpass/internal/platform"
)

const recoveryInstructions = `恢复范围包括同一备份中的程序 nbpass、数据库 panel.db、本机密钥 panel.key。
密钥用于解密数据库中的 DNSPod、Telegram、换 IP 凭据和节点配置，丢失或混用密钥无法恢复。
备份目录含完整密钥及敏感数据，目录权限 0700，数据文件 0600；请离线妥善保管。
升级：用新程序执行 upgrade -data-dir <数据目录> -backup-dir <新备份目录> -program <已安装旧程序>。
先停止面板服务；备份成功后执行事务迁移，成功后再部署新程序并启动服务。
迁移失败不替换程序，数据库事务回滚、密钥不变，直接重启原程序和原数据目录；不要再次执行同一失败迁移。
从旧快照恢复时，使用支持该快照且包含恢复保护功能的程序执行：
nbpass restore -backup-dir <备份目录> -data-dir <新的不存在的数据目录>
恢复后的数据库由执行 restore 的兼容程序启动，不能换回不支持该数据库版本的旧程序。
备份中的 nbpass 仅与未迁移的原始数据库和原密钥配套，用于升级失败的原地回退；保留原目录供核对。
restore 同时恢复数据库和密钥，并在发布目录前撤销全部会话和安装票据、暂停自动写操作。
登录后 GET /api/recovery 查看状态；节点重连、应用新配置后，POST /api/recovery/reconcile {"confirm":true} 核对。
核对只读 DNSPod，并采用能对应已核对成员的当前记录；无法核对的节点或入口组继续暂停。
最近 24 小时换 IP 历史不完整时继续暂停自动换 IP，直到记录补齐或恢复满 24 小时。
不要直接复制旧数据库覆盖正在运行的面板，也不要将旧 WAL/SHM 文件带入恢复目录。
`

type backupManifest struct {
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	Files     map[string]string `json:"sha256"`
}

func maintenanceCommand(ctx context.Context, command string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(out)
	dir := flags.String("data-dir", "data", "源数据目录；restore 必须指定尚不存在的新目录")
	backup := flags.String("backup-dir", "", "备份目录（必填；backup/upgrade 必须尚不存在）")
	program := flags.String("program", "", "已安装程序路径；upgrade 必填，用于备份旧程序")
	flags.Usage = func() {
		fmt.Fprintf(out, "nbpass %s -data-dir <目录> -backup-dir <目录> [-program <已安装程序>]\n%s", command, recoveryInstructions)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *backup == "" {
		flags.Usage()
		return errors.New("请指定备份目录，不接受位置参数")
	}
	if command == "restore" {
		return restorePanelBackup(ctx, *backup, *dir, out)
	}
	if command != "backup" && command != "upgrade" {
		return errors.New("未知维护命令")
	}
	if command == "upgrade" && *program == "" {
		return errors.New("upgrade 必须用 -program 指定已安装旧程序；先停止服务")
	}
	if *program == "" {
		var err error
		*program, err = os.Executable()
		if err != nil {
			return err
		}
	}
	var unlock func()
	if command == "upgrade" {
		var err error
		unlock, err = lockPanelData(*dir)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if err := createPanelBackup(ctx, *dir, *backup, *program); err != nil {
		return fmt.Errorf("备份失败，未执行迁移: %w", err)
	}
	fmt.Fprintf(out, "一致性备份已保存到 %s\n%s", *backup, recoveryInstructions)
	if command == "upgrade" {
		if err := panel.MigrateDatabase(ctx, platform.SQLite{Path: filepath.Join(*dir, "panel.db")}); err != nil {
			return fmt.Errorf("迁移失败: %w；备份目录 %s。恢复范围：程序、数据库、密钥文件；原程序、原数据库、原密钥均保留，可直接重启旧服务；勿重复失败迁移", err, *backup)
		}
		fmt.Fprintln(out, "迁移完成，请部署新程序后启动面板。")
	}
	return nil
}

func createPanelBackup(ctx context.Context, dir, destination, program string) error {
	if err := regularPrivate(filepath.Join(dir, "panel.db")); err != nil {
		return err
	}
	if err := regularPrivate(filepath.Join(dir, "panel.key")); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(destination)
		}
	}()
	if err := panel.BackupDatabase(ctx, platform.SQLite{Path: filepath.Join(dir, "panel.db")}, filepath.Join(destination, "panel.db")); err != nil {
		return err
	}
	if err := copyBackupFile(filepath.Join(dir, "panel.key"), filepath.Join(destination, "panel.key"), 0600); err != nil {
		return err
	}
	if err := copyBackupFile(program, filepath.Join(destination, "nbpass"), 0700); err != nil {
		return err
	}
	manifest := backupManifest{Version: 1, CreatedAt: time.Now().UTC(), Files: map[string]string{}}
	for _, name := range []string{"panel.db", "panel.key", "nbpass"} {
		digest, err := backupDigest(filepath.Join(destination, name))
		if err != nil {
			return err
		}
		manifest.Files[name] = digest
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = writeBackupFile(filepath.Join(destination, "manifest.json"), data, 0600); err != nil {
		return err
	}
	if err = writeBackupFile(filepath.Join(destination, "RECOVERY.md"), []byte(recoveryInstructions), 0600); err != nil {
		return err
	}
	if err = syncDirectory(destination); err != nil {
		return err
	}
	complete = true
	return syncDirectory(filepath.Dir(destination))
}

func restorePanelBackup(ctx context.Context, backup, destination string, out io.Writer) error {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("restore 只允许恢复到尚不存在的新数据目录")
	}
	if err := regularPrivate(filepath.Join(backup, "manifest.json")); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest backupManifest
	if err = json.Unmarshal(data, &manifest); err != nil || manifest.Version != 1 {
		return errors.New("备份清单无效")
	}
	for _, name := range []string{"panel.db", "panel.key", "nbpass"} {
		digest, err := backupDigest(filepath.Join(backup, name))
		if err != nil {
			return err
		}
		if manifest.Files[name] == "" || manifest.Files[name] != digest {
			return fmt.Errorf("备份 %s 校验失败，未恢复", name)
		}
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, name := range []string{"panel.db", "panel.key"} {
		if err = copyBackupFile(filepath.Join(backup, name), filepath.Join(stage, name), 0600); err != nil {
			return err
		}
	}
	if err = panel.PrepareRestore(ctx, platform.SQLite{Path: filepath.Join(stage, "panel.db")}, time.Now()); err != nil {
		return err
	}
	key, e := os.ReadFile(filepath.Join(stage, "panel.key"))
	if e != nil {
		return e
	}
	if err = panel.ValidateDatabaseKey(ctx, platform.SQLite{Path: filepath.Join(stage, "panel.db")}, key); err != nil {
		return err
	}
	if err = syncDirectory(stage); err != nil {
		return err
	}
	// 发布已完整设置保护的目录；正常启动永远看不到尚未设置保护的数据库。
	if err = os.Rename(stage, destination); err != nil {
		return err
	}
	if err = syncDirectory(filepath.Dir(destination)); err != nil {
		return err
	}
	fmt.Fprintf(out, "数据库与密钥已恢复到 %s；全部自动写操作暂停，旧会话和安装票据已清除。\n%s", destination, recoveryInstructions)
	return nil
}

func regularPrivate(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("%s 必须是权限 0600 的普通文件", path)
	}
	return nil
}
func backupDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("备份必须为普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func copyBackupFile(source, destination string, mode os.FileMode) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("备份源必须为普通文件")
	}
	from, err := os.Open(source)
	if err != nil {
		return err
	}
	defer from.Close()
	to, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = io.Copy(to, from)
	if err == nil {
		err = to.Sync()
	}
	return errors.Join(err, to.Close())
}
func writeBackupFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
