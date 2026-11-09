package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"

	"nbpass/internal/checker"
)

// 每条控制流最多八个在途任务；结果只通过专属报告发送，断流取消未完成工作。
type checkerExchange struct {
	ctx     context.Context
	deps    Dependencies
	nodeID  string
	active  map[string]bool
	results chan checker.Result
}

func newCheckerExchange(ctx context.Context, d Dependencies, id string) *checkerExchange {
	if d.CheckerLookup == nil {
		d.CheckerLookup = func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	if d.CheckerDial == nil {
		d.CheckerDial = func(ctx context.Context, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
		}
	}
	return &checkerExchange{ctx: ctx, deps: d, nodeID: id, active: map[string]bool{}, results: make(chan checker.Result, 8)}
}
func (r *checkerExchange) submit(batch [][]byte) {
	for _, data := range batch {
		var t checker.Task
		if json.Unmarshal(data, &t) != nil || len(t.ID) != 32 || t.CheckerID != r.nodeID || t.TargetID == r.nodeID || net.ParseIP(t.TargetIP) == nil || t.TargetPort < 1 || t.TargetPort > 65535 || t.ControlHost == "" || !r.deps.Clock.Now().Before(t.Deadline) || t.Deadline.Sub(t.IssuedAt) > 60*time.Second || r.active[t.ID] || len(r.active) >= 8 {
			continue
		}
		r.active[t.ID] = true
		go func() {
			result := r.execute(t)
			select {
			case r.results <- result:
			case <-r.ctx.Done():
			}
		}()
	}
}
func (r *checkerExchange) execute(t checker.Task) checker.Result {
	started := r.deps.Clock.Now()
	ctx, cancel := timeout(r.ctx, r.deps.Clock, t.Deadline.Sub(started))
	defer cancel()
	result := checker.Result{Task: t, StartedAt: started}
	deferFinish := func() checker.Result { result.FinishedAt = r.deps.Clock.Now(); return result }
	lookup, cancelLookup := timeout(ctx, r.deps.Clock, 10*time.Second)
	ips, err := r.deps.CheckerLookup(lookup, t.ControlHost)
	expired := lookup.Err() != nil
	cancelLookup()
	ip := ""
	if err == nil && !expired {
		for _, s := range ips {
			if parsed := net.ParseIP(s); parsed != nil && parsed.To4() != nil {
				ip = parsed.String()
				break
			}
		}
	}
	if ip == "" {
		result.Control.Error = "对照站点解析失败或超时"
		return deferFinish()
	}
	result.Control, _ = r.connect(ctx, net.JoinHostPort(ip, "443"))
	if !result.Control.Connected {
		return deferFinish()
	}
	target, executionError := r.connect(ctx, net.JoinHostPort(t.TargetIP, strconv.Itoa(t.TargetPort)))
	result.Target = &target
	if executionError {
		result.ExecutionError = "目标检测执行错误，不能作为不可达证据"
	}
	return deferFinish()
}
func (r *checkerExchange) connect(ctx context.Context, address string) (checker.Connection, bool) {
	started := r.deps.Clock.Now()
	call, cancel := timeout(ctx, r.deps.Clock, 5*time.Second)
	conn, err := r.deps.CheckerDial(call, address)
	expired := call.Err() != nil
	cancel()
	result := checker.Connection{Address: address, Connected: err == nil && conn != nil && !expired, DurationMS: r.deps.Clock.Now().Sub(started).Milliseconds()}
	if conn != nil {
		conn.Close()
	}
	executionError := false
	if !result.Connected {
		result.Error = "TCP 连接失败或超时"
		var network net.Error
		executionError = !expired && !errors.As(err, &network)
		for _, local := range []error{syscall.EMFILE, syscall.ENFILE, syscall.ENOBUFS, syscall.ENOMEM, syscall.EADDRNOTAVAIL, syscall.EACCES, syscall.EPERM, syscall.EINVAL, syscall.EBADF, syscall.EAFNOSUPPORT, syscall.EPROTONOSUPPORT} {
			if errors.Is(err, local) {
				executionError = true
				break
			}
		}
	}
	return result, executionError
}
