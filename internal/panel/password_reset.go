package panel

import (
	"context"
	"database/sql"
	"errors"
)

func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 72 }

// ResetAdministratorPassword 是本机 CLI 的入口，不依赖 HTTP 监听、公共源或本机密钥。
// 与在线面板使用同一 SQLite 事务边界，使密码替换和全部会话撤销同时生效。
func ResetAdministratorPassword(ctx context.Context, password string, deps Dependencies) error {
	if !validPassword(password) {
		return errors.New("密码须为 12–72 字节")
	}
	if deps.Persistence == nil {
		return errors.New("缺少密码重置的存储依赖")
	}
	if deps.Passwords == nil {
		deps.Passwords = BcryptPasswords{}
	}
	hash, err := deps.Passwords.Hash(password)
	if err != nil {
		return err
	}
	db, err := deps.Persistence.Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE administrator SET password_hash = ? WHERE id = 1", hash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("管理员密码尚未设置，请先通过面板完成首次设置")
	}
	if err := clearPasswordSessions(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func clearPasswordSessions(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM auth_login_failures")
	return err
}
