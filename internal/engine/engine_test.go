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

// newTestEngine builds an engine with shrunk grace/probe durations for fast,
// deterministic tests.
func newTestEngine(t *testing.T, cfg *config.Config) *Engine {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.GracePeriod = 500 * time.Millisecond
	e.ProbeTimeout = 2 * time.Second
	e.ProbeInterval = 20 * time.Millisecond
	return e
}

// localSource returns a Source pointing at dir.
func localSource(dir string) config.Source {
	return config.Source{Local: dir}
}

// waitForCmd polls until command reaches want or the deadline elapses.
func waitForCmd(t *testing.T, e *Engine, command string, want CmdState, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if e.CmdState(command) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("command %q: expected state %q, got %q within %s", command, want, e.CmdState(command), within)
}

// waitForEnv polls until env reaches want or the deadline elapses.
func waitForEnv(t *testing.T, e *Engine, env string, want EnvState, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if e.EnvState(env) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("environment %q: expected state %q, got %q within %s", env, want, e.EnvState(env), within)
}

func TestStartEnvironment_whenDependencyChain_dependentStartsAfterDependencyHealthy(t *testing.T) {
	// Given a dir and two commands where "app" depends on "db"; "db" only
	// becomes healthy once it creates a marker file, and "app" records the
	// order by writing to a witness file.
	dir := t.TempDir()
	marker := filepath.Join(dir, "db-ready")
	witness := filepath.Join(dir, "witness")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "db",
				Type:   "service",
				Source: localSource(dir),
				// Wait a beat, create the marker, then stay alive.
				Run:       fmt.Sprintf("sleep 0.2; touch %q; sleep 30", marker),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", marker)},
			},
			{
				Name:   "app",
				Type:   "service",
				Source: localSource(dir),
				// Record whether the marker already existed when app started.
				Run:       fmt.Sprintf("if [ -f %q ]; then echo ok > %q; else echo bad > %q; fi; sleep 30", marker, witness, witness),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", witness)},
			},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Workflow: []config.WorkflowStep{
					{Command: "db"},
					{Command: "app", DependsOn: []string{"db"}},
				},
			},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When the environment is started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then both commands become healthy and app started only after db was ready.
	waitForCmd(t, e, "db", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "app", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)

	got, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	if string(got) != "ok\n" {
		t.Fatalf("app started before db was healthy: witness=%q", string(got))
	}
}

func TestStartEnvironment_serviceWithProbeAndTask_reachHealthyAndDone(t *testing.T) {
	// Given a service with a TCP-less shell probe and a task that exits 0.
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
			{
				Name:   "migrate",
				Type:   "task",
				Source: localSource(dir),
				Run:    "exit 0",
			},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Workflow: []config.WorkflowStep{
					{Command: "svc"},
					{Command: "migrate"},
				},
			},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the service reaches healthy and the task reaches done.
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "migrate", CmdDone, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
}

func TestStartEnvironment_whenCommandFails_dependentStaysPendingEnvDegraded(t *testing.T) {
	// Given a failing service "bad" and a dependent "dependent"; plus an
	// independent healthy service "good" so the env is degraded, not error.
	dir := t.TempDir()
	ready := filepath.Join(dir, "good-ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "bad",
				Type:   "service",
				Source: localSource(dir),
				Run:    "exit 1",
			},
			{
				Name:   "dependent",
				Type:   "service",
				Source: localSource(dir),
				Run:    "sleep 30",
			},
			{
				Name:      "good",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", ready),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
			},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Workflow: []config.WorkflowStep{
					{Command: "bad"},
					{Command: "dependent", DependsOn: []string{"bad"}},
					{Command: "good"},
				},
			},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then bad errors, dependent stays pending, good is healthy, env degraded.
	waitForCmd(t, e, "bad", CmdError, 5*time.Second)
	waitForCmd(t, e, "good", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvDegraded, 5*time.Second)

	if got := e.CmdState("dependent"); got != CmdPending {
		t.Fatalf("dependent: expected %q, got %q", CmdPending, got)
	}
}

func TestStartEnvironment_whenAllFail_envError(t *testing.T) {
	// Given a single service that exits non-zero immediately.
	dir := t.TempDir()
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "bad",
				Type:   "service",
				Source: localSource(dir),
				Run:    "exit 1",
			},
		},
		Environments: []config.Environment{
			{
				Name:     "dev",
				Workflow: []config.WorkflowStep{{Command: "bad"}},
			},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the command errors and the environment is error.
	waitForCmd(t, e, "bad", CmdError, 5*time.Second)
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)
}

func TestStopEnvironment_stopsRunningService(t *testing.T) {
	// Given a long-running service that writes a heartbeat file we can watch.
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("echo $$ > %q; sleep 30", pidFile),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", pidFile)},
			},
		},
		Environments: []config.Environment{
			{
				Name:     "dev",
				Workflow: []config.WorkflowStep{{Command: "svc"}},
			},
		},
	}

	e := newTestEngine(t, cfg)

	// When started then stopped.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}

	// Then the command becomes stopped and the env stopped.
	waitForCmd(t, e, "svc", CmdStopped, 2*time.Second)
	waitForEnv(t, e, "dev", EnvStopped, 2*time.Second)
}

func TestStopEnvironment_runsServiceTeardown(t *testing.T) {
	// Given a service with a teardown marker command.
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	tornFile := filepath.Join(dir, "torn")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", readyFile),
				Teardown:  fmt.Sprintf("touch %q", tornFile),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyFile)},
			},
		},
		Environments: []config.Environment{
			{
				Name:     "dev",
				Workflow: []config.WorkflowStep{{Command: "svc"}},
			},
		},
	}

	e := newTestEngine(t, cfg)

	// When started then stopped.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}

	// Then the teardown ran and the command reaches stopped.
	waitForCmd(t, e, "svc", CmdStopped, 5*time.Second)
	if _, err := os.Stat(tornFile); os.IsNotExist(err) {
		t.Error("teardown was not run for service: torn marker file missing")
	}
}

func TestReferenceCounting_sharedCommandStaysUpUntilLastEnvironmentStops(t *testing.T) {
	// Given two environments that share command "shared".
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

	// When both environments are started.
	if err := e.StartEnvironment("a"); err != nil {
		t.Fatalf("StartEnvironment a: %v", err)
	}
	waitForCmd(t, e, "shared", CmdHealthy, 5*time.Second)
	if err := e.StartEnvironment("b"); err != nil {
		t.Fatalf("StartEnvironment b: %v", err)
	}
	waitForEnv(t, e, "b", EnvRunning, 5*time.Second)

	// Then stopping one keeps the shared command healthy.
	if err := e.StopEnvironment("a"); err != nil {
		t.Fatalf("StopEnvironment a: %v", err)
	}
	if got := e.CmdState("shared"); got != CmdHealthy {
		t.Fatalf("after stopping a, shared expected %q, got %q", CmdHealthy, got)
	}

	// And stopping the other stops the shared command.
	if err := e.StopEnvironment("b"); err != nil {
		t.Fatalf("StopEnvironment b: %v", err)
	}
	waitForCmd(t, e, "shared", CmdStopped, 2*time.Second)
}

func TestEvents_emittedForStateTransitions(t *testing.T) {
	// Given a task that exits 0.
	dir := t.TempDir()
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "t",
				Type:   "task",
				Source: localSource(dir),
				Run:    "exit 0",
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "t"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started and we drain events until the task is done.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then we observe the starting and done command transitions, plus a running
	// environment transition.
	sawStarting := false
	sawDone := false
	sawEnvRunning := false
	deadline := time.After(5 * time.Second)
	for !(sawStarting && sawDone && sawEnvRunning) {
		select {
		case ev := <-e.Events():
			if ev.Kind == "command" && ev.Command == "t" {
				switch ev.CmdState {
				case CmdStarting:
					sawStarting = true
				case CmdDone:
					sawDone = true
				}
			}
			if ev.Kind == "environment" && ev.Environment == "dev" && ev.EnvState == EnvRunning {
				sawEnvRunning = true
			}
		case <-deadline:
			t.Fatalf("missing transitions: starting=%v done=%v envRunning=%v", sawStarting, sawDone, sawEnvRunning)
		}
	}
}

func TestStopEnvironment_whenStopping_transitionsThroughStopping(t *testing.T) {
	// Given a long-running service with a readiness probe.
	dir := t.TempDir()
	ready := dir + "/ready"

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc",
				Type:      "service",
				Source:    localSource(dir),
				Run:       "touch " + ready + "; sleep 30",
				Readiness: &config.Readiness{Shell: "test -f " + ready},
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

	// Drain any queued events before stopping.
	for len(e.Events()) > 0 {
		<-e.Events()
	}

	// When stopped.
	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}

	// Then we observe a CmdStopping event before CmdStopped.
	sawStopping := false
	sawStopped := false
	deadline := time.After(5 * time.Second)
	for !(sawStopping && sawStopped) {
		select {
		case ev := <-e.Events():
			if ev.Kind == "command" && ev.Command == "svc" {
				switch ev.CmdState {
				case CmdStopping:
					sawStopping = true
				case CmdStopped:
					sawStopped = true
				}
			}
		case <-deadline:
			t.Fatalf("missing transitions: stopping=%v stopped=%v", sawStopping, sawStopped)
		}
	}
	if sawStopped && !sawStopping {
		t.Error("expected CmdStopping before CmdStopped")
	}
}

func TestStartEnvironment_whenUnknownEnv_returnsError(t *testing.T) {
	// Given an engine with no matching environment.
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "x", Type: "task", Source: config.Source{Local: t.TempDir()}, Run: "exit 0"},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "x"}}},
		},
	}
	e := newTestEngine(t, cfg)

	// When starting an unknown environment.
	err := e.StartEnvironment("nope")

	// Then an error is returned.
	if err == nil {
		t.Fatalf("expected error for unknown environment")
	}
}

func TestNew_whenEnvReferencesUnknownCommand_returnsError(t *testing.T) {
	// Given a config whose workflow references a non-existent command.
	cfg := &config.Config{
		Commands: []config.Command{},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "ghost"}}},
		},
	}

	// When constructing the engine.
	_, err := New(cfg)

	// Then it fails.
	if err == nil {
		t.Fatalf("expected error for unknown command reference")
	}
}

// ---- Setup tests ------------------------------------------------------------

func TestStartCommand_whenSetupSucceeds_runsSetupThenRun(t *testing.T) {
	// Given a service whose setup creates a marker file; run asserts the marker
	// exists so if setup ran first the service becomes healthy, otherwise it exits 1.
	dir := t.TempDir()
	marker := filepath.Join(dir, "setup.done")
	ready := filepath.Join(dir, "run.ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "web",
				Type:   "service",
				Source: localSource(dir),
				Setup:  []string{fmt.Sprintf("touch %q", marker)},
				Run:    fmt.Sprintf("test -f %q && touch %q && sleep 30", marker, ready),
				Readiness: &config.Readiness{
					Shell: fmt.Sprintf("test -f %q", ready),
				},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "web"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When the environment is started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the command reaches healthy (which means setup ran, run found the marker,
	// and the readiness file was created).
	waitForCmd(t, e, "web", CmdHealthy, 5*time.Second)

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("setup marker not found — setup did not run: %v", err)
	}
}

func TestStartCommand_whenSetupFails_commandErrorsAndRunNeverStarts(t *testing.T) {
	// Given a service whose setup exits non-zero; run would create a witness file
	// if it ever starts (it should not).
	dir := t.TempDir()
	witness := filepath.Join(dir, "run.started")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "web",
				Type:   "service",
				Source: localSource(dir),
				Setup:  []string{"exit 1"},
				Run:    fmt.Sprintf("touch %q; sleep 30", witness),
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "web"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When the environment is started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the command errors and run never started.
	waitForCmd(t, e, "web", CmdError, 5*time.Second)

	if _, err := os.Stat(witness); err == nil {
		t.Error("run.started exists — run was unexpectedly executed after failed setup")
	}
}

func TestStartCommand_whenMultipleSetupSteps_runInOrder(t *testing.T) {
	// Given a service with two setup steps each appending a word to a witness file.
	dir := t.TempDir()
	witness := filepath.Join(dir, "order.txt")
	ready := filepath.Join(dir, "ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "web",
				Type:   "service",
				Source: localSource(dir),
				Setup: []string{
					fmt.Sprintf("printf 'first\\n' >> %q", witness),
					fmt.Sprintf("printf 'second\\n' >> %q", witness),
				},
				Run: fmt.Sprintf("touch %q; sleep 30", ready),
				Readiness: &config.Readiness{
					Shell: fmt.Sprintf("test -f %q", ready),
				},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "web"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When the environment is started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	waitForCmd(t, e, "web", CmdHealthy, 5*time.Second)

	// Then the witness file contains both steps in order.
	got, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	want := "first\nsecond\n"
	if string(got) != want {
		t.Errorf("setup order wrong: got %q, want %q", string(got), want)
	}
}

// ---- Restart-after-failure tests --------------------------------------------

func TestStartEnvironment_whenRestartingFailedEnv_recoversToRunning(t *testing.T) {
	// Given a service that fails on first start (marker absent → exits 1 before
	// healthy), but would succeed once the marker is created.
	dir := t.TempDir()
	marker := filepath.Join(dir, "fixed")
	ready := filepath.Join(dir, "ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "service",
				Source: localSource(dir),
				// Exits 1 immediately if the marker does not exist yet.
				Run: fmt.Sprintf("test -f %q && touch %q && sleep 30", marker, ready),
				Readiness: &config.Readiness{
					Shell: fmt.Sprintf("test -f %q", ready),
				},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started before the issue is fixed — env should become error.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (first): %v", err)
	}
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)

	// Simulate fixing the issue (create the marker file).
	if err := os.WriteFile(marker, []byte{}, 0o600); err != nil {
		t.Fatalf("create marker: %v", err)
	}

	// When started again after the fix.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (retry): %v", err)
	}

	// Then the environment recovers to running and the command becomes healthy.
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
}

func TestStartEnvironment_whenReadinessTimesOut_marksTimeout(t *testing.T) {
	// Given a service whose readiness probe never passes and whose timeout is
	// deliberately short so the test runs quickly.
	dir := t.TempDir()

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "service",
				Source: localSource(dir),
				// Stay alive so the probe timeout fires (not an early-exit error).
				Run: "sleep 30",
				Readiness: &config.Readiness{
					Shell:   "false", // never succeeds
					Timeout: &config.Duration{Duration: 100 * time.Millisecond},
				},
			},
		},
		Environments: []config.Environment{
			{
				Name:     "dev",
				Workflow: []config.WorkflowStep{{Command: "svc"}},
			},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When the environment is started.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the command is marked timeout (not error) and the env is error.
	waitForCmd(t, e, "svc", CmdTimeout, 5*time.Second)
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)
}
