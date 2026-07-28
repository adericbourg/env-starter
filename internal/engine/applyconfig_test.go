package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
)

func TestApplyConfig_whenNilConfig_returnsError(t *testing.T) {
	// Given a running engine
	e := newTestEngine(t, minimalConfigForApply("db"))
	defer e.Shutdown(context.Background())

	// When applying a nil config
	err := e.ApplyConfig(nil)

	// Then it returns an error
	if err == nil {
		t.Fatalf("expected error for nil config, got nil")
	}
}

func TestApplyConfig_whenWorkflowReferencesUnknownCommand_returnsErrorAndKeepsOldConfig(t *testing.T) {
	// Given a running engine
	e := newTestEngine(t, minimalConfigForApply("db"))
	defer e.Shutdown(context.Background())

	// When applying a config whose workflow references an undefined command
	bad := &config.Config{
		Commands: []config.Command{{Name: "db", Type: "service", Run: "sleep 30"}},
		Environments: []config.Environment{
			{Name: "env-db", Workflow: []config.WorkflowStep{{Command: "ghost"}}},
		},
	}
	err := e.ApplyConfig(bad)

	// Then it returns an error and the old config is still in force
	if err == nil {
		t.Fatalf("expected error for undefined command reference, got nil")
	}
	envs := e.Environments()
	if len(envs) != 1 || envs[0].Name != "env-db" {
		t.Fatalf("expected old environment list to survive, got %+v", envs)
	}
	if e.WorkflowCommands("env-db")[0] != "db" {
		t.Fatalf("expected old workflow to survive")
	}
}

func TestApplyConfig_ofNewEnvironment_appearsInEnvironmentsAsStopped(t *testing.T) {
	// Given a running engine with one environment
	e := newTestEngine(t, minimalConfigForApply("db"))
	defer e.Shutdown(context.Background())

	// When a config adding a second environment is applied
	cfg := minimalConfigForApply("db")
	cfg.Commands = append(cfg.Commands, config.Command{Name: "app", Type: "service", Run: "sleep 30"})
	cfg.Environments = append(cfg.Environments, config.Environment{
		Name:     "env-app",
		Workflow: []config.WorkflowStep{{Command: "app"}},
	})
	if err := e.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then the new environment appears, stopped
	if e.EnvState("env-app") != EnvStopped {
		t.Fatalf("expected new environment to be EnvStopped, got %v", e.EnvState("env-app"))
	}
	names := map[string]bool{}
	for _, env := range e.Environments() {
		names[env.Name] = true
	}
	if !names["env-app"] {
		t.Fatalf("expected env-app to appear in Environments(), got %v", names)
	}
}

func TestApplyConfig_whenEnvironmentRemoved_disappearsFromEnvironments(t *testing.T) {
	// Given a running engine with two environments, both stopped
	cfg := minimalConfigForApply("db")
	cfg.Commands = append(cfg.Commands, config.Command{Name: "app", Type: "service", Run: "sleep 30"})
	cfg.Environments = append(cfg.Environments, config.Environment{
		Name:     "env-app",
		Workflow: []config.WorkflowStep{{Command: "app"}},
	})
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When a config removing the second environment is applied
	if err := e.ApplyConfig(minimalConfigForApply("db")); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then it no longer appears in Environments()
	for _, env := range e.Environments() {
		if env.Name == "env-app" {
			t.Fatalf("expected env-app to be gone, still present: %+v", e.Environments())
		}
	}
}

func TestApplyConfig_ofChangedDescription_isReflectedByEnvironments(t *testing.T) {
	// Given a running engine
	e := newTestEngine(t, minimalConfigForApply("db"))
	defer e.Shutdown(context.Background())

	// When a config changing only the environment's description is applied
	cfg := minimalConfigForApply("db")
	cfg.Environments[0].Description = "now with a description"
	if err := e.ApplyConfig(cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then Environments() reflects the new description
	envs := e.Environments()
	if len(envs) != 1 || envs[0].Description != "now with a description" {
		t.Fatalf("expected updated description, got %+v", envs)
	}
}

func TestApplyConfig_ofRunningEnvironment_leavesItsCommandsRunning(t *testing.T) {
	// Given a running environment whose command appends to a witness file on
	// every launch
	dir := t.TempDir()
	witness := filepath.Join(dir, "witness")
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "db",
				Type:   "service",
				Source: localSource(dir),
				Run:    fmt.Sprintf("printf 'x' >> %q; sleep 30", witness),
			},
		},
		Environments: []config.Environment{
			{Name: "env-db", Workflow: []config.WorkflowStep{{Command: "db"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("env-db"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "db", CmdHealthy, 5*time.Second)

	// When a cosmetic-only change is applied
	changed := &config.Config{
		Commands:     cfg.Commands,
		Environments: []config.Environment{{Name: "env-db", Description: "cosmetic", Workflow: []config.WorkflowStep{{Command: "db"}}}},
	}
	if err := e.ApplyConfig(changed); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then the command is still healthy and was launched exactly once
	if e.CmdState("db") != CmdHealthy {
		t.Fatalf("expected db to still be CmdHealthy, got %v", e.CmdState("db"))
	}
	got, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	if string(got) != "x" {
		t.Fatalf("expected exactly one launch (witness=%q), command was restarted", string(got))
	}
}

func TestApplyConfig_whenCalledConcurrentlyWithReaders_noRace(t *testing.T) {
	// Given a running engine
	e := newTestEngine(t, minimalConfigForApply("db"))
	defer e.Shutdown(context.Background())

	// When ApplyConfig is repeatedly called concurrently with every read-path accessor
	var wg sync.WaitGroup
	stop := make(chan struct{})
	readers := []func(){
		func() { e.Environments() },
		func() { e.WorkflowCommands("env-db") },
		func() { e.ResolveEnv("env-db", "") },
		func() { e.ResolveEnv("", "db") },
		func() { e.CmdState("db") },
		func() { e.EnvState("env-db") },
		func() { e.StoppingCommands() },
	}
	for _, r := range readers {
		wg.Add(1)
		go func(r func()) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r()
				}
			}
		}(r)
	}

	for i := 0; i < 100; i++ {
		cfg := minimalConfigForApply("db")
		cfg.Environments[0].Description = fmt.Sprintf("rev-%d", i)
		if err := e.ApplyConfig(cfg); err != nil {
			t.Fatalf("ApplyConfig: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	// Then (no assertion beyond -race not firing)
}

// minimalConfigForApply returns a valid single-command, single-environment
// config named after cmdName, distinct from tui's minimalConfig helper (which
// lives in a different package).
func minimalConfigForApply(cmdName string) *config.Config {
	return &config.Config{
		Commands: []config.Command{{Name: cmdName, Type: "service", Run: "sleep 30"}},
		Environments: []config.Environment{
			{Name: "env-" + cmdName, Workflow: []config.WorkflowStep{{Command: cmdName}}},
		},
	}
}
