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

// readWitness returns the full contents of path as a string, failing the test
// if the file cannot be read.
func readWitness(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(data)
}

// liveCommand returns the *command backing name, for tests that need to call
// unexported restart primitives directly.
func liveCommand(e *Engine, name string) *command {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.commands[name]
}

func TestRestartCommandWithNewConfig_ofChangedTeardown_runsTheOldTeardownScript(t *testing.T) {
	// Given a running service whose current definition tears down by appending "old"
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	witness := filepath.Join(dir, "teardown-witness")
	oldCfg := config.Command{
		Name:      "svc",
		Type:      "service",
		Source:    localSource(dir),
		Run:       fmt.Sprintf("touch %q; sleep 30", ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
		Teardown:  fmt.Sprintf("printf 'old\\n' >> %q", witness),
	}
	cfg := &config.Config{
		Commands:     []config.Command{oldCfg},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When restarted with a new definition whose teardown appends "new" instead
	newCfg := oldCfg
	newCfg.Teardown = fmt.Sprintf("printf 'new\\n' >> %q", witness)
	e.restartCommandWithNewConfig(liveCommand(e, "svc"), newCfg)
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// Then only the OLD teardown ran so far
	if got := readWitness(t, witness); got != "old\n" {
		t.Fatalf("expected witness=%q right after restart, got %q", "old\n", got)
	}

	// When the environment is later stopped
	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}
	waitForEnv(t, e, "dev", EnvStopped, 5*time.Second)

	// Then the NEW teardown ran this time
	if got := readWitness(t, witness); got != "old\nnew\n" {
		t.Fatalf("expected witness=%q after stop, got %q", "old\nnew\n", got)
	}
}

func TestRestartCommandWithNewConfig_ofChangedRun_runsTheNewCommand(t *testing.T) {
	// Given a running service
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	witness := filepath.Join(dir, "witness")
	oldCfg := config.Command{
		Name:      "svc",
		Type:      "service",
		Source:    localSource(dir),
		Run:       fmt.Sprintf("printf 'old' >> %q; touch %q; sleep 30", witness, ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
	}
	cfg := &config.Config{
		Commands:     []config.Command{oldCfg},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When restarted with a new Run
	newReady := filepath.Join(dir, "ready-new")
	newCfg := oldCfg
	newCfg.Run = fmt.Sprintf("printf 'new' >> %q; touch %q; sleep 30", witness, newReady)
	newCfg.Readiness = &config.Readiness{Shell: fmt.Sprintf("test -f %q", newReady)}
	e.restartCommandWithNewConfig(liveCommand(e, "svc"), newCfg)
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// Then the new Run executed (old did not run again)
	if got := readWitness(t, witness); got != "oldnew" {
		t.Fatalf("expected witness=%q, got %q", "oldnew", got)
	}
}

func TestRestartCommandWithNewConfig_ofChangedSetup_reRunsSetup(t *testing.T) {
	// Given a running service with no setup steps (guards against a restart
	// primitive that reuses runDir and skips setup, like relaunch does)
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	witness := filepath.Join(dir, "setup-witness")
	oldCfg := config.Command{
		Name:      "svc",
		Type:      "service",
		Source:    localSource(dir),
		Run:       fmt.Sprintf("touch %q; sleep 30", ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
	}
	cfg := &config.Config{
		Commands:     []config.Command{oldCfg},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When restarted with a new definition that adds a setup step
	newReady := filepath.Join(dir, "ready-new")
	newCfg := oldCfg
	newCfg.Setup = []string{fmt.Sprintf("printf 'setup-ran\\n' >> %q", witness)}
	newCfg.Run = fmt.Sprintf("touch %q; sleep 30", newReady)
	newCfg.Readiness = &config.Readiness{Shell: fmt.Sprintf("test -f %q", newReady)}
	e.restartCommandWithNewConfig(liveCommand(e, "svc"), newCfg)
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// Then the new setup step ran
	if got := readWitness(t, witness); got != "setup-ran\n" {
		t.Fatalf("expected setup to have run, witness=%q", got)
	}
}

func TestRestartCommandWithNewConfig_ofChangedRestartPolicy_appliesTheNewPolicy(t *testing.T) {
	// Given a running service with the default restart policy (3 max retries)
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	oldCfg := config.Command{
		Name:      "svc",
		Type:      "service",
		Source:    localSource(dir),
		Run:       fmt.Sprintf("touch %q; sleep 30", ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
	}
	cfg := &config.Config{
		Commands:     []config.Command{oldCfg},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When restarted with a new definition raising max retries to 7
	newMax := 7
	newCfg := oldCfg
	newCfg.Restart = &config.Restart{MaxRetries: &newMax}
	e.restartCommandWithNewConfig(liveCommand(e, "svc"), newCfg)
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// Then CmdRetries reports the new max
	_, max := e.CmdRetries("svc")
	if max != 7 {
		t.Fatalf("expected new max retries 7, got %d", max)
	}
}

func TestRestartCommandWithNewConfig_preservesHolders(t *testing.T) {
	// Given a command shared by two environments
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands: []config.Command{{
			Name:      "shared",
			Type:      "service",
			Source:    localSource(dir),
			Run:       fmt.Sprintf("touch %q; sleep 30", ready),
			Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
		}},
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

	// When restarted with a changed definition
	newCfg := cfg.Commands[0]
	newCfg.Env = map[string]string{"NEW": "1"}
	e.restartCommandWithNewConfig(liveCommand(e, "shared"), newCfg)
	waitForCmd(t, e, "shared", CmdHealthy, 5*time.Second)

	// Then both environments still hold it
	e.mu.Lock()
	c := e.commands["shared"]
	_, aHolds := c.holders["a"]
	_, bHolds := c.holders["b"]
	e.mu.Unlock()
	if !aHolds || !bHolds {
		t.Fatalf("expected both envs to still hold the command, holders=%v", c.holders)
	}
}

func TestRestartCommandWithNewConfig_whenNewRunFails_endsInCmdError(t *testing.T) {
	// Given a running service
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	oldCfg := config.Command{
		Name:      "svc",
		Type:      "service",
		Source:    localSource(dir),
		Run:       fmt.Sprintf("touch %q; sleep 30", ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
	}
	cfg := &config.Config{
		Commands:     []config.Command{oldCfg},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When restarted with a new Run that exits non-zero immediately, no readiness probe
	newCfg := oldCfg
	newCfg.Run = "exit 1"
	newCfg.Readiness = nil
	e.restartCommandWithNewConfig(liveCommand(e, "svc"), newCfg)

	// Then the command ends in CmdError
	waitForCmd(t, e, "svc", CmdError, 5*time.Second)
}
