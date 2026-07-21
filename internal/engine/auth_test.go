package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
)

func TestStartEnvironment_whenInteractiveAuthCommands_neverOverlap(t *testing.T) {
	// Given two independent interactive-auth tasks that atomically claim a
	// shared lock directory on entry (mkdir is atomic on POSIX filesystems)
	// and release it before exiting. If the engine lets them run concurrently,
	// the second one to reach mkdir fails and records an overlap.
	dir := t.TempDir()
	lockDir := filepath.Join(dir, "lock")
	overlapFile := filepath.Join(dir, "overlap")

	authRun := fmt.Sprintf(
		`if mkdir %q 2>/dev/null; then sleep 0.3; rmdir %q; exit 0; else echo overlap >> %q; exit 1; fi`,
		lockDir, lockDir, overlapFile,
	)

	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "login-a", Type: "task", Source: localSource(dir), Run: authRun, InteractiveAuth: true},
			{Name: "login-b", Type: "task", Source: localSource(dir), Run: authRun, InteractiveAuth: true},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Workflow: []config.WorkflowStep{
					{Command: "login-a"},
					{Command: "login-b"},
				},
			},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then both commands eventually reach done, and never overlapped.
	waitForCmd(t, e, "login-a", CmdDone, 5*time.Second)
	waitForCmd(t, e, "login-b", CmdDone, 5*time.Second)

	if _, err := os.Stat(overlapFile); !os.IsNotExist(err) {
		t.Fatalf("interactive-auth commands overlapped (lock collision detected)")
	}
}

func TestStartEnvironment_whenInteractiveAuthCommand_doesNotBlockOtherCommands(t *testing.T) {
	// Given a slow interactive-auth task and a fast, independent non-auth task.
	dir := t.TempDir()

	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "login", Type: "task", Source: localSource(dir), Run: "sleep 2; exit 0", InteractiveAuth: true},
			{Name: "other", Type: "task", Source: localSource(dir), Run: "exit 0"},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Workflow: []config.WorkflowStep{
					{Command: "login"},
					{Command: "other"},
				},
			},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then "other" reaches done quickly, without waiting on the slow auth task.
	waitForCmd(t, e, "other", CmdDone, 500*time.Millisecond)
}
