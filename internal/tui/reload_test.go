package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	// Then
	if ctrl.ConfigChanged() {
		t.Error("expected ConfigChanged to return false for an unchanged file")
	}
}

func TestConfigChanged_whenLoadErrors_false(t *testing.T) {
	// Given — load function always returns an error (simulates a mid-edit parse
	// failure after the mtime has changed).
	path := writeTempConfig(t, "# placeholder")
	cfg := minimalConfig("alpha")
	eng := newTestEngine(t, cfg)
	ctrl := NewReloadController(eng, cfg, []string{path}, func() (*config.Config, error) {
		return nil, fmt.Errorf("syntax error")
	})

	// Bump the file mtime so the stat gate opens.
	if err := os.WriteFile(path, []byte("# changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	// When / Then — parse error must not set dirty.
	if ctrl.ConfigChanged() {
		t.Error("expected ConfigChanged to return false when load fails")
	}
	if ctrl.dirty {
		t.Error("expected dirty to remain false when load fails")
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

	// When / Then
	if ctrl.ConfigChanged() {
		t.Error("expected ConfigChanged to return false for a semantically identical config")
	}
}

func TestConfigChanged_whenSemanticChange_trueAndLatches(t *testing.T) {
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
	changed := ctrl.ConfigChanged()

	// Then — first call returns true and latches dirty.
	if !changed {
		t.Error("expected ConfigChanged to return true for a semantically different config")
	}
	if !ctrl.dirty {
		t.Error("expected dirty to be latched after a semantic change")
	}

	// Subsequent calls return true without re-loading (file unchanged now).
	if !ctrl.ConfigChanged() {
		t.Error("expected ConfigChanged to remain true once latched")
	}
}

// ── Reload tests ──────────────────────────────────────────────────────────────

func TestReload_success_swapsEngineAndClearsDirty(t *testing.T) {
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
		t.Errorf("expected new engine to expose 'env-beta', got %v", envs)
	}
}

func TestReload_whenLoadFails_keepsOldEngine(t *testing.T) {
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

	// Then — error returned; old engine still active; dirty unchanged.
	if err == nil {
		t.Fatal("expected Reload to return an error when load fails")
	}
	if !ctrl.dirty {
		t.Error("expected dirty to remain true after a failed reload")
	}
	envs := ctrl.Environments()
	if len(envs) == 0 || envs[0].Name != "env-alpha" {
		t.Errorf("expected old engine to still expose 'env-alpha', got %v", envs)
	}
}

func TestReload_eventsReturnsNewChannel(t *testing.T) {
	// Given
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

	// Then — the channel identity must differ (new engine, new channel).
	if oldCh == newCh {
		t.Error("expected Events() to return a different channel after Reload")
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
