package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/engine"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// minimalConfig returns a valid Config with one command and one environment.
// No processes are started until StartEnvironment is explicitly called.
func minimalConfig(name string) *config.Config {
	return &config.Config{
		Commands: []config.Command{
			{Name: name, Run: "echo " + name},
		},
		Environments: []config.Environment{
			{Name: "env-" + name, Workflow: []config.WorkflowStep{{Command: name}}},
		},
	}
}

// newTestEngine builds an *engine.Engine from cfg with a very short grace
// period (matches the engine_test convention).
func newTestEngine(t *testing.T, cfg *config.Config) *engine.Engine {
	t.Helper()
	eng, err := engine.New(cfg)
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	eng.GracePeriod = 100 * time.Millisecond
	return eng
}

// writeTempConfig writes YAML content to a temp file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatalf("creating temp config: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("closing temp config: %v", err)
	}
	return f.Name()
}

// newTestController builds a reloadController backed by a real engine and a
// fixed load function returning cfg. The watched path is a throwaway temp file
// created for the test (so statNewest has a valid file to inspect).
func newTestController(t *testing.T, cfg *config.Config) (*reloadController, string) {
	t.Helper()
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	loadCalls := 0
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		loadCalls++
		_ = loadCalls
		return cfg, nil
	})
	return ctrl, path
}

// ── ConfigChanged tests ───────────────────────────────────────────────────────

func TestConfigChanged_whenFileUnchanged_false(t *testing.T) {
	// Given — controller seeded with the current mtime.
	cfg := minimalConfig("alpha")
	ctrl, _ := newTestController(t, cfg)

	// When — called immediately (file has not changed).
	dirty, parseErr := ctrl.ConfigChanged()

	// Then
	if dirty {
		t.Error("expected ConfigChanged to return dirty=false for an unchanged file")
	}
	if parseErr != nil {
		t.Errorf("expected no parse error for an unchanged file, got: %v", parseErr)
	}
}

func TestConfigChanged_whenLoadErrors_returnsFalseAndError(t *testing.T) {
	// Given — load function always returns an error (simulates a mid-edit parse
	// failure after the mtime has changed).
	path := writeTempConfig(t, "# placeholder")
	cfg := minimalConfig("alpha")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return nil, fmt.Errorf("syntax error on line 3")
	})

	// Bump the file mtime so the stat gate opens.
	if err := os.WriteFile(path, []byte("# changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	dirty, parseErr := ctrl.ConfigChanged()

	// Then — parse error surfaced; dirty not set.
	if dirty {
		t.Error("expected dirty=false when load fails")
	}
	if parseErr == nil {
		t.Fatal("expected a non-nil parse error")
	}
	if !strings.Contains(parseErr.Error(), "syntax error") {
		t.Errorf("expected error to mention 'syntax error', got: %v", parseErr)
	}
	if ctrl.dirty {
		t.Error("expected dirty field to remain false when load fails")
	}
	if ctrl.parseErr == nil {
		t.Error("expected parseErr field to be set on the controller")
	}
}

func TestConfigChanged_whenFileUnchanged_persistsParseError(t *testing.T) {
	// Given — controller already has a cached parse error (from a prior scan).
	cfg := minimalConfig("alpha")
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	cachedErr := fmt.Errorf("cached yaml error")
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return nil, cachedErr
	})
	ctrl.parseErr = cachedErr

	// When — file has NOT changed (same mtime/size).
	dirty, parseErr := ctrl.ConfigChanged()

	// Then — cached error is returned without re-reading the file.
	if dirty {
		t.Error("expected dirty=false")
	}
	if parseErr == nil || parseErr.Error() != cachedErr.Error() {
		t.Errorf("expected cached parse error to be preserved, got: %v", parseErr)
	}
}

func TestConfigChanged_whenTouchedButSemanticallyEqual_false(t *testing.T) {
	// Given — the file changes on disk but the loaded config is DeepEqual to
	// the running config (e.g. a reformatted but semantically identical save).
	cfg := minimalConfig("alpha")
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		// Always returns the same config as what the engine was built from.
		return cfg, nil
	})

	// Bump mtime.
	if err := os.WriteFile(path, []byte("# no-op change"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	dirty, parseErr := ctrl.ConfigChanged()

	// Then — no dirty, no error; parse error is also cleared (file loaded OK).
	if dirty {
		t.Error("expected dirty=false for a semantically identical config")
	}
	if parseErr != nil {
		t.Errorf("expected no parse error, got: %v", parseErr)
	}
	if ctrl.parseErr != nil {
		t.Error("expected controller parseErr to be cleared after a successful load")
	}
}

func TestConfigChanged_whenSemanticChange_trueAndNilError(t *testing.T) {
	// Given — load returns a different config (new command name).
	cfg := minimalConfig("alpha")
	fresh := minimalConfig("beta")
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return fresh, nil
	})

	// Bump mtime to trigger the re-load.
	if err := os.WriteFile(path, []byte("# changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	dirty, parseErr := ctrl.ConfigChanged()

	// Then — dirty and no error.
	if !dirty {
		t.Error("expected dirty=true for a semantically different config")
	}
	if parseErr != nil {
		t.Errorf("expected no parse error, got: %v", parseErr)
	}
	if !ctrl.dirty {
		t.Error("expected dirty field to be set on the controller")
	}

	// Subsequent call with unchanged file must return the same state.
	dirty2, parseErr2 := ctrl.ConfigChanged()
	if !dirty2 {
		t.Error("expected dirty to persist on subsequent call with unchanged file")
	}
	if parseErr2 != nil {
		t.Error("expected no parse error on subsequent unchanged call")
	}
}

// ── Reload tests ──────────────────────────────────────────────────────────────

func TestReload_success_appliesConfigAndClearsDirty(t *testing.T) {
	// Given
	cfg := minimalConfig("alpha")
	fresh := minimalConfig("beta")
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return fresh, nil
	})
	ctrl.dirty = true

	// When
	err := ctrl.Reload(context.Background())

	// Then — no error; dirty cleared; engine reflects the new config.
	if err != nil {
		t.Fatalf("expected Reload to succeed, got: %v", err)
	}
	if ctrl.dirty {
		t.Error("expected dirty to be cleared after successful reload")
	}
	envs := ctrl.Environments()
	if len(envs) == 0 || envs[0].Name != "env-beta" {
		t.Errorf("expected engine to expose 'env-beta', got %v", envs)
	}
}

func TestReload_success_appliesConfigWithoutStoppingRunningCommands(t *testing.T) {
	// Given a running environment
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands: []config.Command{{Name: "svc", Type: "service", Source: config.Source{Local: dir},
			Run: fmt.Sprintf("touch %q; sleep 30", ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	defer eng.Shutdown(context.Background())
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return cfg, nil
	})

	if err := ctrl.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ctrl.CmdState("svc") != engine.CmdHealthy {
		time.Sleep(10 * time.Millisecond)
	}
	if ctrl.CmdState("svc") != engine.CmdHealthy {
		t.Fatalf("svc did not become healthy")
	}

	// When a cosmetic-only reload is applied (load returns the identical config)
	if err := ctrl.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Then the command is left running, untouched
	if ctrl.CmdState("svc") != engine.CmdHealthy {
		t.Errorf("expected svc to remain healthy across reload, got %v", ctrl.CmdState("svc"))
	}
}

func TestReload_whenLoadFails_keepsRunningConfigAndSetsParseErr(t *testing.T) {
	// Given
	cfg := minimalConfig("alpha")
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return nil, fmt.Errorf("bad yaml")
	})
	ctrl.dirty = true

	// When
	err := ctrl.Reload(context.Background())

	// Then — error returned; running config unaffected; dirty cleared; parseErr set
	// (the on-disk file is invalid, so reload is now blocked until it's fixed).
	if err == nil {
		t.Fatal("expected Reload to return an error when load fails")
	}
	if ctrl.dirty {
		t.Error("expected dirty to be cleared (file is invalid, reload unavailable)")
	}
	if ctrl.parseErr == nil {
		t.Error("expected parseErr to be set after a load failure in Reload")
	}
	envs := ctrl.Environments()
	if len(envs) == 0 || envs[0].Name != "env-alpha" {
		t.Errorf("expected engine to still expose 'env-alpha', got %v", envs)
	}
}

func TestReload_whenApplyConfigFails_leavesDirtySetAndKeepsOldConfig(t *testing.T) {
	// Given a config that will fail ApplyConfig's validation (a workflow
	// referencing an undefined command)
	cfg := minimalConfig("alpha")
	invalid := &config.Config{
		Environments: []config.Environment{{Name: "env-ghost", Workflow: []config.WorkflowStep{{Command: "ghost"}}}},
	}
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return invalid, nil
	})
	ctrl.dirty = true

	// When
	err := ctrl.Reload(context.Background())

	// Then — error returned; dirty is left untouched (still true, so the
	// banner keeps prompting); the running config is unaffected.
	if err == nil {
		t.Fatal("expected Reload to return an error when ApplyConfig fails")
	}
	if !ctrl.dirty {
		t.Error("expected dirty to remain true after an ApplyConfig failure")
	}
	envs := ctrl.Environments()
	if len(envs) == 0 || envs[0].Name != "env-alpha" {
		t.Errorf("expected engine to still expose 'env-alpha', got %v", envs)
	}
}

func TestReload_success_clearsDirtyWhileRestartsAreStillRunning(t *testing.T) {
	// Given a running command whose restart (triggered by the reload) takes
	// noticeably longer than Reload itself should take to return
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cfg := &config.Config{
		Commands: []config.Command{{Name: "svc", Type: "service", Source: config.Source{Local: dir},
			Run: fmt.Sprintf("touch %q; sleep 30", ready), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)}}},
		Environments: []config.Environment{{Name: "dev", Workflow: []config.WorkflowStep{{Command: "svc"}}}},
	}
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	defer eng.Shutdown(context.Background())

	newReady := filepath.Join(dir, "ready-new")
	fresh := &config.Config{
		Commands: []config.Command{{Name: "svc", Type: "service", Source: config.Source{Local: dir},
			Run: fmt.Sprintf("sleep 1; touch %q; sleep 30", newReady), Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", newReady)}}},
		Environments: cfg.Environments,
	}
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return fresh, nil
	})

	if err := ctrl.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && ctrl.CmdState("svc") != engine.CmdHealthy {
		time.Sleep(10 * time.Millisecond)
	}
	ctrl.dirty = true

	// When
	start := time.Now()
	if err := ctrl.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	elapsed := time.Since(start)

	// Then dirty is already cleared, well before the ~1s restart completes
	if elapsed > 200*time.Millisecond {
		t.Fatalf("expected Reload to return promptly, took %s", elapsed)
	}
	if ctrl.dirty {
		t.Error("expected dirty to be cleared immediately, not after restarts settle")
	}
}

func TestReload_eventsReturnsTheSameChannel(t *testing.T) {
	// Given — eng is mutated in place by ApplyConfig, not swapped, so
	// Events() must keep returning the same channel across a reload.
	cfg := minimalConfig("alpha")
	fresh := minimalConfig("beta")
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return fresh, nil
	})

	// When
	oldCh := ctrl.Events()
	if err := ctrl.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	newCh := ctrl.Events()

	// Then — same channel identity: subscribers never need to resubscribe.
	if oldCh != newCh {
		t.Error("expected Events() to return the same channel after Reload")
	}
}

// ── Concurrency test ──────────────────────────────────────────────────────────

func TestReloadController_concurrentReadsDuringReload(t *testing.T) {
	// Given — run this test under -race to detect data races.
	cfg := minimalConfig("alpha")
	fresh := minimalConfig("beta")
	path := writeTempConfig(t, "# placeholder")
	eng := newTestEngine(t, cfg)

	calls := 0
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		calls++
		return fresh, nil
	})

	done := make(chan struct{})
	// Concurrent readers.
	for i := 0; i < 4; i++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					ctrl.Environments()
					ctrl.Events()
				}
			}
		}()
	}

	// Reload while readers are active.
	if err := ctrl.Reload(context.Background()); err != nil {
		t.Fatalf("Reload under concurrent reads: %v", err)
	}

	close(done)
}

// ── statNewest tests ──────────────────────────────────────────────────────────

func TestStatNewest_whenAllPathsMissing_returnsError(t *testing.T) {
	// Given
	paths := []string{filepath.Join(t.TempDir(), "nonexistent.yaml")}

	// When
	_, _, err := statNewest(paths)

	// Then
	if err == nil {
		t.Error("expected an error when all paths are missing")
	}
}

func TestStatNewest_whenOnePathMissing_usesOther(t *testing.T) {
	// Given — one real file, one missing path.
	dir := t.TempDir()
	real := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "nonexistent.yaml")

	// When
	mod, size, err := statNewest([]string{real, missing})

	// Then — returns the real file's info without error.
	if err != nil {
		t.Errorf("expected no error with one valid path, got: %v", err)
	}
	if mod.IsZero() {
		t.Error("expected a non-zero mtime")
	}
	if size == 0 {
		t.Error("expected a non-zero size")
	}
}

func TestStatNewest_skipsEmptyPaths(t *testing.T) {
	// Given — the overlay path is the empty string (no overlay configured).
	dir := t.TempDir()
	real := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(real, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When
	_, _, err := statNewest([]string{real, ""})

	// Then — empty string is skipped; no error.
	if err != nil {
		t.Errorf("expected no error when one path is empty, got: %v", err)
	}
}
