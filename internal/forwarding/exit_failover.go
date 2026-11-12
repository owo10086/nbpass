package forwarding

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"sync/atomic"
	"syscall"
	"time"

	"nbpass/internal/controlpb"
)

type failureClass string

const (
	failureLink       failureClass = "entry_exit"
	failureDownstream failureClass = "downstream_next_hop"
	failureTarget     failureClass = "exit_target"
	failureLocal      failureClass = "local_resource"
	failureConfig     failureClass = "configuration_authorization"
)

type tunnelFailure struct {
	resourceOrder *ChainResourceOrder
	class         failureClass
	cause         error
	edge          *ChainFailure
}

func (e *tunnelFailure) Error() string { return string(e.class) }
func (e *tunnelFailure) Unwrap() error { return e.cause }
func failTunnel(class failureClass, err error) error {
	var existing *tunnelFailure
	if errors.As(err, &existing) {
		return err
	}
	var certError *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &certError) || errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		class = failureConfig
	}
	if errors.Is(err, errTunnelCapacity) || errors.Is(err, context.Canceled) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE) || errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.ENOMEM) || errors.Is(err, syscall.EADDRNOTAVAIL) {
		class = failureLocal
	}
	return &tunnelFailure{class: class, cause: err}
}
func classifyTunnel(err error) failureClass {
	var failure *tunnelFailure
	if errors.As(err, &failure) {
		return failure.class
	}
	return failureLink
}

type tunnelSelection struct {
	rule           *controlpb.ForwardRule
	clients        []*tunnelClient
	selected       *atomic.Pointer[controlpb.TunnelEndpoint]
	selectedClient *atomic.Pointer[tunnelClient]
}

func (s *tunnelSelection) dial(ctx context.Context, cleanups ...*tunnelAttemptCleanup) (net.Conn, error) {
	cleanup := &tunnelAttemptCleanup{}
	if len(cleanups) > 0 {
		cleanup = cleanups[0]
	}
	if len(s.clients) == 0 {
		return nil, failTunnel(failureConfig, errors.New("没有已确认且健康的出口"))
	}
	budget := 10 * time.Second
	if s.clients[0] != nil && s.clients[0].engine.resources.limits.DialTimeout < budget {
		budget = s.clients[0].engine.resources.limits.DialTimeout
	}
	ctx, cancel := s.clients[0].engine.tunnelTimeout(ctx, budget)
	defer cancel()
	seen := map[string]bool{}
	var last error
	for _, c := range s.clients {
		if c == nil {
			continue
		}
		exitID := c.currentEndpoint().NodeId
		if seen[exitID] {
			continue
		}
		if !c.available() {
			// A locally unhealthy (including recovering) authorized edge remains a
			// link failure. It must not turn a later stream into a configuration denial.
			if last == nil {
				failure := &tunnelFailure{class: failureLink, cause: errors.New("已授权下一边尚未恢复健康")}
				if s.rule.ChainId != "" {
					failure.edge = chainEdgeFailure(s.rule, c.currentEndpoint())
				}
				last = failure
			}
			continue
		}
		if len(seen) == 3 || ctx.Err() != nil {
			break
		}
		seen[exitID] = true
		ep := c.currentEndpoint()
		if s.selected != nil {
			s.selected.Store(ep)
		}
		if s.selectedClient != nil {
			s.selectedClient.Store(c)
		}
		conn, err := c.dial(ctx, s.rule, cleanup)
		if err == nil {
			c.engine.recordChainEstablishment(s.rule, ep)
			return conn, nil
		}
		class := classifyTunnel(err)
		if s.rule.ChainId != "" {
			var failure *tunnelFailure
			if !errors.As(err, &failure) || failure.edge == nil {
				err = &tunnelFailure{class: class, cause: err, edge: chainEdgeFailure(s.rule, ep)}
			}
			c.engine.recordChainFailure(s.rule, err)
		}
		last = err
		if ctx.Err() != nil {
			return nil, last
		}
		if class == failureLink {
			c.linkFailed()
		}
		if class == failureLocal || class == failureConfig {
			break
		}
	}
	if last == nil {
		if ctx.Err() != nil {
			return nil, failTunnel(failureLocal, ctx.Err())
		}
		return nil, failTunnel(failureConfig, errors.New("没有已确认且获授权的出口"))
	}
	return nil, last
}

func (c *tunnelClient) available() bool {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	return !c.unhealthy
}
func (c *tunnelClient) linkFailed() {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	c.unhealthy = true
	c.recoverySince = time.Time{}
	c.failureEpoch++
	c.failure = failureLink
}
