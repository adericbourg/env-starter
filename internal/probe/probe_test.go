package probe

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TCP probe
// ---------------------------------------------------------------------------

func TestTCPCheck_withActiveListener_returnsNil(t *testing.T) {
	// Given: a real TCP listener on an OS-assigned port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not start listener: %v", err)
	}
	defer ln.Close()

	p := NewTCP(ln.Addr().String(), 5*time.Second, time.Second)

	// When
	got := p.Check(context.Background())

	// Then
	if got != nil {
		t.Errorf("expected nil error, got: %v", got)
	}
}

func TestTCPCheck_withNoListener_returnsError(t *testing.T) {
	// Given: a port that is definitely not listening (we bind and immediately
	// close so the OS won't reuse it within the test)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not allocate port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	p := NewTCP(addr, 200*time.Millisecond, time.Second)

	// When
	got := p.Check(context.Background())

	// Then
	if got == nil {
		t.Error("expected an error when no listener is available, got nil")
	}
}

// ---------------------------------------------------------------------------
// Shell probe
// ---------------------------------------------------------------------------

func TestShellCheck_exitZero_returnsNil(t *testing.T) {
	// Given
	p := NewShell("exit 0", 5*time.Second, time.Second)

	// When
	got := p.Check(context.Background())

	// Then
	if got != nil {
		t.Errorf("expected nil error, got: %v", got)
	}
}

func TestShellCheck_exitOne_returnsError(t *testing.T) {
	// Given
	p := NewShell("exit 1", 5*time.Second, time.Second)

	// When
	got := p.Check(context.Background())

	// Then
	if got == nil {
		t.Error("expected an error for exit 1, got nil")
	}
}

func TestShellCheck_exceedsTimeout_returnsError(t *testing.T) {
	// Given: a command that sleeps longer than the per-attempt timeout
	p := NewShell("sleep 10", 50*time.Millisecond, time.Second)

	// When
	start := time.Now()
	got := p.Check(context.Background())
	elapsed := time.Since(start)

	// Then: should fail quickly (well under 1 s) and return an error
	if got == nil {
		t.Error("expected timeout error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Check did not respect timeout: elapsed %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// WaitReady
// ---------------------------------------------------------------------------

// flipProbe is a test helper whose Check returns an error for the first
// failCount calls and nil thereafter.
type flipProbe struct {
	calls     atomic.Int64
	failCount int64
}

func (f *flipProbe) Check(_ context.Context) error {
	n := f.calls.Add(1)
	if n <= f.failCount {
		return errNotReady
	}
	return nil
}

var errNotReady = errors.New("not ready yet")

func TestWaitReady_becomesReadyAfterFewIntervals_returnsNil(t *testing.T) {
	// Given: probe that fails the first 2 calls, then succeeds
	p := &flipProbe{failCount: 2}

	// When
	err := WaitReady(context.Background(), p, 2*time.Second, 20*time.Millisecond)

	// Then
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
	if p.calls.Load() < 3 {
		t.Errorf("expected at least 3 Check calls, got %d", p.calls.Load())
	}
}

func TestWaitReady_overallTimeout_returnsError(t *testing.T) {
	// Given: probe that never becomes ready
	p := &flipProbe{failCount: 1_000_000}

	// When
	start := time.Now()
	err := WaitReady(context.Background(), p, 100*time.Millisecond, 10*time.Millisecond)
	elapsed := time.Since(start)

	// Then: must return an error and not take much longer than the timeout
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("expected errors.Is(err, ErrTimeout) to be true, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitReady ran too long: %v", elapsed)
	}
}

func TestWaitReady_contextCancelled_returnsError(t *testing.T) {
	// Given: probe that never becomes ready; cancel the context promptly
	p := &flipProbe{failCount: 1_000_000}
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	// When
	start := time.Now()
	err := WaitReady(ctx, p, 10*time.Second, 10*time.Millisecond)
	elapsed := time.Since(start)

	// Then: must return quickly with an error, but NOT as a timeout
	if err == nil {
		t.Error("expected cancellation error, got nil")
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("cancellation should not be classified as ErrTimeout, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitReady did not return promptly on cancellation: %v", elapsed)
	}
}
