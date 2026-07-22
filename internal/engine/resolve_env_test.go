package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
)

func findResolved(t *testing.T, vars []ResolvedEnvVar, key string) ResolvedEnvVar {
	t.Helper()
	for _, v := range vars {
		if v.Key == key {
			return v
		}
	}
	t.Fatalf("key %q not found in %+v", key, vars)
	return ResolvedEnvVar{}
}

func indexOfKey(vars []ResolvedEnvVar, key string) int {
	for i, v := range vars {
		if v.Key == key {
			return i
		}
	}
	return -1
}

func TestResolveEnv_ofUnknownEnvironment_returnsNil(t *testing.T) {
	e := newTestEngine(t, &config.Config{})
	if got := e.ResolveEnv("nope", ""); got != nil {
		t.Errorf("expected nil for unknown environment, got %v", got)
	}
}

func TestResolveEnv_ofUnknownCommand_returnsNil(t *testing.T) {
	e := newTestEngine(t, &config.Config{})
	if got := e.ResolveEnv("", "nope"); got != nil {
		t.Errorf("expected nil for unknown command, got %v", got)
	}
}

func TestResolveEnv_ofEnvironment_resolvesOSOverriddenByEnvironment(t *testing.T) {
	// Given an OS var and an environment that overrides one shared key and
	// declares one key of its own.
	t.Setenv("ENV_STARTER_TEST_OS_ONLY", "os-value")
	t.Setenv("ENV_STARTER_TEST_SHARED", "os-shared")

	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "db", Type: "service", Run: "sleep 30", Source: localSource(t.TempDir())},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Env: map[string]string{
					"ENV_STARTER_TEST_SHARED":   "env-value",
					"ENV_STARTER_TEST_ENV_ONLY": "env-only-value",
				},
				Workflow: []config.WorkflowStep{{Command: "db"}},
			},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	got := e.ResolveEnv("dev", "")

	// Then: the OS-only var is present, unshadowed, with OS provenance.
	osVar := findResolved(t, got, "ENV_STARTER_TEST_OS_ONLY")
	if osVar.Winning.Source != EnvSourceOS || osVar.Winning.Value != "os-value" {
		t.Errorf("expected OS var unchanged, got %+v", osVar)
	}
	if len(osVar.Shadowed) != 0 {
		t.Errorf("expected no shadowed layers for an OS-only var, got %+v", osVar.Shadowed)
	}

	// And the shared key resolves to the environment's value, shadowing the OS one.
	shared := findResolved(t, got, "ENV_STARTER_TEST_SHARED")
	if shared.Winning.Source != EnvSourceEnvironment || shared.Winning.Value != "env-value" {
		t.Errorf("expected environment override, got %+v", shared)
	}
	if len(shared.Shadowed) != 1 || shared.Shadowed[0].Source != EnvSourceOS || shared.Shadowed[0].Value != "os-shared" {
		t.Errorf("expected shadowed OS value, got %+v", shared.Shadowed)
	}

	// And the environment-only var is present too.
	envOnly := findResolved(t, got, "ENV_STARTER_TEST_ENV_ONLY")
	if envOnly.Winning.Source != EnvSourceEnvironment || envOnly.Winning.Value != "env-only-value" {
		t.Errorf("expected environment var, got %+v", envOnly)
	}
}

func TestResolveEnv_ofCommand_resolvesOSEnvironmentAndCommandLayers(t *testing.T) {
	// Given an OS var, an environment holding "api" that overrides it and adds
	// one of its own, and the command itself overriding the same shared key.
	t.Setenv("ENV_STARTER_TEST_SHARED", "os-shared")

	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")

	cfg := &config.Config{
		Commands: []config.Command{
			{
				Name:      "api",
				Type:      "service",
				Source:    localSource(dir),
				Run:       fmt.Sprintf("touch %q; sleep 30", ready),
				Readiness: &config.Readiness{Shell: fmt.Sprintf("test -f %q", ready)},
				Env:       map[string]string{"ENV_STARTER_TEST_SHARED": "command-value"},
			},
		},
		Environments: []config.Environment{
			{
				Name: "dev",
				Env: map[string]string{
					"ENV_STARTER_TEST_SHARED":   "env-value",
					"ENV_STARTER_TEST_ENV_ONLY": "env-only-value",
				},
				Workflow: []config.WorkflowStep{{Command: "api"}},
			},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	if err := e.StartEnvironment("dev"); err != nil {
		t.Fatalf("StartEnvironment: %v", err)
	}
	waitForCmd(t, e, "api", CmdHealthy, 5*time.Second)

	// When
	got := e.ResolveEnv("", "api")

	// Then: the command's own value wins, shadowing environment then OS, in
	// nearest-shadowed-first order.
	shared := findResolved(t, got, "ENV_STARTER_TEST_SHARED")
	if shared.Winning.Source != EnvSourceCommand || shared.Winning.Value != "command-value" {
		t.Fatalf("expected command override, got %+v", shared)
	}
	if len(shared.Shadowed) != 2 {
		t.Fatalf("expected 2 shadowed layers, got %d: %+v", len(shared.Shadowed), shared.Shadowed)
	}
	if shared.Shadowed[0].Source != EnvSourceEnvironment || shared.Shadowed[0].Value != "env-value" {
		t.Errorf("expected nearest-shadowed to be environment, got %+v", shared.Shadowed[0])
	}
	if shared.Shadowed[1].Source != EnvSourceOS || shared.Shadowed[1].Value != "os-shared" {
		t.Errorf("expected next-shadowed to be OS, got %+v", shared.Shadowed[1])
	}

	// And the environment-only var is visible via the holder layer.
	envOnly := findResolved(t, got, "ENV_STARTER_TEST_ENV_ONLY")
	if envOnly.Winning.Source != EnvSourceEnvironment || envOnly.Winning.Value != "env-only-value" {
		t.Errorf("expected environment var via holder, got %+v", envOnly)
	}
}

func TestResolveEnv_ofNeverStartedCommand_hasNoEnvironmentLayer(t *testing.T) {
	// Given a command that overrides a key also set by its (not yet started)
	// environment.
	cfg := &config.Config{
		Commands: []config.Command{
			{Name: "api", Type: "service", Run: "sleep 30", Source: localSource(t.TempDir()),
				Env: map[string]string{"ENV_STARTER_TEST_KEY": "cmd-value"}},
		},
		Environments: []config.Environment{
			{Name: "dev", Env: map[string]string{"ENV_STARTER_TEST_KEY": "env-value"},
				Workflow: []config.WorkflowStep{{Command: "api"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When: resolved before the command has ever been started (no holders yet).
	got := e.ResolveEnv("", "api")

	// Then: the command's own value still wins (it always overrides), but there
	// is no environment-level shadow since no holder has joined yet.
	key := findResolved(t, got, "ENV_STARTER_TEST_KEY")
	if key.Winning.Source != EnvSourceCommand || key.Winning.Value != "cmd-value" {
		t.Fatalf("expected command value, got %+v", key)
	}
	if len(key.Shadowed) != 0 {
		t.Errorf("expected no shadowed layers before any holder starts the command, got %+v", key.Shadowed)
	}
}

func TestResolveEnv_sortsConfigDefinedKeysBeforeOSKeys(t *testing.T) {
	// Given an OS-only key that would sort alphabetically before a
	// config-defined key — the grouping rule must still put the config-defined
	// key first.
	t.Setenv("AAA_ENV_STARTER_TEST_OS_ONLY", "x")

	cfg := &config.Config{
		Commands: []config.Command{{Name: "db", Type: "service", Run: "sleep 30", Source: localSource(t.TempDir())}},
		Environments: []config.Environment{
			{Name: "dev", Env: map[string]string{"ZZZ_ENV_STARTER_TEST_ENV_ONLY": "y"},
				Workflow: []config.WorkflowStep{{Command: "db"}}},
		},
	}
	e := newTestEngine(t, cfg)
	defer e.Shutdown(context.Background())

	// When
	got := e.ResolveEnv("dev", "")

	// Then
	idxEnv := indexOfKey(got, "ZZZ_ENV_STARTER_TEST_ENV_ONLY")
	idxOS := indexOfKey(got, "AAA_ENV_STARTER_TEST_OS_ONLY")
	if idxEnv < 0 || idxOS < 0 {
		t.Fatalf("expected both keys present, got %+v", got)
	}
	if idxEnv > idxOS {
		t.Errorf("expected config-defined key before OS-only key despite reverse alphabetical order, got order %+v", got)
	}
}
