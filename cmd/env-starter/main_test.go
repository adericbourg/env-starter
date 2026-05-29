package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigPath_withXDGConfigHome(t *testing.T) {
	// Given
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

	// When
	got := defaultConfigPath()

	// Then
	want := "/custom/xdg/env-starter/config.yaml"
	if got != want {
		t.Errorf("defaultConfigPath() = %q; want %q", got, want)
	}
}

func TestDefaultConfigPath_withoutXDGConfigHome(t *testing.T) {
	// Given
	t.Setenv("XDG_CONFIG_HOME", "")

	// When
	got := defaultConfigPath()

	// Then
	if !strings.HasSuffix(got, filepath.Join(".config", "env-starter", "config.yaml")) {
		t.Errorf("defaultConfigPath() = %q; want suffix %q", got, filepath.Join(".config", "env-starter", "config.yaml"))
	}
}

func writeConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	return path
}

const minimalConfig = `
env-starter:
  commands: []
  environments: []
`

func TestResolveConfig_defaultMissing(t *testing.T) {
	// Given: XDG_CONFIG_HOME points to an empty dir so the default file is absent.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	// When
	_, err := resolveConfig("", "")

	// Then: expect a helpful error message.
	if err == nil {
		t.Fatal("expected an error for missing default config, got nil")
	}
	if !strings.Contains(err.Error(), "create ~/.config/env-starter/config.yaml") {
		t.Errorf("error message should mention the hint, got: %v", err)
	}
}

func TestResolveConfig_explicitConfigOnly(t *testing.T) {
	// Given
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", minimalConfig)

	// When
	cfg, err := resolveConfig(cfgPath, "")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestResolveConfig_overlayOnly(t *testing.T) {
	// Given: XDG_CONFIG_HOME base + a separate overlay file.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "env-starter"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeConfig(t, dir, filepath.Join("env-starter", "config.yaml"), minimalConfig)

	overlayDir := t.TempDir()
	overlayPath := writeConfig(t, overlayDir, "overlay.yaml", minimalConfig)

	// When
	cfg, err := resolveConfig("", overlayPath)

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestResolveConfig_bothConfigAndOverlay(t *testing.T) {
	// Given
	dir := t.TempDir()
	basePath := writeConfig(t, dir, "base.yaml", minimalConfig)
	overlayPath := writeConfig(t, dir, "overlay.yaml", minimalConfig)

	// When
	cfg, err := resolveConfig(basePath, overlayPath)

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestResolveConfig_missingExplicitConfig(t *testing.T) {
	// Given: a path that does not exist.
	// When
	_, err := resolveConfig("/nonexistent/path/config.yaml", "")

	// Then
	if err == nil {
		t.Fatal("expected error for missing explicit config, got nil")
	}
}
