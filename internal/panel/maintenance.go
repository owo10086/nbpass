package panel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BackupDatabase 使用 SQLite 自身的一致性在线快照，包含 WAL 中的已提交事务。
// 不调用 Open，因此不会迁移源库、生成密钥或启动后台动作。
func BackupDatabase(ctx context.Context, persistence Persistence, destination string) error {
	path, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	db, err := persistence.Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	// VACUUM INTO 会拒绝非空文件，并在只读快照事务内生成独立数据库。
	_, err = db.ExecContext(ctx, "VACUUM INTO '"+strings.ReplaceAll(path, "'", "''")+"'")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = errors.Join(file.Sync(), file.Close())
	if err != nil {
		return err
	}
	complete = true
	return nil
}

// MigrateDatabase 供新程序的 upgrade 子命令使用，不启动服务或自动动作。
func MigrateDatabase(ctx context.Context, persistence Persistence) error {
	db, err := persistence.Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	return (&Panel{db: db}).migrate(ctx)
}

// PrepareRestore 仅用于尚未向外提供服务的恢复数据库；必须在发布恢复目录前调用。
func PrepareRestore(ctx context.Context, persistence Persistence, now time.Time) error {
	db, err := persistence.Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = (&Panel{db: db}).migrate(ctx); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`DELETE FROM sessions`, `DELETE FROM installation_tickets`,
		`DELETE FROM recovery_nodes`, `DELETE FROM recovery_groups`,
		`DELETE FROM recovery_records`,
		// 恢复不重放旧地址收尾；逐记录读回核对后由管理员处理偏差。
		`DELETE FROM node_address_sync_jobs`,
		`DELETE FROM recovery_state`,
		`INSERT INTO recovery_state(id,restored_at) VALUES(1,?)`,
		`INSERT INTO recovery_nodes(node_id) SELECT id FROM nodes`,
		`INSERT INTO recovery_groups(group_id) SELECT id FROM entry_groups`,
		`INSERT INTO recovery_records(managed_record_id,binding_generation) SELECT id,binding_generation FROM managed_records`,
		`DELETE FROM node_rule_reports`,
		`UPDATE node_self_check SET state='unknown',consecutive=0,last_config_key=''`,
		`UPDATE self_check_rounds SET decision_eligible=0`,
		`UPDATE nodes SET applied_revision=0,applied_digest='',attempted_revision=0,apply_error='',last_seen_at=NULL,disconnected_at=NULL,process_instance=''`,
		// 旧授权可能已执行：永不重新签发或重放，等待节点的实际动作历史。
		`UPDATE rotation_actions SET state='unconfirmed',expires_at=0,pending_operation='',permit_sent=1,observation_complete=CASE WHEN state='unconfirmed' THEN observation_complete ELSE 0 END,reason='从旧备份恢复，等待节点核对' WHERE state NOT IN ('changed','cancelled','released')`,
	}
	for _, s := range statements {
		if strings.Contains(s, "VALUES(1,?)") {
			_, err = tx.ExecContext(ctx, s, now.UnixNano())
		} else {
			_, err = tx.ExecContext(ctx, s)
		}
		if err != nil {
			return fmt.Errorf("设置恢复保护: %w", err)
		}
	}
	return tx.Commit()
}

// ValidateDatabaseKey 检查恢复包中的密钥与原数据库加密哨兵匹配，不创建新哨兵。
func ValidateDatabaseKey(ctx context.Context, persistence Persistence, key []byte) error {
	db, err := persistence.Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	secrets, err := newSecrets(db, key, nil)
	if err != nil {
		return err
	}
	value, err := secrets.Get(ctx, "__key_check")
	if err != nil || string(value) != "nbpass local encryption key v1" {
		return errors.New("恢复密钥与数据库不匹配，请使用同一备份的数据库和密钥文件")
	}
	return nil
}
