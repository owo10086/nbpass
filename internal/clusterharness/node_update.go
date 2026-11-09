package clusterharness

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"strings"
	"sync/atomic"

	"nbpass/internal/node"
	"nbpass/internal/nodeupdate"
)

// UpdateService substitutes only the privileged updater's external environment.
// TLS resources, task bindings and artifact verification use the production update pipeline.
type UpdateService struct {
	nodeupdate.Service
	Unavailable        *atomic.Bool
	loseSubmitResponse atomic.Bool
	cancel             context.CancelFunc
	manager            *nodeupdate.Manager
}
type UpdateEnvironment struct {
	LoseSubmitResponse bool
	Unavailable        *atomic.Bool
	Corrupt            string
	Gate               <-chan struct{}
	Switch             func()
	FailStartup        bool
	FailBusiness       bool
}

func (h *Harness) UpdateService(store node.Store, env UpdateEnvironment) *UpdateService {
	roots := x509.NewCertPool()
	roots.AddCert(h.Server.Certificate())
	source, err := nodeupdate.NewHTTPS(h.Server.URL, roots, func(ctx context.Context) (string, error) { c, e := store.Load(ctx); return c.Token, e })
	if err != nil {
		h.t.Fatal(err)
	}
	fakeSource := nodeupdate.SourceFuncs{
		ManifestFunc: func(ctx context.Context, id string) (nodeupdate.Manifest, error) {
			m, e := source.Manifest(ctx, id)
			switch env.Corrupt {
			case "task":
				m.TaskID = "other"
			case "version":
				m.Version = "other"
			case "platform":
				m.Platform = "other"
			case "size":
				m.Size++
			case "hash":
				m.SHA256 = strings.Repeat("a", 64)
			}
			return m, e
		},
		ArtifactFunc: func(ctx context.Context, id string) (io.ReadCloser, error) {
			if env.Corrupt == "body" {
				return io.NopCloser(strings.NewReader("modified-artifact")), nil
			}
			return source.Artifact(ctx, id)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	apply := func(ctx context.Context, m nodeupdate.Manifest, data []byte, emit func(string)) error {
		if env.Gate != nil {
			select {
			case <-env.Gate:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Fixed paths, symlink and filesystem race properties are exercised in nodeupdate tests.
		emit("switching")
		if env.FailStartup || env.FailBusiness {
			emit("rolling_back")
			if env.Switch != nil {
				env.Switch()
			}
			emit("rolled_back")
			return nodeupdate.ErrRolledBack
		}
		if env.Switch == nil {
			return errors.New("测试更新环境未提供程序切换")
		}
		env.Switch()
		emit("reconnecting")
		return nil
	}
	manager, err := nodeupdate.OpenManager(ctx, "", fakeSource, apply)
	if err != nil {
		h.t.Fatal(err)
	}
	service := &UpdateService{Service: manager, Unavailable: env.Unavailable, cancel: cancel, manager: manager}
	h.t.Cleanup(service.Close)
	service.loseSubmitResponse.Store(env.LoseSubmitResponse)
	return service
}

// RestartVersion models installing another binary, retaining all node data and real runtime.
func (n *Node) RestartVersion(version string) { n.Stop(); n.deps.SoftwareVersion = version; n.start() }

func (h *Harness) AttachUpdater(n *Node, env UpdateEnvironment) *UpdateService {
	n.Stop()
	service := h.UpdateService(n.deps.Store, env)
	n.deps.Updater = service
	n.start()
	return service
}

func (s *UpdateService) Status(ctx context.Context) (nodeupdate.Progress, error) {
	if s.Unavailable != nil && s.Unavailable.Load() {
		return nodeupdate.Progress{}, errors.New("模拟更新服务尚未启动")
	}
	return s.Service.Status(ctx)
}
func (s *UpdateService) Submit(ctx context.Context, m nodeupdate.Manifest) (nodeupdate.Progress, error) {
	if s.Unavailable != nil && s.Unavailable.Load() {
		return nodeupdate.Progress{}, errors.New("模拟更新服务尚未启动")
	}
	p, err := s.Service.Submit(ctx, m)
	if err == nil && s.loseSubmitResponse.CompareAndSwap(true, false) {
		return nodeupdate.Progress{}, errors.New("任务已受理但响应丢失")
	}
	return p, err
}

func (s *UpdateService) Close() { s.cancel(); s.manager.Wait() }
