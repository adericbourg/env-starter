//go:build !windows

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/adericbourg/env-starter/internal/engine"
	"github.com/adericbourg/env-starter/internal/source"
)

func TestSocketPath_returnsPathUnderCacheDir(t *testing.T) {
	// Given
	cacheDir, err := source.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}

	// When
	socketPath, err := SocketPath()

	// Then
	if err != nil {
		t.Fatalf("SocketPath: unexpected error: %v", err)
	}
	if !strings.HasSuffix(socketPath, "daemon.sock") {
		t.Errorf("SocketPath: want suffix %q, got %q", "daemon.sock", socketPath)
	}
	if !strings.HasPrefix(socketPath, cacheDir) {
		t.Errorf("SocketPath: want prefix %q, got %q", cacheDir, socketPath)
	}
}

func TestLockPath_returnsPathUnderCacheDir(t *testing.T) {
	// Given
	cacheDir, err := source.CacheDir()
	if err != nil {
		t.Fatalf("CacheDir: %v", err)
	}

	// When
	lockPath, err := LockPath()

	// Then
	if err != nil {
		t.Fatalf("LockPath: unexpected error: %v", err)
	}
	if !strings.HasSuffix(lockPath, "daemon.lock") {
		t.Errorf("LockPath: want suffix %q, got %q", "daemon.lock", lockPath)
	}
	if !strings.HasPrefix(lockPath, cacheDir) {
		t.Errorf("LockPath: want prefix %q, got %q", cacheDir, lockPath)
	}
}

func TestDialOnly_whenNoSocket_returnsNil(t *testing.T) {
	// Given — a path to a socket that does not exist.
	nonExistentPath := t.TempDir() + "/no-such-daemon.sock"

	// When
	client, err := DialOnly(nonExistentPath)

	// Then
	if err != nil {
		t.Errorf("DialOnly: want nil error for missing socket, got: %v", err)
	}
	if client != nil {
		t.Errorf("DialOnly: want nil client for missing socket, got non-nil")
	}
}

func TestEnsureDaemon_whenAlreadyRunning_returnsClient(t *testing.T) {
	// Given — a minimal fake daemon server running on a temp socket.
	snap := Snapshot{
		EnvStates:    map[string]engine.EnvState{},
		CmdStates:    map[string]engine.CmdState{},
		CmdRetries:   map[string][2]int{},
		Environments: nil,
		WorkflowCmds: map[string][]string{},
		LogPaths:     map[string]string{},
	}
	socketPath := startFakeServer(t, snap, nil)
	lockPath := t.TempDir() + "/daemon.lock"

	// When
	client, err := EnsureDaemon(context.Background(), socketPath, lockPath, "", "")

	// Then — a connected client is returned without spawning a new process.
	if err != nil {
		t.Fatalf("EnsureDaemon: unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("EnsureDaemon: want non-nil client, got nil")
	}
	t.Cleanup(func() { client.Detach() })
}
