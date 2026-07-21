package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adericbourg/env-starter/internal/linkscan"
	"github.com/adericbourg/env-starter/internal/trust"
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

// ── renderCLIOverlay ──────────────────────────────────────────────────────────

func TestRenderCLIOverlay_ofEmptyLinks_returnsEmpty(t *testing.T) {
	// Given / When
	got := renderCLIOverlay(nil, 80)

	// Then
	if got != "" {
		t.Errorf("renderCLIOverlay(nil) = %q, want empty", got)
	}
}

func TestRenderCLIOverlay_ofSingleLink_containsBorderTitleAndUrl(t *testing.T) {
	// Given
	links := []linkscan.Link{{Command: "db", URL: "https://localhost:5432"}}

	// When
	got := renderCLIOverlay(links, 80)

	// Then — rounded border corners, embedded title, command label, URL, OSC 8
	if !strings.Contains(got, "╭") || !strings.Contains(got, "╰") {
		t.Errorf("renderCLIOverlay: missing rounded border corners; got:\n%s", got)
	}
	if !strings.Contains(got, "Contextual links") {
		t.Errorf("renderCLIOverlay: missing title; got:\n%s", got)
	}
	if !strings.Contains(got, "[db]") {
		t.Errorf("renderCLIOverlay: missing command label; got:\n%s", got)
	}
	if !strings.Contains(got, "https://localhost:5432") {
		t.Errorf("renderCLIOverlay: missing URL; got:\n%s", got)
	}
	if !strings.Contains(got, "\x1b]8;;") {
		t.Errorf("renderCLIOverlay: missing OSC 8 hyperlink; got:\n%s", got)
	}
}

func TestRenderCLIOverlay_ofMultipleLinks_rendersAllRows(t *testing.T) {
	// Given
	links := []linkscan.Link{
		{Command: "db", URL: "http://db.example.com"},
		{Command: "proxy", URL: "https://proxy.example.com"},
	}

	// When
	got := renderCLIOverlay(links, 80)

	// Then — both commands and URLs appear
	for _, want := range []string{"[db]", "[proxy]", "http://db.example.com", "https://proxy.example.com"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderCLIOverlay: missing %q; got:\n%s", want, got)
		}
	}
}

// ── tailStartupLogs ───────────────────────────────────────────────────────────

func TestTailStartupLogs_whenNotInteractive_printsNewLines(t *testing.T) {
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
		tailStartupLogs(context.Background(), src, "myenv", &out, false, stopCh)
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

func TestTailStartupLogs_whenNotInteractive_performsFinalFlushOnStop(t *testing.T) {
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
		tailStartupLogs(context.Background(), src, "e", &out, false, stopCh)
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

func TestTailStartupLogs_whenNotInteractive_printsPlainLinesNoAnsiOverlay(t *testing.T) {
	// Given
	src := &stubLogSource{
		commands: []string{"svc"},
		logs:     map[string][]string{"svc": {"Login at https://example.com/sso"}},
	}

	var out bytes.Buffer
	stopCh := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tailStartupLogs(context.Background(), src, "e", &out, false, stopCh)
	}()
	close(stopCh)
	wg.Wait()

	// Then — no cursor-control sequences and no overlay header
	output := out.String()
	if strings.Contains(output, "\x1b[") && strings.Contains(output, "A\x1b[J") {
		t.Errorf("tailStartupLogs(interactive=false): unexpected cursor-control sequence in output")
	}
	if strings.Contains(output, "Contextual links") {
		t.Errorf("tailStartupLogs(interactive=false): unexpected overlay header in output")
	}
	// The URL itself should still appear as part of the plain log line
	if !strings.Contains(output, "https://example.com/sso") {
		t.Errorf("tailStartupLogs(interactive=false): expected URL in plain log line")
	}
}

func TestTailStartupLogs_whenInteractiveAndUrlPresent_drawsStickyOverlay(t *testing.T) {
	// Given
	src := &stubLogSource{
		commands: []string{"db"},
		logs:     map[string][]string{"db": {"ready at https://localhost:5432/login"}},
	}

	var out bytes.Buffer
	stopCh := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tailStartupLogs(context.Background(), src, "e", &out, true, stopCh)
	}()
	// Let one tick fire before stopping so the overlay is drawn
	time.Sleep(300 * time.Millisecond)
	close(stopCh)
	wg.Wait()

	// Then — overlay separators, the command label, the URL, and OSC 8 sequence
	output := out.String()
	if !strings.Contains(output, "Contextual links") {
		t.Errorf("tailStartupLogs(interactive=true): expected 'Contextual links' in output; got:\n%s", output)
	}
	if !strings.Contains(output, "[db]") {
		t.Errorf("tailStartupLogs(interactive=true): expected '[db]' label in overlay; got:\n%s", output)
	}
	if !strings.Contains(output, "https://localhost:5432/login") {
		t.Errorf("tailStartupLogs(interactive=true): expected URL in overlay; got:\n%s", output)
	}
	// OSC 8 hyperlink sequence start
	if !strings.Contains(output, "\x1b]8;;") {
		t.Errorf("tailStartupLogs(interactive=true): expected OSC 8 hyperlink sequence in overlay; got:\n%s", output)
	}
}

func TestTailStartupLogs_whenInteractive_clearsOverlayOnStop(t *testing.T) {
	// Given — a URL so an overlay is drawn on the first tick
	src := &stubLogSource{
		commands: []string{"svc"},
		logs:     map[string][]string{"svc": {"https://auth.example.com"}},
	}

	var out bytes.Buffer
	stopCh := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		tailStartupLogs(context.Background(), src, "e", &out, true, stopCh)
	}()
	time.Sleep(300 * time.Millisecond)
	close(stopCh)
	wg.Wait()

	// Then — the final output must end with a cursor-up+clear sequence (overlay
	// erasure) followed by the log line(s) and no trailing overlay header.
	output := out.String()
	// The overlay must have been erased at some point (cursor-up sequence present)
	if !strings.Contains(output, "\x1b[") {
		t.Errorf("tailStartupLogs(interactive=true): expected cursor-control sequences in output")
	}
	// After stopping, no overlay header should trail the output
	lastOverlayIdx := strings.LastIndex(output, "Contextual links")
	lastLogLineIdx := strings.LastIndex(output, "https://auth.example.com")
	if lastOverlayIdx > lastLogLineIdx {
		t.Errorf("tailStartupLogs(interactive=true): overlay persists after final flush (overlay at %d, last log at %d)", lastOverlayIdx, lastLogLineIdx)
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

// isolateCacheDir redirects os.UserCacheDir() (and therefore the trust
// store) to a throwaway temp dir, so tests never read or write the
// developer's real trust store. Mirrors the pattern in internal/trust's own
// tests.
func isolateCacheDir(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
}

// approveConfig marks paths as trusted so resolveConfig's gate lets them
// through. Tests that only care about load/merge behavior — not the trust
// gate itself — call this in their Given step.
func approveConfig(t *testing.T, paths ...string) {
	t.Helper()
	if err := trust.Approve(paths); err != nil {
		t.Fatalf("trust.Approve: %v", err)
	}
}

const minimalConfig = `
env-starter:
  commands: []
  environments: []
`

func TestResolveConfig_defaultMissing(t *testing.T) {
	// Given: XDG_CONFIG_HOME points to an empty dir so the default file is absent.
	isolateCacheDir(t)
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
	// Given: an approved config file.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", minimalConfig)
	approveConfig(t, cfgPath)

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
	// Given: XDG_CONFIG_HOME base + a separate, approved overlay file.
	isolateCacheDir(t)
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "env-starter"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	basePath := writeConfig(t, dir, filepath.Join("env-starter", "config.yaml"), minimalConfig)

	overlayDir := t.TempDir()
	overlayPath := writeConfig(t, overlayDir, "overlay.yaml", minimalConfig)
	approveConfig(t, basePath, overlayPath)

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
	// Given: an approved base and overlay.
	isolateCacheDir(t)
	dir := t.TempDir()
	basePath := writeConfig(t, dir, "base.yaml", minimalConfig)
	overlayPath := writeConfig(t, dir, "overlay.yaml", minimalConfig)
	approveConfig(t, basePath, overlayPath)

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

func TestResolveConfig_unapprovedConfig_returnsNotApprovedError(t *testing.T) {
	// Given: a valid config file that has never been approved.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", minimalConfig)

	// When
	_, _, _, err := resolveConfig(cfgPath, "")

	// Then — refused before it is ever parsed, with an actionable message.
	var notApproved *trust.NotApprovedError
	if !errors.As(err, &notApproved) {
		t.Fatalf("expected a *trust.NotApprovedError, got: %v", err)
	}
}

func TestResolveConfig_loadFn_whenConfigTamperedAfterApproval_returnsNotApprovedError(t *testing.T) {
	// Given: a config approved once via resolveConfig, matching how the daemon
	// gates both its initial load and every hot-reload through the same loadFn
	// (see cmd/env-starter/main.go resolveConfig — the chokepoint every config
	// passed to engine.New flows through).
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", minimalConfig)
	approveConfig(t, cfgPath)

	_, loadFn, _, err := resolveConfig(cfgPath, "")
	if err != nil {
		t.Fatalf("initial resolveConfig: unexpected error: %v", err)
	}

	// When: the file is edited after approval (a legitimate change or a
	// tampered/slipped-in one — indistinguishable without review) and the same
	// loadFn used for hot-reload is invoked again.
	if err := os.WriteFile(cfgPath, []byte(minimalConfig+"# tampered\n"), 0o600); err != nil {
		t.Fatalf("editing config: %v", err)
	}
	_, err = loadFn()

	// Then — the changed file is refused, never reaching config.Load/Merge.
	var notApproved *trust.NotApprovedError
	if !errors.As(err, &notApproved) {
		t.Fatalf("expected a *trust.NotApprovedError, got: %v", err)
	}
	if notApproved.Reason != trust.ReasonChanged {
		t.Errorf("expected Reason=ReasonChanged, got %v", notApproved.Reason)
	}
}

func TestResolveConfig_missingExplicitConfig(t *testing.T) {
	// Given: a path that does not exist.
	isolateCacheDir(t)
	// When
	_, _, _, err := resolveConfig("/nonexistent/path/config.yaml", "")

	// Then
	if err == nil {
		t.Fatal("expected error for missing explicit config, got nil")
	}
}

// ── doAllow ──────────────────────────────────────────────────────────────────

const allowTestConfig = `
env-starter:
  commands:
    - name: web
      type: service
      source:
        local: /tmp
      setup:
        - mkdir -p /tmp/x
      run: echo hello
      teardown: echo bye
  environments: []
`

func TestDoAllow_print_showsPreviewAndApprovesNothing(t *testing.T) {
	// Given: a fresh, never-approved config.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", allowTestConfig)
	var out bytes.Buffer

	// When
	result, err := doAllow(&out, strings.NewReader(""), cfgPath, "", false, true)

	// Then — the preview is shown, but nothing is approved or written.
	if err != nil {
		t.Fatalf("doAllow: unexpected error: %v", err)
	}
	if result.approved {
		t.Error("expected --print to not approve anything")
	}
	if !strings.Contains(out.String(), "run:") || !strings.Contains(out.String(), "echo hello") {
		t.Errorf("expected the preview to show the run command, got:\n%s", out.String())
	}
	if err := trust.Check([]string{cfgPath}); err == nil {
		t.Error("expected the config to remain unapproved after --print")
	}
}

func TestDoAllow_yes_approvesWithoutPrompting(t *testing.T) {
	// Given: a fresh, never-approved config.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", allowTestConfig)
	var out bytes.Buffer

	// When: --yes is set, so stdin (empty reader — would block/fail if read) is
	// never consulted.
	result, err := doAllow(&out, strings.NewReader(""), cfgPath, "", true, false)

	// Then
	if err != nil {
		t.Fatalf("doAllow: unexpected error: %v", err)
	}
	if !result.approved {
		t.Error("expected --yes to approve the config")
	}
	if err := trust.Check([]string{cfgPath}); err != nil {
		t.Errorf("expected the config to be approved, Check returned: %v", err)
	}
}

func TestDoAllow_promptYes_approves(t *testing.T) {
	// Given: a fresh config, and an operator who answers "y" at the prompt.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", allowTestConfig)
	var out bytes.Buffer

	// When
	result, err := doAllow(&out, strings.NewReader("y\n"), cfgPath, "", false, false)

	// Then
	if err != nil {
		t.Fatalf("doAllow: unexpected error: %v", err)
	}
	if !result.approved {
		t.Error("expected a 'y' answer to approve the config")
	}
	if err := trust.Check([]string{cfgPath}); err != nil {
		t.Errorf("expected the config to be approved, Check returned: %v", err)
	}
}

func TestDoAllow_promptNo_declinesAndLeavesConfigUnapproved(t *testing.T) {
	// Given: a fresh config, and an operator who answers "n" at the prompt.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", allowTestConfig)
	var out bytes.Buffer

	// When
	result, err := doAllow(&out, strings.NewReader("n\n"), cfgPath, "", false, false)

	// Then
	if err != nil {
		t.Fatalf("doAllow: unexpected error: %v", err)
	}
	if !result.declined {
		t.Error("expected a 'n' answer to be reported as declined")
	}
	if result.approved {
		t.Error("expected a 'n' answer to not approve the config")
	}
	if err := trust.Check([]string{cfgPath}); err == nil {
		t.Error("expected the config to remain unapproved after declining")
	}
}

func TestDoAllow_whenAlreadyApproved_skipsPromptAndSaysSo(t *testing.T) {
	// Given: a config already approved.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", allowTestConfig)
	approveConfig(t, cfgPath)
	var out bytes.Buffer

	// When: no prompt is needed — an empty stdin would fail/block if read.
	result, err := doAllow(&out, strings.NewReader(""), cfgPath, "", false, false)

	// Then
	if err != nil {
		t.Fatalf("doAllow: unexpected error: %v", err)
	}
	if !result.approved {
		t.Error("expected an already-approved config to report approved=true")
	}
	if !strings.Contains(out.String(), "Already approved") {
		t.Errorf("expected an 'already approved' message, got:\n%s", out.String())
	}
}

func TestDoAllow_invalidConfig_returnsError(t *testing.T) {
	// Given: a config file that fails to parse.
	isolateCacheDir(t)
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "config.yaml", "not: [valid")
	var out bytes.Buffer

	// When
	_, err := doAllow(&out, strings.NewReader(""), cfgPath, "", true, true)

	// Then
	if err == nil {
		t.Fatal("expected an error for an invalid config, got nil")
	}
}
