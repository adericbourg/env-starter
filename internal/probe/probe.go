// Package probe provides readiness probes used to decide when a started
// command is "healthy". Each Probe implementation performs a single Check
// attempt; use WaitReady to poll until success or timeout.
package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"time"
)

// Probe is the interface implemented by all readiness checks.
// Check returns nil when the resource is ready, or an error otherwise.
// A single call to Check is one attempt — it does not retry internally.
type Probe interface {
	Check(ctx context.Context) error
}

// tcpProbe dials a TCP address to verify readiness.
type tcpProbe struct {
	addr     string
	timeout  time.Duration
	interval time.Duration
}

// NewTCP returns a Probe that dials addr (host:port) over TCP.
// Each Check attempt uses timeout as the dial deadline; the connection is
// closed immediately after a successful dial.
// interval is the default poll interval used by WaitReady when its own
// interval argument is zero.
func NewTCP(addr string, timeout, interval time.Duration) Probe {
	return &tcpProbe{addr: addr, timeout: timeout, interval: interval}
}

func (p *tcpProbe) Check(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	d := net.Dialer{}
	conn, err := d.DialContext(dialCtx, "tcp", p.addr)
	if err != nil {
		return fmt.Errorf("tcp probe: dial %s: %w", p.addr, err)
	}
	_ = conn.Close()
	return nil
}

// shellProbe runs a shell command to verify readiness.
type shellProbe struct {
	command  string
	timeout  time.Duration
	interval time.Duration
}

// NewShell returns a Probe that executes command via sh -c.
// Each Check attempt uses timeout as the execution deadline (via
// exec.CommandContext); the probe is ready when the command exits with
// status 0.
// interval is the default poll interval used by WaitReady when its own
// interval argument is zero.
func NewShell(command string, timeout, interval time.Duration) Probe {
	return &shellProbe{command: command, timeout: timeout, interval: interval}
}

func (p *shellProbe) Check(ctx context.Context) error {
	cmdCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", p.command)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shell probe: command %q: %w", p.command, err)
	}
	return nil
}

// ErrTimeout is returned (wrapped) by WaitReady when the overall deadline
// elapses before the probe succeeds. Callers can distinguish it from other
// probe failures using errors.Is.
var ErrTimeout = errors.New("timed out waiting for readiness")

// defaultTimeout is used by WaitReady when timeout is zero.
// defaultInterval is used by WaitReady (and as a fallback) when interval is zero.
const (
	defaultTimeout  = 30 * time.Second
	defaultInterval = 1 * time.Second
)

// probeInterval returns the interval stored on a probe when the probe
// implements the optional intervalProvider interface, falling back to
// defaultInterval.
type intervalProvider interface {
	defaultInterval() time.Duration
}

func (p *tcpProbe) defaultInterval() time.Duration   { return p.interval }
func (p *shellProbe) defaultInterval() time.Duration { return p.interval }

// WaitReady polls p.Check every interval until it returns nil (ready),
// the overall timeout elapses, or ctx is cancelled.
//
// Defaults applied when arguments are zero:
//   - timeout  → 30 s
//   - interval → probe's own interval if non-zero, otherwise 1 s
func WaitReady(ctx context.Context, p Probe, timeout, interval time.Duration) error {
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if interval == 0 {
		if ip, ok := p.(intervalProvider); ok {
			interval = ip.defaultInterval()
		}
		if interval == 0 {
			interval = defaultInterval
		}
	}

	deadline := time.Now().Add(timeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var lastErr error
	for {
		lastErr = p.Check(ctx)
		if lastErr == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			// Distinguish between overall timeout and caller cancellation.
			if time.Now().Before(deadline) {
				return fmt.Errorf("probe: context cancelled while waiting for readiness: %w (last error: %w)", ctx.Err(), lastErr)
			}
			return fmt.Errorf("probe: %w after %s (last error: %w)", ErrTimeout, timeout, lastErr)
		case <-time.After(interval):
		}
	}
}
