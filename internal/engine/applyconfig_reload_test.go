package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
)

func TestApplyConfig_whenActiveEnvironmentRemoved_stopsIt(t *testing.T) {
	// Given a running environment
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("touch %q; sleep 30", ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When a config removing "dev" (but keeping "svc" defined, unreferenced
	// by any environment) is applied — isolates R1a from R1b's command purge
	if err := e.ApplyConfig(&config.Config{Commands: cfg.Commands}); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then its command is stopped (not purged: it is still in the new config)
	waitForCmd(t, e, "svc", CmdStopped, 5*time.Second)
	for _, env := range e.Environments() {
		if env.Name == "dev" {
			t.Fatalf("expected dev to be gone from Environments(), got %+v", e.Environments())
		}
	}
}

func TestApplyConfig_whenStoppedEnvironmentRemoved_doesNothing(t *testing.T) {
	// Given an environment that was never started
	cfg := minimalConfigForApply("svc")
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When a config removing it is applied
	if err := e.ApplyConfig(&config.Config{}); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then there is nothing to stop: the command was never started
	if got := e.CmdState("svc"); got != CmdPending {
		t.Fatalf("expected svc to remain CmdPending, got %v", got)
	}
}

func TestApplyConfig_whenCommandRemoved_tearsItDownAndForgetsIt(t *testing.T) {
	// Given a command that was started, then fully stopped (a dormant entry
	// with zero holders survives in engine state until something recreates
	// or forgets it)
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("touch %q; sleep 30", ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
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
	waitForCmd(t, e, "svc", CmdStopped, 5*time.Second)

	// When a config removing "svc" (and its workflow step) is applied — "dev"
	// itself survives, but is stopped, so its workflow change is a no-op;
	// this isolates the explicit purge from the environment-restart path.
	newCfg := &config.Config{
		Environments: []config.Environment{{Name: "dev"}},
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then svc is forgotten entirely: CmdState reports CmdPending, the
	// default for a command absent from engine state.
	waitForCmd(t, e, "svc", CmdPending, 2*time.Second)
}

func TestApplyConfig_ofEnvironmentEnvChange_restartsOnlyAffectedCommands(t *testing.T) {
	// Given an environment with Env{K:v1} holding two commands: "a" (no own
	// Env override, so it inherits K from the environment) and "b" (overrides
	// K itself, so the environment's K never reaches its effective env)
	dir := t.TempDir()
	readyA, readyB := filepath.Join(dir, "ready-a"), filepath.Join(dir, "ready-b")
	runsA, runsB := filepath.Join(dir, "runs-a"), filepath.Join(dir, "runs-b")
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "a", Type: "service", Source: localSource(dir),
				Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runsA, readyA, readyA),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyA)}},
			{Name: "b", Type: "service", Source: localSource(dir), Env: map[string]string{"K": "fixed"},
				Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runsB, readyB, readyB),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyB)}},
		},
		Environments: []config.Environment{
			{Name: "dev", Env: map[string]string{"K": "v1"}, Workflow: []config.WorkflowStep{{Command: "a"}, {Command: "b"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "a", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "b", CmdHealthy, 5*time.Second)
	if n := runCount(t, runsA); n != 1 {
		t.Fatalf("expected a to have run once before reload, got %d", n)
	}
	if n := runCount(t, runsB); n != 1 {
		t.Fatalf("expected b to have run once before reload, got %d", n)
	}

	// When only the environment's Env map changes
	newCfg := &config.Config{
		Commands: cfg.Commands,
		Environments: []config.Environment{
			{Name: "dev", Env: map[string]string{"K": "v2"}, Workflow: []config.WorkflowStep{{Command: "a"}, {Command: "b"}}},
		},
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then only "a" (whose effective env actually changed) restarts
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runCount(t, runsA) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runCount(t, runsA); n != 2 {
		t.Fatalf("expected a to have restarted once (run count 2), got %d", n)
	}
	time.Sleep(200 * time.Millisecond) // grace period for a stray restart of b to surface
	if n := runCount(t, runsB); n != 1 {
		t.Fatalf("expected b NOT to restart (its effective env is unchanged), run count %d", n)
	}
}

func TestApplyConfig_ofWorkflowChange_restartsTheEnvironment(t *testing.T) {
	// Given a running environment with one command
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	runs := filepath.Join(dir, "runs")
	cfg := &config.Config{
		Commands: []config.Command{{Name: "svc", Type: "service", Source: localSource(dir),
			Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runs, ready, ready),
			Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
			// Removes the readiness marker on teardown: a fresh acquire after
			// a full release (see runEnvironment/acquireAndStart) probes
			// readiness via checkUnmanaged before launching anything, and
			// would otherwise "adopt" the stale marker left by the killed
			// process instead of genuinely relaunching.
			Teardown: fmt.Sprintf("rm -f %q", ready)}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When a workflow step is added
	extraReady := filepath.Join(dir, "ready-extra")
	newCfg := &config.Config{
		Commands: append(cfg.Commands, config.Command{
			Name: "extra", Type: "service", Source: localSource(dir),
			Run: fmt.Sprintf("touch %q; sleep 30", extraReady), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", extraReady)},
		}),
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}, {Command: "extra"}}}},
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then the environment (including its pre-existing command) restarted,
	// and the new step came up too
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runCount(t, runs) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runCount(t, runs); n != 2 {
		t.Fatalf("expected svc to have restarted (run count 2), got %d", n)
	}
	waitForCmd(t, e, "extra", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "dev", EnvRunning, 5*time.Second)
}

func TestApplyConfig_ofWorkflowChange_leavesCommandsHeldByAnotherEnvironmentRunning(t *testing.T) {
	// Given a command shared by two environments
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	runs := filepath.Join(dir, "runs")
	shared := config.Command{Name: "shared", Type: "service", Source: localSource(dir),
		Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runs, ready, ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}
	cfg := &config.Config{
		Commands: []config.Command{shared},
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
	if n := runCount(t, runs); n != 1 {
		t.Fatalf("expected shared to have run once, got %d", n)
	}

	// When only "a"'s workflow changes (an unrelated step is added); "b" is untouched
	extraReady := filepath.Join(dir, "ready-extra")
	newCfg := &config.Config{
		Commands: append(cfg.Commands, config.Command{
			Name: "extra", Type: "service", Source: localSource(dir),
			Run: fmt.Sprintf("touch %q; sleep 30", extraReady), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", extraReady)},
		}),
		Environments: []config.Environment{
			{Name: "a", Workflow: []config.WorkflowStep{{Command: "shared"}, {Command: "extra"}}},
			{Name: "b", Workflow: []config.WorkflowStep{{Command: "shared"}}},
		},
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	waitForCmd(t, e, "extra", CmdHealthy, 5*time.Second)
	waitForEnv(t, e, "a", EnvRunning, 5*time.Second)

	// Then "shared" was never torn down (still held by "b") and never restarted
	time.Sleep(200 * time.Millisecond)
	if n := runCount(t, runs); n != 1 {
		t.Fatalf("expected shared NOT to restart (still held by b), run count %d", n)
	}
	waitForEnv(t, e, "b", EnvRunning, 2*time.Second)
}

func TestApplyConfig_ofChangedCommand_restartsItForAllHolders(t *testing.T) {
	// Given a command shared by two environments, both otherwise unchanged
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	runs := filepath.Join(dir, "runs")
	shared := config.Command{Name: "shared", Type: "service", Source: localSource(dir),
		Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runs, ready, ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}
	cfg := &config.Config{
		Commands: []config.Command{shared},
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

	// When the shared command's own definition changes
	changed := shared
	changed.Env = map[string]string{"NEW": "1"}
	newCfg := &config.Config{Commands: []config.Command{changed}, Environments: cfg.Environments}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then it restarts exactly once, and both environments still hold it
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runCount(t, runs) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runCount(t, runs); n != 2 {
		t.Fatalf("expected shared to have restarted exactly once (run count 2), got %d", n)
	}
	waitForEnv(t, e, "a", EnvRunning, 5*time.Second)
	waitForEnv(t, e, "b", EnvRunning, 5*time.Second)
}

func TestApplyConfig_ofChangedCommand_restartsTransitiveDependentsAfterIt(t *testing.T) {
	// Given "app" depends on "db" within one environment whose own workflow
	// will not change
	dir := t.TempDir()
	order := filepath.Join(dir, "order.txt")
	readyDB, readyApp := filepath.Join(dir, "ready-db"), filepath.Join(dir, "ready-app")
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "db", Type: "service", Source: localSource(dir),
				Run:       fmt.Sprintf("printf 'db\\n' >> %q; rm -f %q; touch %q; sleep 30", order, readyDB, readyDB),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyDB)}},
			{Name: "app", Type: "service", Source: localSource(dir),
				Run:       fmt.Sprintf("printf 'app\\n' >> %q; rm -f %q; touch %q; sleep 30", order, readyApp, readyApp),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyApp)}},
		},
		Environments: []config.Environment{
			{Name: "dev", Workflow: []config.WorkflowStep{{Command: "db"}, {Command: "app", DependsOn: []string{"db"}}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "db", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "app", CmdHealthy, 5*time.Second)

	// When "db"'s definition changes ("dev"'s workflow does not)
	changedDB := cfg.Commands[0]
	changedDB.Env = map[string]string{"NEW": "1"}
	newCfg := &config.Config{Commands: []config.Command{changedDB, cfg.Commands[1]}, Environments: cfg.Environments}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then both restart, "app" strictly after "db"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(order); strings.Count(string(data), "\n") >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := readWitness(t, order)
	if got != "db\napp\ndb\napp\n" {
		t.Fatalf("expected order=%q, got %q", "db\napp\ndb\napp\n", got)
	}
}

func TestApplyConfig_ofChangedCommandInRestartedEnvironment_doesNotRestartItTwice(t *testing.T) {
	// Given an environment exclusively holding "svc"
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	runs := filepath.Join(dir, "runs")
	svc := config.Command{Name: "svc", Type: "service", Source: localSource(dir),
		Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runs, ready, ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
		// See ofWorkflowChange_restartsTheEnvironment: removes the readiness
		// marker so a fresh acquire after a full release doesn't get
		// "adopted" via checkUnmanaged instead of genuinely relaunching.
		Teardown: fmt.Sprintf("rm -f %q", ready)}
	cfg := &config.Config{
		Commands:     []config.Command{svc},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When the environment's workflow changes (an unrelated step is added)
	// AND svc's own definition changes at the same time
	extraReady := filepath.Join(dir, "ready-extra")
	changedSvc := svc
	changedSvc.Env = map[string]string{"NEW": "1"}
	newCfg := &config.Config{
		Commands: []config.Command{changedSvc, {
			Name: "extra", Type: "service", Source: localSource(dir),
			Run: fmt.Sprintf("touch %q; sleep 30", extraReady), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", extraReady)},
		}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}, {Command: "extra"}}}},
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	waitForCmd(t, e, "extra", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// Then svc restarted exactly once (the environment restart already
	// applied the new definition; R3 must not restart it again)
	time.Sleep(300 * time.Millisecond) // grace period for a stray extra restart to surface
	if n := runCount(t, runs); n != 2 {
		t.Fatalf("expected svc run count 2 (not restarted twice), got %d", n)
	}
}

func TestApplyConfig_ofChangedCommandSharedWithUnaffectedEnvironment_isNotSubsumed(t *testing.T) {
	// Given a command shared by two environments
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	runs := filepath.Join(dir, "runs")
	shared := config.Command{Name: "shared", Type: "service", Source: localSource(dir),
		Run:       fmt.Sprintf("echo run >> %q; rm -f %q; touch %q; sleep 30", runs, ready, ready),
		Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}
	cfg := &config.Config{
		Commands: []config.Command{shared},
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

	// When "a"'s workflow changes (an unrelated step added) AND "shared"'s own
	// definition changes; "b" is completely untouched — "shared" is still
	// held by "b", an environment not being restarted, so it is NOT subsumed
	extraReady := filepath.Join(dir, "ready-extra")
	changedShared := shared
	changedShared.Env = map[string]string{"NEW": "1"}
	newCfg := &config.Config{
		Commands: []config.Command{changedShared, {
			Name: "extra", Type: "service", Source: localSource(dir),
			Run: fmt.Sprintf("touch %q; sleep 30", extraReady), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", extraReady)},
		}},
		Environments: []config.Environment{
			{Name: "a", Workflow: []config.WorkflowStep{{Command: "shared"}, {Command: "extra"}}},
			{Name: "b", Workflow: []config.WorkflowStep{{Command: "shared"}}},
		},
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	waitForCmd(t, e, "extra", CmdHealthy, 5*time.Second)

	// Then "shared" still restarts (via R3, not subsumed by a's env restart)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runCount(t, runs) < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runCount(t, runs); n != 2 {
		t.Fatalf("expected shared to have restarted once (run count 2), got %d", n)
	}
	waitForEnv(t, e, "a", EnvRunning, 5*time.Second)
	waitForEnv(t, e, "b", EnvRunning, 5*time.Second)
}

func TestApplyConfig_ofStoppedEnvironment_appliesTheNewConfigOnNextStart(t *testing.T) {
	// Given an environment that is never started
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	witness := filepath.Join(dir, "witness")
	cfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("printf 'v1' >> %q; touch %q; sleep 30", witness, ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When the command's definition changes while dev is stopped
	newCfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("printf 'v2' >> %q; touch %q; sleep 30", witness, ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
		Environments: cfg.Environments,
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then nothing runs yet, but the NEW definition applies on the next start
	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
	if got := readWitness(t, witness); got != "v2" {
		t.Fatalf("expected the new definition (v2) to apply, got %q", got)
	}
}

func TestApplyConfig_whenARestartFails_returnsNilAndReportsViaEvents(t *testing.T) {
	// Given a running command, and a background reader collecting its events
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("touch %q; sleep 30", ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// Events() is never closed by the engine (Shutdown stops commands, not
	// the channel), so drain it with a stop signal of our own rather than
	// ranging over it.
	var mu sync.Mutex
	var events []Event
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case ev := <-e.Events():
				mu.Lock()
				events = append(events, ev)
				mu.Unlock()
			}
		}
	}()
	defer func() { close(stop); <-done }()

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When a config whose new Run fails immediately (no readiness probe) is applied
	newCfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: "exit 1"}},
		Environments: cfg.Environments,
	}
	start := time.Now()
	err := e.ApplyConfig(newCfg)
	elapsed := time.Since(start)

	// Then ApplyConfig itself returns nil...
	if err != nil {
		t.Fatalf("expected ApplyConfig to return nil (restart failures surface via events), got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("expected ApplyConfig to return promptly, took %s", elapsed)
	}

	// ...and the restart failure surfaces as a CmdError event
	waitForCmd(t, e, "svc", CmdError, 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, ev := range events {
		if ev.Kind == "command" && ev.Command == "svc" && ev.CmdState == CmdError {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a CmdError event for svc, got %+v", events)
	}
}

func TestApplyConfig_ofSlowRestart_returnsBeforeItCompletes(t *testing.T) {
	// Given a running command whose restart takes noticeably long
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("touch %q; sleep 30", ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)

	// When a config whose new definition takes ~1s to become ready is applied
	newReady := filepath.Join(dir, "ready-new")
	newCfg := &config.Config{
		Commands:     []config.Command{{Name: "svc", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("sleep 1; touch %q; sleep 30", newReady), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", newReady)}}},
		Environments: cfg.Environments,
	}
	start := time.Now()
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	elapsed := time.Since(start)

	// Then ApplyConfig returns well before the restart completes
	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected ApplyConfig to return promptly, took %s", elapsed)
	}
	waitForCmd(t, e, "svc", CmdHealthy, 5*time.Second)
}

func TestApplyConfig_whenDependencyGraphCyclesAcrossEnvironments_terminates(t *testing.T) {
	// Given two environments that together declare a dependency cycle across
	// them (env "a" says y depends-on x, env "b" says x depends-on y), even
	// though neither environment's own workflow is cyclic by itself
	dir := t.TempDir()
	readyX, readyY := filepath.Join(dir, "ready-x"), filepath.Join(dir, "ready-y")
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "x", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("touch %q; sleep 30", readyX), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyX)}},
			{Name: "y", Type: "service", Source: localSource(dir), Run: fmt.Sprintf("touch %q; sleep 30", readyY), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyY)}},
		},
		Environments: []config.Environment{
			{Name: "a", Workflow: []config.WorkflowStep{{Command: "x"}, {Command: "y", DependsOn: []string{"x"}}}},
			{Name: "b", Workflow: []config.WorkflowStep{{Command: "y"}, {Command: "x", DependsOn: []string{"y"}}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("a"); err != nil {
		t.Fatalf("StartEnvironment(a): %v", err)
	}
	waitForCmd(t, e, "x", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "y", CmdHealthy, 5*time.Second)
	if err := e.StartEnvironment("b"); err != nil {
		t.Fatalf("StartEnvironment(b): %v", err)
	}
	waitForEnv(t, e, "b", EnvRunning, 5*time.Second)

	// When "x"'s definition changes (both environments' workflows stay identical)
	newCfg := &config.Config{
		Commands: []config.Command{
			{Name: "x", Type: "service", Source: localSource(dir), Env: map[string]string{"NEW": "1"}, Run: fmt.Sprintf("touch %q; sleep 30", readyX), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", readyX)}},
			cfg.Commands[1],
		},
		Environments: cfg.Environments,
	}
	if err := e.ApplyConfig(newCfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}

	// Then the reload completes — does not hang on the cycle — and both
	// commands come back healthy
	waitForCmd(t, e, "x", CmdHealthy, 5*time.Second)
	waitForCmd(t, e, "y", CmdHealthy, 5*time.Second)
}
