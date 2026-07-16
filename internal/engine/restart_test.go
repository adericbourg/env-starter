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

func boolPtr(b bool) *bool { return &b }

// runCount returns the number of newline-terminated entries in path, or 0 if
// the file does not exist yet.
func runCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	var n int
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	return n
}

func TestRestartCommand_ofHealthyService_relaunchesAndStaysHealthy(t *testing.T) {
	// Given a running, healthy service that records one line per launch.
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	runs := filepath.Join(dir, "runs")
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runs, ready, ready),
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
	if got := runCount(t, runs); got != 1 {
		t.Fatalf("expected 1 run before restart, got %d", got)
	}

	// When the command is restarted manually.
	if err := e.RestartCommand("svc"); err != nil {
		t.Fatalf("RestartCommand: %v", err)
	}

	// Then it relaunches a fresh process and settles back to healthy.
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runCount(t, runs) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runCount(t, runs); got != 2 {
		t.Fatalf("expected 2 runs after restart, got %d", got)
	}
}

func TestRestartCommand_whenRestartDisabled_stillRestarts(t *testing.T) {
	// Given a service with auto-restart explicitly disabled.
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	runs := filepath.Join(dir, "runs")
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runs, ready, ready),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
				Restart:   &config.Restart{Enabled: boolPtr(false)},
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

	// When restarted manually despite the policy being disabled.
	if err := e.RestartCommand("svc"); err != nil {
		t.Fatalf("RestartCommand: %v", err)
	}

	// Then it still relaunches and becomes healthy again.
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runCount(t, runs) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runCount(t, runs); got != 2 {
		t.Fatalf("expected 2 runs after restart, got %d", got)
	}
}

func TestRestartCommand_preservesHolders(t *testing.T) {
	// Given a command shared by two environments, both running.
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "shared",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", ready),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
			},
		},
		Environments: []config.Environment{
			{Name: "a", Workflow: []config.WorkflowStep{{Command: "shared"}}},
			{Name: "b", Workflow: []config.WorkflowStep{{Command: "shared"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("a"); err != nil {
		t.Fatalf("StartEnvironment(a): %v", err)
	}
	waitForCmd(t, e, "shared", CmdHealthy, 5*time.Second)
	if err := e.StartEnvironment("b"); err != nil {
		t.Fatalf("StartEnvironment(b): %v", err)
	}
	waitForEnv(t, e, "b", EnvRunning, 5*time.Second)

	// When the shared command is restarted manually.
	if err := e.RestartCommand("shared"); err != nil {
		t.Fatalf("RestartCommand: %v", err)
	}
	waitForCmd(t, e, "shared", CmdHealthy, 5*time.Second)

	// Then both environments still hold it — neither was torn down.
	e.mu.Lock()
	c := e.commands["shared"]
	_, aHolds := c.holders["a"]
	_, bHolds := c.holders["b"]
	e.mu.Unlock()
	if !aHolds || !bHolds {
		t.Fatalf("expected both envs to still hold the command, holders=%v", c.holders)
	}
	waitForEnv(t, e, "a", EnvRunning, 2*time.Second)
	waitForEnv(t, e, "b", EnvRunning, 2*time.Second)
}

func TestRestartCommand_ofUnknownCommand_returnsError(t *testing.T) {
	// Given an engine with no commands started.
	cfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(t.TempDir()), Run: "sleep 30"}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When/Then restarting an unknown command is an error.
	if err := e.RestartCommand("does-not-exist"); err == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
}

func TestRestartCommand_ofStoppedCommand_returnsError(t *testing.T) {
	// Given a command that was started then fully stopped (no holders left).
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
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdStopped, 3*time.Second)

	// When/Then restarting a command with no holders is an error.
	if err := e.RestartCommand("svc"); err == nil {
		t.Fatal("expected error for unheld command, got nil")
	}
}
