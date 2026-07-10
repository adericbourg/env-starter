package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	for !sawStarting || !sawDone || !sawEnvRunning {
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
	for !sawStopping || !sawStopped {
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

func TestNew_whenNoCacheDirAvailable_returnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.UserCacheDir does not depend on HOME on windows")
	}
	// Given no way to resolve a per-user cache dir. The engine must refuse to
	// start rather than fall back to a shared location (e.g. os.TempDir) for
	// log files, which can contain secrets.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	// When constructing the engine.
	_, err := New(&config.Config{})

	// Then it fails.
	if err == nil {
		t.Fatal("expected an error when the user cache dir cannot be resolved")
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

// ---- Auto-restart tests -------------------------------------------------------

// shortRestart returns a *config.Restart with all timings shrunk for fast
// tests. maxRetries defaults to 3 if zero.
func shortRestart(t *testing.T, maxRetries int) *config.Restart {
	t.Helper()
	if maxRetries == 0 {
		maxRetries = 3
	}
	one := true
	base := config.Duration{Duration: 20 * time.Millisecond}
	check := config.Duration{Duration: 30 * time.Millisecond}
	return &config.Restart{
		Enabled:       &one,
		MaxRetries:    &maxRetries,
		BackoffBase:   &base,
		CheckInterval: &check,
	}
}

func TestResolveRestart_serviceDefaults_enabledThreeRetriesOneSecondBase(t *testing.T) {
	// Given a service with no restart config
	cmd := config.Command{
		Name:      "svc",
		Type:      "service",
		Readiness: &config.Readiness{Shell: "true"},
	}

	// When
	p := resolveRestart(cmd)

	// Then
	if !p.enabled {
		t.Error("expected enabled=true for service with no restart config")
	}
	if p.maxRetries != 3 {
		t.Errorf("expected maxRetries=3, got %d", p.maxRetries)
	}
	if p.backoffBase != time.Second {
		t.Errorf("expected backoffBase=1s, got %v", p.backoffBase)
	}
	if p.checkInterval != 10*time.Second {
		t.Errorf("expected checkInterval=10s, got %v", p.checkInterval)
	}
}

func TestResolveRestart_taskWithNoBlock_isDisabled(t *testing.T) {
	// Given a task with no restart block
	cmd := config.Command{
		Name: "migrate",
		Type: "task",
	}

	// When
	p := resolveRestart(cmd)

	// Then
	if p.enabled {
		t.Error("expected enabled=false for task with no restart block")
	}
}

func TestResolveRestart_taskWithBlock_isEnabled(t *testing.T) {
	// Given a task with an explicit restart block and a readiness probe
	enabled := true
	cmd := config.Command{
		Name:      "tunnel",
		Type:      "task",
		Readiness: &config.Readiness{Shell: "check.sh"},
		Restart:   &config.Restart{Enabled: &enabled},
	}

	// When
	p := resolveRestart(cmd)

	// Then
	if !p.enabled {
		t.Error("expected enabled=true for task with explicit restart block")
	}
	if p.maxRetries != 3 {
		t.Errorf("expected default maxRetries=3, got %d", p.maxRetries)
	}
	if p.checkInterval != 10*time.Second {
		t.Errorf("expected default checkInterval=10s, got %v", p.checkInterval)
	}
}

func TestResolveRestart_serviceWithNoReadiness_checkIntervalZero(t *testing.T) {
	// Given a service with no readiness probe
	cmd := config.Command{
		Name: "svc",
		Type: "service",
	}

	// When
	p := resolveRestart(cmd)

	// Then: liveness impossible without probe
	if p.checkInterval != 0 {
		t.Errorf("expected checkInterval=0 for service with no readiness probe, got %v", p.checkInterval)
	}
	// But crash-restart is still enabled
	if !p.enabled {
		t.Error("expected enabled=true even without readiness probe")
	}
}

func TestSuperviseService_whenProcessCrashesAfterHealthy_restarts(t *testing.T) {
	// Given a service that becomes healthy, then exits unexpectedly, then
	// becomes healthy again on the next run.
	dir := t.TempDir()
	readyMarker := filepath.Join(dir, "ready")
	crashOnce := filepath.Join(dir, "crashed") // created after first run so subsequent runs stay alive

	// Run script: create readyMarker, then on first run sleep briefly and exit
	// (giving the probe time to fire before the exit), on subsequent runs sleep
	// forever.
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "service",
				Source: localSource(dir),
				Run: fmt.Sprintf(
					`touch %q; if [ ! -f %q ]; then touch %q; sleep 0.5; exit 1; fi; sleep 30`,
					readyMarker, crashOnce, crashOnce,
				),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyMarker)},
				Restart:   shortRestart(t, 3),
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

	// Then: the service becomes healthy, crashes, restarts, and is healthy again.
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	// After the first crash (after 500ms sleep) a restart cycle fires.
	waitForCmd(t, e, "svc", CmdRestarting, 5*time.Second)
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)

	// Retry counter resets to 0 on success.
	attempts, _ := e.CmdRetries("svc")
	if attempts != 0 {
		t.Errorf("expected retry counter reset to 0 after success, got %d", attempts)
	}
}

func TestSuperviseService_whenLivenessProbeFailsAfterHealthy_restartsToHealthy(t *testing.T) {
	// Given a service that stays alive but whose liveness probe starts failing
	// (marker deleted) and then starts passing again (marker recreated).
	dir := t.TempDir()
	liveMarker := filepath.Join(dir, "live")
	restartedMarker := filepath.Join(dir, "restarted") // created on second run

	if err := os.WriteFile(liveMarker, []byte{}, 0o600); err != nil {
		t.Fatalf("create live marker: %v", err)
	}

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "service",
				Source: localSource(dir),
				// Stay alive; the probe checks the marker, not the process health.
				Run: fmt.Sprintf(`touch %q; sleep 30`, restartedMarker),
				Readiness: &config.Readiness{
					Shell: fmt.Sprintf("test -f %q", liveMarker),
				},
				Restart: shortRestart(t, 3),
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

	// Simulate liveness failure: remove the marker.
	if err := os.Remove(liveMarker); err != nil {
		t.Fatalf("remove live marker: %v", err)
	}

	// The liveness probe will fail; the service should start restarting.
	waitForCmd(t, e, "svc", CmdRestarting, 5*time.Second)

	// Recreate the marker so the next readiness probe passes.
	if err := os.WriteFile(liveMarker, []byte{}, 0o600); err != nil {
		t.Fatalf("recreate live marker: %v", err)
	}

	// The service should come back healthy.
	waitForCmd(t, e, "svc", CmdHealthy, 10*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
}

func TestSuperviseService_whenRestartDisabled_crashMarksError(t *testing.T) {
	// Given a service with restart.enabled=false that crashes after becoming healthy.
	dir := t.TempDir()
	readyMarker := filepath.Join(dir, "ready")

	disabled := false
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "service",
				Source: localSource(dir),
				// Create ready marker, sleep briefly so the probe fires and the
				// command reaches CmdHealthy, then exit. The sleep must exceed the
				// probe interval (20ms in tests) to ensure a reliable outcome.
				Run:       fmt.Sprintf("touch %q; sleep 0.3; exit 0", readyMarker),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyMarker)},
				Restart:   &config.Restart{Enabled: &disabled},
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

	// The service should become healthy, then crash and stay errored (no restart).
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "svc", CmdError, 5*time.Second)
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)
}

func TestAttemptRestart_whenMaxRetriesExceeded_marksCmdError(t *testing.T) {
	// Given a service that becomes healthy on its first run (by sleeping past
	// noProbeGrace), then exits. Every subsequent relaunch also exits immediately
	// (< noProbeGrace), causing each restart attempt to fail. With max-retries=2
	// and no readiness probe (so no liveness checking), the engine should give up
	// and mark the command CmdError.
	dir := t.TempDir()
	started := filepath.Join(dir, "started")

	max := 2
	base := config.Duration{Duration: 10 * time.Millisecond}
	enabled := true
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "service",
				Source: localSource(dir),
				// No readiness probe: healthy after 150ms (noProbeGrace).
				// First run: sleep 300ms (> noProbeGrace) so the command reaches
				// CmdHealthy, then exit. Subsequent runs: exit immediately (<
				// noProbeGrace), so each relaunch fails.
				Run: fmt.Sprintf(
					`if [ ! -f %q ]; then touch %q; sleep 0.3; fi; exit 1`,
					started, started,
				),
				Restart: &config.Restart{Enabled: &enabled, MaxRetries: &max, BackoffBase: &base},
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

	// Wait for all retries to be exhausted → CmdError.
	waitForCmd(t, e, "svc", CmdError, 10*time.Second)
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)

	attempts, maxRetries := e.CmdRetries("svc")
	if attempts != max {
		t.Errorf("expected %d retries recorded, got %d", max, attempts)
	}
	if maxRetries != max {
		t.Errorf("expected max=%d, got %d", max, maxRetries)
	}
}

func TestStopEnvironment_duringBackoffSleep_userStopWins(t *testing.T) {
	// Given a service that becomes healthy, exits (triggering a restart with a
	// long backoff), we stop the environment while the backoff sleep is in
	// progress. The command must end up stopped, not healthy or restarting.
	dir := t.TempDir()
	started := filepath.Join(dir, "started")

	enabled := true
	max := 3
	longBackoff := config.Duration{Duration: 10 * time.Second} // long enough to stop during
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "service",
				Source: localSource(dir),
				// No readiness probe: healthy after 150ms (noProbeGrace).
				// First run: sleep 300ms so the command reaches CmdHealthy, then exit.
				// The long backoff (10s) means we have plenty of time to call
				// StopEnvironment before the restart fires.
				Run: fmt.Sprintf(
					`if [ ! -f %q ]; then touch %q; sleep 0.3; fi; exit 1`,
					started, started,
				),
				Restart: &config.Restart{Enabled: &enabled, MaxRetries: &max, BackoffBase: &longBackoff},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}},
		},
	}

	e := newTestEngine(t, cfg)

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdRestarting, 5*time.Second)

	// Stop the environment while in the backoff sleep.
	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}

	// The command must end up stopped (not healthy or restarting).
	waitForCmd(t, e, "svc", CmdStopped, 5*time.Second)
	waitForEnv(t, e, "dev", EnvStopped, 5*time.Second)
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

// ---- Task with readiness probe tests -----------------------------------------

func TestHandleTask_withNoProbe_reachesDone(t *testing.T) {
	// Given a task with no readiness probe that exits 0.
	dir := t.TempDir()
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "migrate",
				Type:   "task",
				Source: localSource(dir),
				Run:    "exit 0",
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "migrate"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the task reaches done (not healthy) — regression guard for no-probe path.
	waitForCmd(t, e, "migrate", CmdDone, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
}

func TestHandleTask_withReadinessProbe_reachesHealthy(t *testing.T) {
	// Given a task that exits 0 and leaves a marker file that the probe checks.
	dir := t.TempDir()
	marker := filepath.Join(dir, "ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "tunnel",
				Type:   "task",
				Source: localSource(dir),
				// Exit 0 immediately while creating the marker (simulating a tunnel
				// that backgrounds itself and returns).
				Run:       fmt.Sprintf("touch %q", marker),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", marker)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "tunnel"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the task reaches healthy (not done) and the env is running.
	waitForCmd(t, e, "tunnel", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
}

func TestHandleTask_whenReadinessTimesOut_reachesTimeout(t *testing.T) {
	// Given a task whose process exits 0 but the readiness probe never passes.
	dir := t.TempDir()

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "tunnel",
				Type:   "task",
				Source: localSource(dir),
				// Exit immediately; the probe will never see a marker.
				Run: "exit 0",
				Readiness: &config.Readiness{
					Shell:   "false", // never succeeds
					Timeout: &config.Duration{Duration: 100 * time.Millisecond},
				},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "tunnel"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}

	// Then the task is marked timeout (not done).
	waitForCmd(t, e, "tunnel", CmdTimeout, 5*time.Second)
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)
}

func TestHandleTask_withReadinessProbe_unblocksDependent(t *testing.T) {
	// Given a task (tunnel) with a readiness probe, and a dependent service that
	// records whether the probe had passed before it started.
	dir := t.TempDir()
	tunnelMarker := filepath.Join(dir, "tunnel-ready")
	witness := filepath.Join(dir, "witness")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "tunnel",
				Type:   "task",
				Source: localSource(dir),
				Run:    fmt.Sprintf("touch %q", tunnelMarker),
				Readiness: &config.Readiness{
					Shell: fmt.Sprintf("test -f %q", tunnelMarker),
				},
			},
			{
				Name:   "app",
				Type:   "service",
				Source: localSource(dir),
				// Record whether the tunnel marker existed when the service started.
				Run: fmt.Sprintf(
					"if [ -f %q ]; then echo ok > %q; else echo bad > %q; fi; sleep 30",
					tunnelMarker, witness, witness,
				),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", witness)},
			},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Workflow: []config.WorkflowStep{
					{Command: "tunnel"},
					{Command: "app", DependsOn: []string{"tunnel"}},
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

	// Then the tunnel is healthy (not just done) and app started only after probe passed.
	waitForCmd(t, e, "tunnel", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "app", CmdHealthy, 5*time.Second)

	got, err := os.ReadFile(witness)
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	if string(got) != "ok\n" {
		t.Fatalf("app started before tunnel probe passed: witness=%q", string(got))
	}
}

func TestSuperviseTask_whenLivenessProbeFailsAfterHealthy_restartsToHealthy(t *testing.T) {
	// Given a task that exits 0 and passes its probe (marker exists); after the
	// task is healthy, the marker is removed (liveness fails → restart); then
	// the marker is recreated so the relaunched task passes the probe again.
	dir := t.TempDir()
	liveMarker := filepath.Join(dir, "live")

	if err := os.WriteFile(liveMarker, []byte{}, 0o600); err != nil {
		t.Fatalf("create live marker: %v", err)
	}

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "tunnel",
				Type:   "task",
				Source: localSource(dir),
				// Exit 0 immediately; the probe checks an external marker file.
				Run: "exit 0",
				Readiness: &config.Readiness{
					Shell: fmt.Sprintf("test -f %q", liveMarker),
				},
				Restart: shortRestart(t, 3),
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "tunnel"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "tunnel", CmdHealthy, 5*time.Second)

	// Simulate liveness failure: remove the marker.
	if err := os.Remove(liveMarker); err != nil {
		t.Fatalf("remove live marker: %v", err)
	}

	// The liveness probe should fail → restart cycle begins.
	waitForCmd(t, e, "tunnel", CmdRestarting, 5*time.Second)

	// Recreate the marker so the relaunched task's probe passes.
	if err := os.WriteFile(liveMarker, []byte{}, 0o600); err != nil {
		t.Fatalf("recreate live marker: %v", err)
	}

	// The task should come back healthy.
	waitForCmd(t, e, "tunnel", CmdHealthy, 10*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)

	// Retry counter resets to 0 on success.
	attempts, _ := e.CmdRetries("tunnel")
	if attempts != 0 {
		t.Errorf("expected retry counter reset to 0 after success, got %d", attempts)
	}
}

// ---- Selective retry tests ---------------------------------------------------

func TestStartEnvironment_whenRetryDegradedEnv_healthyCommandNotRestarted(t *testing.T) {
	// Given an env with a healthy service A and a failing service B (→ EnvDegraded).
	// A records every process launch in a counter file.
	dir := t.TempDir()
	counterA := filepath.Join(dir, "a-launches")
	readyA := filepath.Join(dir, "a-ready")
	fixB := filepath.Join(dir, "b-fix")
	readyB := filepath.Join(dir, "b-ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc-a",
				Type:   "service",
				Source: localSource(dir),
				// Append one byte per launch so we can count launches by file size.
				Run:       fmt.Sprintf(`printf 'x' >> %q; touch %q; sleep 30`, counterA, readyA),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyA)},
			},
			{
				Name:      "svc-b",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("test -f %q && touch %q && sleep 30", fixB, readyB),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyB)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{
				{Command: "svc-a"},
				{Command: "svc-b"},
			}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When the env is started (svc-a healthy, svc-b fails → degraded).
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (first): %v", err)
	}
	waitForCmd(t, e, "svc-a", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvDegraded, 5*time.Second)

	// Fix svc-b and retry the env.
	if err := os.WriteFile(fixB, []byte{}, 0o600); err != nil {
		t.Fatalf("create fix marker: %v", err)
	}
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (retry): %v", err)
	}

	// Then the env recovers to running, svc-b becomes healthy, and svc-a was NOT relaunched.
	waitForCmd(t, e, "svc-b", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)

	data, err := os.ReadFile(counterA)
	if err != nil {
		t.Fatalf("read svc-a counter: %v", err)
	}
	if len(data) != 1 {
		t.Errorf("svc-a launched %d time(s), expected exactly 1", len(data))
	}
}

func TestStartEnvironment_whenRetrySharedHealthyCommand_otherEnvUnaffected(t *testing.T) {
	// Given envs dev and prod sharing a healthy service A; dev also has a failing
	// service B. Retrying dev must not disturb A or prod.
	dir := t.TempDir()
	counterA := filepath.Join(dir, "a-launches")
	readyA := filepath.Join(dir, "a-ready")
	fixB := filepath.Join(dir, "b-fix")
	readyB := filepath.Join(dir, "b-ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "shared",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf(`printf 'x' >> %q; touch %q; sleep 30`, counterA, readyA),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyA)},
			},
			{
				Name:      "web",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("test -f %q && touch %q && sleep 30", fixB, readyB),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyB)},
			},
		},
		Environments: []config.Environment{
			{Name: "prod", Workflow: []config.WorkflowStep{{Command: "shared"}}},
			{Name: "dev", Workflow: []config.WorkflowStep{
				{Command: "shared"},
				{Command: "web"},
			}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// Start prod (shared becomes healthy).
	if err := e.StartEnvironment("prod"); err != nil {
		t.Fatalf("StartEnvironment prod: %v", err)
	}
	waitForEnv(t, e, "prod", EnvRunning, 5*time.Second)

	// Start dev (shared already healthy, web fails → dev degraded).
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment dev: %v", err)
	}
	waitForEnv(t, e, "dev", EnvDegraded, 5*time.Second)

	// Fix web and retry dev.
	if err := os.WriteFile(fixB, []byte{}, 0o600); err != nil {
		t.Fatalf("create fix marker: %v", err)
	}
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment dev retry: %v", err)
	}

	// Then dev reaches running, prod stays running, and shared was never relaunched.
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
	if got := e.EnvState("prod"); got != EnvRunning {
		t.Errorf("prod expected EnvRunning after dev retry, got %q", got)
	}
	if got := e.CmdState("shared"); got != CmdHealthy {
		t.Errorf("shared expected CmdHealthy after dev retry, got %q", got)
	}

	data, err := os.ReadFile(counterA)
	if err != nil {
		t.Fatalf("read shared counter: %v", err)
	}
	if len(data) != 1 {
		t.Errorf("shared launched %d time(s), expected exactly 1", len(data))
	}
}

func TestStartEnvironment_whenRetryRecoversFailedDependency_dependentStarts(t *testing.T) {
	// Given a dep chain: A (healthy) → B (fails until fixed) → C (depends on B).
	// On first start C stays pending because B fails. After fixing B and retrying,
	// C should start.
	dir := t.TempDir()
	readyA := filepath.Join(dir, "a-ready")
	fixB := filepath.Join(dir, "b-fix")
	readyB := filepath.Join(dir, "b-ready")
	readyC := filepath.Join(dir, "c-ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "svc-a",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", readyA),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyA)},
			},
			{
				Name:      "svc-b",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("test -f %q && touch %q && sleep 30", fixB, readyB),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyB)},
			},
			{
				Name:      "svc-c",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", readyC),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyC)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{
				{Command: "svc-a"},
				{Command: "svc-b"},
				{Command: "svc-c", DependsOn: []string{"svc-b"}},
			}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started before svc-b is fixed: svc-a healthy, svc-b fails, svc-c pending.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (first): %v", err)
	}
	waitForCmd(t, e, "svc-a", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "svc-b", CmdError, 5*time.Second)
	// svc-c must stay pending (dep failed, never started).
	if got := e.CmdState("svc-c"); got != CmdPending {
		t.Errorf("svc-c expected CmdPending before retry, got %q", got)
	}

	// Fix svc-b and retry.
	if err := os.WriteFile(fixB, []byte{}, 0o600); err != nil {
		t.Fatalf("create fix marker: %v", err)
	}
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (retry): %v", err)
	}

	// Then svc-b and svc-c become healthy and the env reaches running.
	waitForCmd(t, e, "svc-b", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "svc-c", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
}

func TestStartEnvironment_whenRetryAfterTaskDone_taskNotRerun(t *testing.T) {
	// Given an env with a task that has completed (CmdDone) and a failing service.
	// On retry the task must not re-run.
	dir := t.TempDir()
	counterTask := filepath.Join(dir, "task-launches")
	fixSvc := filepath.Join(dir, "svc-fix")
	readySvc := filepath.Join(dir, "svc-ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				// No readiness probe → exits 0 → CmdDone.
				Name:   "migrate",
				Type:   "task",
				Source: localSource(dir),
				// Append one byte per run.
				Run: fmt.Sprintf(`printf 'x' >> %q`, counterTask),
			},
			{
				Name:      "web",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("test -f %q && touch %q && sleep 30", fixSvc, readySvc),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readySvc)},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{
				{Command: "migrate"},
				{Command: "web"},
			}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started: migrate runs once (Done), web fails → env degraded.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (first): %v", err)
	}
	waitForCmd(t, e, "migrate", CmdDone, 5*time.Second)
	waitForEnv(t, e, "dev", EnvDegraded, 5*time.Second)

	// Fix web and retry.
	if err := os.WriteFile(fixSvc, []byte{}, 0o600); err != nil {
		t.Fatalf("create fix marker: %v", err)
	}
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment (retry): %v", err)
	}

	// Then web becomes healthy, env reaches running, and migrate was NOT re-run.
	waitForCmd(t, e, "web", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)

	if got := e.CmdState("migrate"); got != CmdDone {
		t.Errorf("migrate expected CmdDone after retry, got %q", got)
	}
	data, err := os.ReadFile(counterTask)
	if err != nil {
		t.Fatalf("read task counter: %v", err)
	}
	if len(data) != 1 {
		t.Errorf("migrate ran %d time(s), expected exactly 1", len(data))
	}
}

func TestStartEnvironment_whenRetryDownSharedCommand_relaunchesForBothEnvs(t *testing.T) {
	// Given a command shared by two envs that is driven to CmdError (liveness
	// failure exhausts retries). Retrying one env must restart the command in place
	// and recover both envs to running.
	dir := t.TempDir()
	liveMarker := filepath.Join(dir, "live")
	probeTimeout := config.Duration{Duration: 100 * time.Millisecond}

	if err := os.WriteFile(liveMarker, []byte{}, 0o600); err != nil {
		t.Fatalf("create live marker: %v", err)
	}

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "shared",
				Type:   "service",
				Source: localSource(dir),
				Run:    "sleep 30",
				Readiness: &config.Readiness{
					Shell:   fmt.Sprintf("test -f %q", liveMarker),
					Timeout: &probeTimeout,
				},
				Restart: shortRestart(t, 2),
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "shared"}}},
			{Name: "prod", Workflow: []config.WorkflowStep{{Command: "shared"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// Start both envs.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment dev: %v", err)
	}
	waitForCmd(t, e, "shared", CmdHealthy, 5*time.Second)
	if err := e.StartEnvironment("prod"); err != nil {
		t.Fatalf("StartEnvironment prod: %v", err)
	}
	waitForEnv(t, e, "prod", EnvRunning, 5*time.Second)

	// Drive shared to CmdError by removing the liveness marker (exhausts retries).
	if err := os.Remove(liveMarker); err != nil {
		t.Fatalf("remove live marker: %v", err)
	}
	waitForCmd(t, e, "shared", CmdError, 10*time.Second)
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)
	waitForEnv(t, e, "prod", EnvError, 5*time.Second)

	// Restore the marker so the in-place restart will succeed.
	if err := os.WriteFile(liveMarker, []byte{}, 0o600); err != nil {
		t.Fatalf("restore live marker: %v", err)
	}

	// Retrying dev must restart the shared command in place and recover both envs.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment dev retry: %v", err)
	}

	waitForCmd(t, e, "shared", CmdHealthy, 10*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
	waitForEnv(t, e, "prod", EnvRunning, 5*time.Second)
}

func TestReleaseCommand_whenOneOfTwoHoldersReleases_commandStaysUp(t *testing.T) {
	// Given two envs sharing a command. Releasing one env's hold must leave the
	// command healthy; releasing the second must tear it down. Pins the holders
	// balance invariant.
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

	// When both envs are started.
	if err := e.StartEnvironment("a"); err != nil {
		t.Fatalf("StartEnvironment a: %v", err)
	}
	waitForCmd(t, e, "shared", CmdHealthy, 5*time.Second)
	if err := e.StartEnvironment("b"); err != nil {
		t.Fatalf("StartEnvironment b: %v", err)
	}
	waitForEnv(t, e, "b", EnvRunning, 5*time.Second)

	// Then stopping one env leaves shared healthy.
	if err := e.StopEnvironment("a"); err != nil {
		t.Fatalf("StopEnvironment a: %v", err)
	}
	if got := e.CmdState("shared"); got != CmdHealthy {
		t.Errorf("after releasing a's hold, shared expected CmdHealthy, got %q", got)
	}

	// And stopping the other tears it down.
	if err := e.StopEnvironment("b"); err != nil {
		t.Fatalf("StopEnvironment b: %v", err)
	}
	waitForCmd(t, e, "shared", CmdStopped, 2*time.Second)
}

func TestSuperviseTask_whenMaxRetriesExceeded_marksCmdError(t *testing.T) {
	// Given a task that passes its initial probe (marker exists) but after the
	// marker is permanently removed, each restart attempt's probe fails, exhausting
	// all retries and ending in CmdError.
	dir := t.TempDir()
	liveMarker := filepath.Join(dir, "live")

	if err := os.WriteFile(liveMarker, []byte{}, 0o600); err != nil {
		t.Fatalf("create live marker: %v", err)
	}

	max := 2
	enabled := true
	base := config.Duration{Duration: 10 * time.Millisecond}
	check := config.Duration{Duration: 30 * time.Millisecond}
	probeTimeout := config.Duration{Duration: 50 * time.Millisecond}
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "tunnel",
				Type:   "task",
				Source: localSource(dir),
				Run:    "exit 0",
				Readiness: &config.Readiness{
					Shell:   fmt.Sprintf("test -f %q", liveMarker),
					Timeout: &probeTimeout,
				},
				Restart: &config.Restart{
					Enabled:       &enabled,
					MaxRetries:    &max,
					BackoffBase:   &base,
					CheckInterval: &check,
				},
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "tunnel"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "tunnel", CmdHealthy, 5*time.Second)

	// Remove the marker permanently so all restart probes fail.
	if err := os.Remove(liveMarker); err != nil {
		t.Fatalf("remove live marker: %v", err)
	}

	// All retries exhausted → CmdError.
	waitForCmd(t, e, "tunnel", CmdError, 10*time.Second)
	waitForEnv(t, e, "dev", EnvError, 5*time.Second)

	attempts, maxRetries := e.CmdRetries("tunnel")
	if attempts != max {
		t.Errorf("expected %d retries recorded, got %d", max, attempts)
	}
	if maxRetries != max {
		t.Errorf("expected max=%d, got %d", max, maxRetries)
	}
}

// ---- Shared-command status isolation -----------------------------------------

func TestRecomputeEnvsFor_whenSharedCommandChanges_leavesStoppedEnvStopped(t *testing.T) {
	// Given two environments sharing command "shared"; "env2" also needs "other".
	dir := t.TempDir()
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "shared", Type: "service", Source: localSource(dir)},
			{Name: "other", Type: "service", Source: localSource(dir)},
		},
		Environments: []config.Environment{
			{Name: "env1", Workflow: []config.WorkflowStep{{Command: "shared"}}},
			{Name: "env2", Workflow: []config.WorkflowStep{{Command: "shared"}, {Command: "other"}}},
		},
	}
	e := newTestEngine(t, cfg)

	// And "shared" is healthy, "env1" was started (active), "env2" never was.
	e.mu.Lock()
	e.commands["shared"] = &command{cfg: cfg.Commands[0], state: CmdHealthy, startDone: make(chan struct{})}
	e.mu.Unlock()
	e.setEnvState("env1", EnvStarting)

	// When a shared-command state change fans out to every referencing env.
	e.recomputeEnvsFor("shared")

	// Then the active env is recomputed (EnvStarting -> EnvRunning)...
	if got := e.EnvState("env1"); got != EnvRunning {
		t.Errorf("active env1: expected %q, got %q", EnvRunning, got)
	}
	// ...but the never-started env stays stopped (not EnvDegraded).
	if got := e.EnvState("env2"); got != EnvStopped {
		t.Errorf("unstarted env2: expected %q, got %q", EnvStopped, got)
	}
}

func TestStartEnvironment_whenSharedCommandFails_unstartedEnvStaysStopped(t *testing.T) {
	// Given a shared service that becomes healthy then exits on demand (restart
	// disabled, so an exit lands in CmdError and triggers env recomputation),
	// shared by "env1" (started) and "env2" (never started, also needs "other").
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	kill := filepath.Join(dir, "kill")
	off := false

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "shared",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; while [ ! -f %q ]; do sleep 0.02; done", ready, kill),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
				Restart:   &config.Restart{Enabled: &off},
			},
			{
				Name:      "other",
				Type:      "service",
				Source:    localSource(dir),
				Run:       "sleep 30",
				Readiness: &config.Readiness{Shell: "true"},
			},
		},
		Environments: []config.Environment{
			{Name: "env1", Workflow: []config.WorkflowStep{{Command: "shared"}}},
			{Name: "env2", Workflow: []config.WorkflowStep{{Command: "shared"}, {Command: "other"}}},
		},
	}

	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When only env1 is started.
	if err := e.StartEnvironment("env1"); err != nil {
		t.Fatalf("StartEnvironment env1: %v", err)
	}
	waitForCmd(t, e, "shared", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "env1", EnvRunning, 5*time.Second)

	// Then the never-started env2 stays stopped while shared is healthy.
	if got := e.EnvState("env2"); got != EnvStopped {
		t.Fatalf("env2 after env1 start: expected %q, got %q", EnvStopped, got)
	}

	// When the shared command fails.
	if err := os.WriteFile(kill, []byte{}, 0o600); err != nil {
		t.Fatalf("write kill marker: %v", err)
	}
	waitForCmd(t, e, "shared", CmdError, 5*time.Second)

	// Then the active env1 shows the failure...
	waitForEnv(t, e, "env1", EnvError, 5*time.Second)
	// ...but env2, never started, is still stopped (not EnvDegraded).
	if got := e.EnvState("env2"); got != EnvStopped {
		t.Errorf("env2 after shared failure: expected %q, got %q", EnvStopped, got)
	}
}

func TestAppendSubdir_whenSubdirExists_returnsJoinedPath(t *testing.T) {
	// Given
	base := t.TempDir()
	sub := filepath.Join(base, "scripts")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	// When
	got, err := appendSubdir(base, "scripts")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sub {
		t.Errorf("got %q, want %q", got, sub)
	}
}

func TestAppendSubdir_whenSubdirMissing_returnsError(t *testing.T) {
	// Given
	base := t.TempDir()

	// When
	_, err := appendSubdir(base, "nonexistent")

	// Then
	if err == nil {
		t.Fatal("expected error for missing subdir, got nil")
	}
}

func TestAppendSubdir_whenSubdirEmpty_returnsBase(t *testing.T) {
	// Given
	base := t.TempDir()

	// When
	got, err := appendSubdir(base, "")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != base {
		t.Errorf("got %q, want %q", got, base)
	}
}

func TestStopEnvironment_taskTeardownOutputAppearsInLogs(t *testing.T) {
	// Given a task with a teardown script that emits a known marker.
	dir := t.TempDir()
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:     "mytask",
				Type:     "task",
				Source:   localSource(dir),
				Run:      "exit 0",
				Teardown: "echo TEARDOWN_MARKER",
			},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "mytask"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When started (task exits quickly) then stopped.
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "mytask", CmdDone, 5*time.Second)

	if err := e.StopEnvironment("dev"); err != nil {
		t.Fatalf("StopEnvironment: %v", err)
	}
	waitForEnv(t, e, "dev", EnvStopped, 5*time.Second)

	// Then the teardown output must appear in the task's log.
	logs := e.Logs("mytask")
	found := false
	for _, line := range logs {
		if strings.Contains(line, "TEARDOWN_MARKER") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected task logs to contain TEARDOWN_MARKER; got: %v", logs)
	}
}

func TestStartEnvironment_whenSubdirMissing_logsErrorToCommandLog(t *testing.T) {
	// Given – a local source that exists but configured with a subdir that does not.
	srcDir := t.TempDir()
	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:   "svc",
				Type:   "task",
				Source: config.Source{Local: srcDir, Subdir: "nonexistent"},
				Run:    "exit 0",
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
	waitForCmd(t, e, "svc", CmdError, 5*time.Second)

	// Then – the error must appear in the command log so the user can diagnose it.
	logs := e.Logs("svc")
	found := false
	for _, line := range logs {
		if strings.Contains(line, "nonexistent") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log to contain 'nonexistent', got: %v", logs)
	}
}
