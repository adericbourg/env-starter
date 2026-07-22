package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
)

func TestStartCommand_whenReadinessAlreadyPasses_adoptsUnmanagedWithoutSpawning(t *testing.T) {
	// Given a marker that already exists before anything starts (simulating an
	// externally-running process satisfying the readiness probe), and a Run
	// script that would create a separate witness file if it ever executed.
	dir := t.TempDir()
	marker := filepath.Join(dir, "already-up")
	spawned := filepath.Join(dir, "spawned")

	if err := os.WriteFile(marker, []byte{}, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", spawned),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", marker)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the command is healthy and flagged unmanaged, without env-starter
	// ever spawning its own process, and the log carries a warning about it.
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	if !e.IsUnmanaged("svc") {
		t.Error("expected svc to be reported unmanaged")
	}
	if _, err := os.Stat(spawned); !os.IsNotExist(err) {
		t.Errorf("expected env-starter to never spawn its own process, but witness file exists (stat err=%v)", err)
	}

	logs := e.Logs("svc")
	found := false
	for _, line := range logs {
		if strings.Contains(line, "unmanaged") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected command log to contain an unmanaged warning; got: %v", logs)
	}
}

func TestStartCommand_whenNoReadiness_neverUnmanaged(t *testing.T) {
	// Given a service with no readiness probe configured at all.
	dir := t.TempDir()
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "svc", Type: "service", Source: localSource(dir), Run: "sleep 30"},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the command starts normally and is never reported unmanaged — there
	// is no health check to run "if any".
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	if e.IsUnmanaged("svc") {
		t.Error("expected svc to never be reported unmanaged without a readiness probe")
	}
}

func TestStartCommand_whenReadinessNotYetSatisfied_managedStartNotUnmanaged(t *testing.T) {
	// Given a marker that does not exist yet; only Run creates it.
	dir := t.TempDir()
	marker := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", marker),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", marker)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then env-starter performs a normal managed start, since nothing already
	// satisfied the probe before it spawned anything.
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	if e.IsUnmanaged("svc") {
		t.Error("expected svc to be managed, not unmanaged, since the probe did not pass before spawn")
	}
}

func TestSuperviseUnmanaged_whenProbeFails_takesOverWithManagedStart(t *testing.T) {
	// Given a marker that already exists (adopted as unmanaged), and a Run
	// script that recreates the marker and touches a separate witness file if
	// it ever actually executes.
	dir := t.TempDir()
	marker := filepath.Join(dir, "already-up")
	spawned := filepath.Join(dir, "spawned")

	if err := os.WriteFile(marker, []byte{}, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; touch %q; sleep 30", marker, spawned),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", marker)},
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
	if !e.IsUnmanaged("svc") {
		t.Fatal("expected svc to be adopted as unmanaged")
	}

	// When the external process's health check starts failing.
	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	// Then env-starter takes over: unmanaged clears and it spawns its own
	// process, which recreates the marker itself and becomes healthy again.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && e.IsUnmanaged("svc") {
		time.Sleep(10 * time.Millisecond)
	}
	if e.IsUnmanaged("svc") {
		t.Fatal("expected svc to no longer be unmanaged after takeover")
	}

	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	if _, err := os.Stat(spawned); err != nil {
		t.Errorf("expected env-starter to have spawned its own process: %v", err)
	}
}

func TestStopEnvironment_whenCommandUnmanaged_skipsTeardownAndKill(t *testing.T) {
	// Given a command adopted as unmanaged, with a teardown script that would
	// leave a witness file if env-starter ever ran it.
	dir := t.TempDir()
	marker := filepath.Join(dir, "already-up")
	tornDown := filepath.Join(dir, "torn-down")

	if err := os.WriteFile(marker, []byte{}, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       "sleep 30",
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", marker)},
				Teardown:  fmt.Sprintf("touch %q", tornDown),
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
	if !e.IsUnmanaged("svc") {
		t.Fatal("expected svc to be adopted as unmanaged")
	}

	// When
	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}

	// Then the command stops without running teardown or attempting to kill
	// anything env-starter never launched (there is no runDir/process of ours).
	waitForCmd(t, e, "svc", CmdStopped, 2*time.Second)
	if _, err := os.Stat(tornDown); !os.IsNotExist(err) {
		t.Errorf("expected teardown to never run for an unmanaged command (stat err=%v)", err)
	}
}
