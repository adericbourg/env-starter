package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stubLogSource implements logSource for tests.
type stubLogSource struct {
	commands []string
	logs     map[string][]string
}

func (s *stubLogSource) WorkflowCommands(_ string) []string { return s.commands }
func (s *stubLogSource) Logs(cmd string) []string           { return s.logs[cmd] }

func TestBuildPrefixes_assignsDistinctColoredPrefixes(t *testing.T) {
	// Given
	cmds := []string{"alpha", "beta", "gamma"}

	// When
	prefixes := buildPrefixes(cmds)

	// Then
	seen := make(map[string]bool)
	for _, cmd := range cmds {
		p, ok := prefixes[cmd]
		if !ok {
			t.Fatalf("buildPrefixes: no prefix for %q", cmd)
		}
		if !strings.Contains(p, cmd) {
			t.Errorf("buildPrefixes(%q): prefix %q does not contain command name", cmd, p)
		}
		if !strings.Contains(p, "\033[") {
			t.Errorf("buildPrefixes(%q): prefix %q contains no ANSI escape", cmd, p)
		}
		if seen[p] {
			t.Errorf("buildPrefixes: duplicate prefix %q", p)
		}
		seen[p] = true
	}
}

func TestBuildPrefixes_cyclesColorsWhenMoreCommandsThanColors(t *testing.T) {
	// Given: more commands than the color palette size
	cmds := make([]string, len(cmdColorCodes)+3)
	for i := range cmds {
		cmds[i] = fmt.Sprintf("cmd%d", i)
	}

	// When
	prefixes := buildPrefixes(cmds)

	// Then: all commands get a prefix, even if colors repeat
	for _, cmd := range cmds {
		if _, ok := prefixes[cmd]; !ok {
			t.Errorf("buildPrefixes: no prefix for %q", cmd)
		}
	}
}

func TestTailStartupLogs_printsNewLines(t *testing.T) {
	// Given
	src := &stubLogSource{
		commands: []string{"svc"},
		logs:     map[string][]string{"svc": {"line1", "line2"}},
	}

	var out bytes.Buffer
	stopCh := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tailStartupLogs(context.Background(), src, "myenv", &out, stopCh)
	}()

	// When: stop immediately — the final flush on stopCh must capture all lines
	close(stopCh)
	wg.Wait()

	// Then
	output := out.String()
	for _, line := range []string{"line1", "line2"} {
		if !strings.Contains(output, line) {
			t.Errorf("tailStartupLogs: output missing %q; got:\n%s", line, output)
		}
	}
	if !strings.Contains(output, "svc") {
		t.Errorf("tailStartupLogs: output missing command name %q; got:\n%s", "svc", output)
	}
}

func TestTailStartupLogs_performsFinalFlushOnStop(t *testing.T) {
	// Given: initial logs present when the goroutine starts
	src := &stubLogSource{
		commands: []string{"worker"},
		logs:     map[string][]string{"worker": {"first"}},
	}

	var out bytes.Buffer
	stopCh := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tailStartupLogs(context.Background(), src, "e", &out, stopCh)
	}()

	// Add a line and then stop — the final flush must capture it
	src.logs["worker"] = append(src.logs["worker"], "second")
	close(stopCh)
	wg.Wait()

	// Then
	output := out.String()
	if !strings.Contains(output, "second") {
		t.Errorf("tailStartupLogs: final flush missed late line; got:\n%s", output)
	}
}

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
	_, _, _, err := resolveConfig("", "")

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
	cfg, _, _, err := resolveConfig(cfgPath, "")

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
	cfg, _, _, err := resolveConfig("", overlayPath)

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
	cfg, _, _, err := resolveConfig(basePath, overlayPath)

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
	_, _, _, err := resolveConfig("/nonexistent/path/config.yaml", "")

	// Then
	if err == nil {
		t.Fatal("expected error for missing explicit config, got nil")
	}
}
