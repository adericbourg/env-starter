package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/adericbourg/env-starter/internal/source"
)

// DaemonLogPath returns the path to the daemon log file:
// <source.CacheDir()>/daemon.log
func DaemonLogPath() (string, error) {
	dir, err := source.CacheDir()
	if err != nil {
		return "", fmt.Errorf("daemon: log path: %w", err)
	}
	return filepath.Join(dir, "daemon.log"), nil
}

// SocketPath returns the path to the daemon unix socket:
// <source.CacheDir()>/daemon.sock
func SocketPath() (string, error) {
	dir, err := source.CacheDir()
	if err != nil {
		return "", fmt.Errorf("daemon: socket path: %w", err)
	}
	return filepath.Join(dir, "daemon.sock"), nil
}

// LockPath returns the path to the spawn lock file:
// <source.CacheDir()>/daemon.lock
func LockPath() (string, error) {
	dir, err := source.CacheDir()
	if err != nil {
		return "", fmt.Errorf("daemon: lock path: %w", err)
	}
	return filepath.Join(dir, "daemon.lock"), nil
}

// DialOnly tries to connect to an already-running daemon.
// Returns nil, nil if no daemon is listening (connection refused, no socket file).
// Returns non-nil error only for unexpected failures (e.g. permission denied on the
// directory).
func DialOnly(socketPath string) (*ClientController, error) {
	c, err := Connect(socketPath)
	if err == nil {
		return c, nil
	}
	// Network-level errors (connection refused, no such socket) mean no daemon is
	// running — not an unexpected error.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return nil, nil
	}
	// Socket file doesn't exist.
	if os.IsNotExist(err) {
		return nil, nil
	}
	return nil, err
}

// EnsureDaemon ensures a daemon is running and returns a connected client.
// configFile and configOverlay are the --config / --config-overlay flag values
// (empty = use defaults). The daemon is started with the same flags.
//
// Flow:
//  1. Try to dial socketPath — if success, return the client.
//  2. If dial fails:
//     a. Acquire an exclusive flock on lockPath (creating the file if needed).
//     b. Retry dial — another process may have started the daemon while we waited.
//     c. If still not running: spawn the daemon as a detached background process.
//     d. Poll dial for up to 10 seconds (100×100ms), stop as soon as connected.
//     e. Release the flock.
//  3. Handle stale socket: if the socket file exists but dial gives "connection
//     refused", remove the socket file before spawning.
func EnsureDaemon(ctx context.Context, socketPath, lockPath, configFile, configOverlay string) (*ClientController, error) {
	// Step 1: try an optimistic dial first (fast path — daemon already running).
	if c, err := Connect(socketPath); err == nil {
		return c, nil
	}

	// Step 2a: acquire an exclusive flock on the lock file.
	// 0o700: this dir holds the daemon socket, lock, and log. Restricting it to
	// the owner closes the brief window between net.Listen creating the socket and
	// the server chmod'ing it to 0600 — another local user cannot even traverse in.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: ensure: create lock dir: %w", err)
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: ensure: open lock file: %w", err)
	}
	defer func() { _ = lockFile.Close() }()

	if err := lockFileExclusive(lockFile); err != nil {
		return nil, fmt.Errorf("daemon: ensure: acquire lock: %w", err)
	}
	defer unlockFile(lockFile) //nolint:errcheck

	// Step 2b: retry dial after acquiring the lock — another process may have
	// started the daemon while we waited for the lock.
	c, connectErrAfterLock := Connect(socketPath)
	if connectErrAfterLock == nil {
		return c, nil
	}

	// Step 3: handle stale socket file — remove it only when the error indicates
	// connection refused or no-such-file (i.e. the socket file is genuinely stale).
	if isStaleSocket(socketPath, connectErrAfterLock) {
		if rmErr := os.Remove(socketPath); rmErr != nil {
			return nil, fmt.Errorf("daemon: ensure: remove stale socket: %w", rmErr)
		}
	}

	// Step 2c: spawn the daemon as a detached background process.
	if err := spawnDaemon(configFile, configOverlay); err != nil {
		return nil, fmt.Errorf("daemon: ensure: spawn: %w", err)
	}

	// Step 2d: poll for up to 10 seconds until the daemon is listening.
	const (
		pollInterval = 100 * time.Millisecond
		pollAttempts = 100
	)
	for i := 0; i < pollAttempts; i++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("daemon: ensure: cancelled while waiting for daemon: %w", ctx.Err())
		case <-time.After(pollInterval):
		}
		if c, err := Connect(socketPath); err == nil {
			return c, nil
		}
	}

	logPath, _ := DaemonLogPath()
	return nil, fmt.Errorf("daemon: ensure: timed out waiting for daemon to start (check %s for errors)", logPath)
}

// isStaleSocket reports whether socketPath holds a stale socket file that should
// be removed before re-spawning. A socket is stale when the file exists but the
// connection was refused or the socket has been closed.
func isStaleSocket(socketPath string, connectErr error) bool {
	if connectErr == nil {
		return false
	}
	// Socket file must actually exist.
	if _, statErr := os.Stat(socketPath); os.IsNotExist(statErr) {
		return false
	}
	// Only remove if connection refused or socket was closed.
	var opErr *net.OpError
	if errors.As(connectErr, &opErr) {
		return errors.Is(opErr.Err, syscall.ECONNREFUSED) ||
			errors.Is(opErr.Err, syscall.ENOENT)
	}
	return false
}

// spawnDaemon launches the daemon as a detached background process.
// It uses os.Executable() to locate the current binary and passes the hidden
// "__daemon" subcommand along with any provided config flags.
func spawnDaemon(configFile, configOverlay string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}

	args := []string{"__daemon"}
	if configFile != "" {
		args = append(args, "--config", configFile)
	}
	if configOverlay != "" {
		args = append(args, "--config-overlay", configOverlay)
	}

	logPath, err := DaemonLogPath()
	if err != nil {
		return fmt.Errorf("daemon log path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("create daemon log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}

	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = logFile
	// configureDetached detaches the child from the controlling terminal so it
	// survives the parent process exiting. Implementation varies by OS.
	configureDetached(cmd)

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start daemon process: %w", err)
	}

	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()

	return nil
}
