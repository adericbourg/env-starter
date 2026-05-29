package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
)

func TestStoppingCommands_whenNothingStopping_returnsEmpty(t *testing.T) {
	// Given an engine with no running commands.
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "svc", Type: "service", Source: localSource(t.TempDir()), Run: "sleep 30"},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	got := e.StoppingCommands()

	// Then
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestStoppingCommands_whenCommandStopping_reportsElapsedAndGrace(t *testing.T) {
	// Given a running service that ignores SIGINT so terminate() must wait the
	// full GracePeriod before sending SIGKILL — giving us time to observe it in
	// StoppingCommands while the kill-grace window is still open.
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; trap '' INT; sleep 30", ready),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When the environment is stopped (stopStartedAt is set before the slow
	// terminate() call so StoppingCommands captures it while the wait is in flight).
	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}

	// Poll until the command appears in StoppingCommands (the goroutine setting
	// stopStartedAt runs asynchronously).
	var stopping []StoppingCommand
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stopping = e.StoppingCommands()
		if len(stopping) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Then the stopping command is reported with the correct grace and a
	// non-negative elapsed duration.
	if len(stopping) != 1 {
		t.Fatalf("expected 1 stopping command, got %d: %v", len(stopping), stopping)
	}
	sc := stopping[0]
	if sc.Command != "svc" {
		t.Errorf("expected command %q, got %q", "svc", sc.Command)
	}
	if sc.Grace != e.GracePeriod {
		t.Errorf("expected grace %v, got %v", e.GracePeriod, sc.Grace)
	}
	if sc.Elapsed < 0 {
		t.Errorf("expected non-negative elapsed, got %v", sc.Elapsed)
	}
}

func TestStoppingCommands_afterCommandStopped_disappearsFromList(t *testing.T) {
	// Given a running service that exits cleanly on SIGINT.
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", ready),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When the environment is stopped and we wait for it to finish.
	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdStopped, 3*time.Second)

	// Then the command no longer appears in StoppingCommands.
	got := e.StoppingCommands()
	if len(got) != 0 {
		t.Errorf("expected empty after stop, got %v", got)
	}
}
