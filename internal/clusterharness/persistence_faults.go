package clusterharness

import (
	"context"
	"database/sql"
	"sync"

	"nbpass/internal/panel"
)

// PersistenceFaults 在真实 SQLite 写边界注入故障，认证与读取仍照常运行。
// 不提供面板内部状态读取能力。
type PersistenceFaults struct {
	Base panel.Persistence
	mu   sync.Mutex
	db   *sql.DB
}

func (f *PersistenceFaults) Open(ctx context.Context) (*sql.DB, error) {
	db, err := f.Base.Open(ctx)
	f.mu.Lock()
	f.db = db
	f.mu.Unlock()
	return db, err
}

// FailEntryActions 模拟动作 INSERT 或 UPDATE 写失败；空字符串恢复。
func (f *PersistenceFaults) FailEntryActions(operation string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.db.Exec("DROP TRIGGER IF EXISTS fail_entry_actions"); err != nil {
		return err
	}
	if operation == "" {
		return nil
	}
	if operation != "INSERT" && operation != "UPDATE" {
		return sql.ErrNoRows
	}
	_, err := f.db.Exec("CREATE TEMP TRIGGER fail_entry_actions BEFORE " + operation + " ON entry_actions BEGIN SELECT RAISE(FAIL, '测试台注入磁盘写失败'); END")
	return err
}
