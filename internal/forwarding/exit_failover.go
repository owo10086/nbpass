package forwarding

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
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
	class failureClass
	cause error
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
	return &tunnelFailure{class, err}
}
func classifyTunnel(err error) failureClass {
	var failure *tunnelFailure
	if errors.As(err, &failure) {
		return failure.class
	}
	return failureLink
}

type tunnelSelection struct {
	rule    *controlpb.ForwardRule
	clients []*tunnelClient
}

func (s *tunnelSelection) dial(ctx context.Context, cleanups ...*tunnelAttemptCleanup) (net.Conn, error) {
	cleanup := &tunnelAttemptCleanup{}
	if len(cleanups) > 0 {
		cleanup = cleanups[0]
	}
	if len(s.clients) == 0 {
		return nil, errors.New("没有已确认且健康的出口")
	}
	budget := 10 * time.Second
	if s.clients[0] != nil && s.clients[0].engine.resources.limits.DialTimeout < budget {
		budget = s.clients[0].engine.resources.limits.DialTimeout
	}
	ctx, cancel := s.clients[0].engine.tunnelTimeout(ctx, budget)
	defer cancel()
	seen := map[string]bool{}
	var last error = errors.New("没有已确认且健康的出口")
	for _, c := range s.clients {
		if c == nil {
			continue
		}
		exitID := c.currentEndpoint().NodeId
		if seen[exitID] || !c.available() {
			continue
		}
		if len(seen) == 3 || ctx.Err() != nil {
			break
		}
		seen[exitID] = true
		conn, err := c.dial(ctx, s.rule, cleanup)
		if err == nil {
			return conn, nil
		}
		last = err
		class := classifyTunnel(err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if class == failureLink {
			c.linkFailed()
		}
		if class == failureLocal || class == failureConfig {
			break
		}
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
