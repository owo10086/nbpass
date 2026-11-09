package nodeupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Manager serializes privileged updates and durably records progress before side effects.
// Its context belongs to the updater service, independently of node/control connections.
type Manager struct {
	mu       sync.Mutex
	workers  sync.WaitGroup
	ctx      context.Context
	source   Source
	apply    Apply
	path     string
	progress Progress
	running  bool
}

func OpenManager(ctx context.Context, path string, source Source, apply Apply) (*Manager, error) {
	m := &Manager{ctx: ctx, path: path, source: source, apply: apply}
	if path != "" {
		if err := regular(path); err == nil {
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if err = json.Unmarshal(data, &m.progress); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if m.progress.State != "" && !Terminal(m.progress.State) && m.progress.State != "reconnecting" {
			m.progress.State = "failed"
			m.progress.Error = "更新服务曾中断，已先恢复安装事务；请核对后重试"
			m.progress.Sequence++
			m.progress.Steps = append(m.progress.Steps, "failed")
			if err := m.save(); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
func (m *Manager) save() error {
	if m.path == "" {
		return nil
	}
	if err := regular(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(m.progress)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(m.path), ".progress-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(file.Name(), m.path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(m.path))
}
func (m *Manager) Status(context.Context) (Progress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.copy(), nil
}
func (m *Manager) copy() Progress {
	p := m.progress
	p.Steps = append([]string(nil), p.Steps...)
	return p
}
func (m *Manager) Submit(_ context.Context, wanted Manifest) (Progress, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return Progress{}, err
	}
	if err := wanted.Validate(); err != nil {
		return Progress{}, err
	}
	if m.progress.Manifest.TaskID == wanted.TaskID {
		if m.progress.Manifest != wanted {
			return Progress{}, errors.New("相同任务清单不可变更")
		}
		return m.copy(), nil
	}
	if m.running {
		return Progress{}, errors.New("更新服务忙碌")
	}
	old := m.progress
	m.progress = Progress{Manifest: wanted, State: "downloading", Sequence: 1, Steps: []string{"downloading"}}
	if err := m.save(); err != nil {
		m.progress = old
		return Progress{}, err
	}
	m.running = true
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		emit := func(stage string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.progress.State == stage {
				return
			}
			m.progress.State = stage
			m.progress.Sequence++
			m.progress.Steps = append(m.progress.Steps, stage)
			_ = m.save()
		}
		err := Execute(m.ctx, wanted, m.source, m.apply, emit)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.running = false
		if err != nil {
			if errors.Is(err, ErrRolledBack) {
				m.progress.State = "rolled_back"
			} else {
				m.progress.State = "failed"
			}
			m.progress.Error = err.Error()
			if len(m.progress.Error) > 512 {
				m.progress.Error = "更新服务执行失败，请核对本机服务"
			}
			m.progress.Sequence++
			if len(m.progress.Steps) == 0 || m.progress.Steps[len(m.progress.Steps)-1] != m.progress.State {
				m.progress.Steps = append(m.progress.Steps, m.progress.State)
			}
			_ = m.save()
		}
	}()
	return m.copy(), nil
}

// Handler accepts only a bound manifest. Unknown fields (including paths/commands) fail closed.
func Handler(service Service) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, p Progress, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			http.Error(w, "更新服务拒绝请求", 409)
			return
		}
		json.NewEncoder(w).Encode(p)
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { p, e := service.Status(r.Context()); write(w, p, e) })
	mux.HandleFunc("POST /submit", func(w http.ResponseWriter, r *http.Request) {
		var m Manifest
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&m) != nil {
			http.Error(w, "清单无效", 400)
			return
		}
		var extra any
		if d.Decode(&extra) != io.EOF {
			http.Error(w, "清单无效", 400)
			return
		}
		p, e := service.Submit(r.Context(), m)
		write(w, p, e)
	})
	return mux
}

// Wait finishes an update or its rollback after the owning service context is canceled.
func (m *Manager) Wait() { m.mu.Lock(); m.mu.Unlock(); m.workers.Wait() }
