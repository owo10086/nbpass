package clusterharness

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"nbpass/internal/node"
)

// RotationPersistenceFaults 只在真实节点动作存储的写边界注入故障。
// Base 仍负责加密落盘和读取；不暴露动作内容供业务断言。
type RotationPersistenceFaults struct {
	Base      node.RotationStore
	mu        sync.Mutex
	failState string
}

func (f *RotationPersistenceFaults) FailState(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failState = state
}

func (f *RotationPersistenceFaults) LoadRotation(ctx context.Context) ([]byte, error) {
	return f.Base.LoadRotation(ctx)
}

func (f *RotationPersistenceFaults) SaveRotation(ctx context.Context, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var journal struct {
		Current string `json:"current"`
		Results map[string]struct {
			State string `json:"state"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		return err
	}
	if f.failState != "" && journal.Results[journal.Current].State == f.failState {
		return errors.New("测试台注入节点动作写失败")
	}
	return f.Base.SaveRotation(ctx, data)
}

// FailRotationActions 与既有 entry_actions 故障相同，不妨碍 HTTP 认证或读取。
func (f *PersistenceFaults) FailRotationActions(operation string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.db.Exec("DROP TRIGGER IF EXISTS fail_rotation_actions"); err != nil {
		return err
	}
	if operation == "" {
		return nil
	}
	if operation != "INSERT" && operation != "UPDATE" {
		return sql.ErrNoRows
	}
	_, err := f.db.Exec("CREATE TEMP TRIGGER fail_rotation_actions BEFORE " + operation + " ON rotation_actions BEGIN SELECT RAISE(FAIL, '测试台注入磁盘写失败'); END")
	return err
}
