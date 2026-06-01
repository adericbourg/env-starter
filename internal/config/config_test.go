package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---- helpers ----------------------------------------------------------------

func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writeYAML: %v", err)
	}
	return p
}

// minimalValidYAML returns a YAML string with one command and one environment
// that can be adjusted for individual tests.
const minimalValidYAML = `
env-starter:
  commands:
    - name: database
      type: service
      source:
        github:
          repo: owner/db
          branch: main
      run: docker compose up
  environments:
    - name: dev
      workflow:
        - command: database
`

// ---- Load tests -------------------------------------------------------------

func TestLoad_ofValidConfig_parsesDurations(t *testing.T) {
	// Given
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: db
      type: service
      source:
        local: /tmp/db
      run: ./start.sh
      readiness:
        tcp: "localhost:5432"
        timeout: 60s
        interval: 500ms
  environments:
    - name: dev
      workflow:
        - command: db
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Commands[0].Readiness == nil {
		t.Fatal("expected readiness to be set")
	}
	if cfg.Commands[0].Readiness.Timeout.Duration != 60*time.Second {
		t.Errorf("expected timeout 60s, got %v", cfg.Commands[0].Readiness.Timeout.Duration)
	}
	if cfg.Commands[0].Readiness.Interval.Duration != 500*time.Millisecond {
		t.Errorf("expected interval 500ms, got %v", cfg.Commands[0].Readiness.Interval.Duration)
	}
}

func TestLoad_ofMissingFile_returnsError(t *testing.T) {
	// Given
	path := "/nonexistent/path/config.yaml"

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoad_ofInvalidYAML_returnsError(t *testing.T) {
	// Given
	dir := t.TempDir()
	path := writeYAML(t, dir, "bad.yaml", "{ not: valid: yaml: }")

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoad_ofValidConfig_parsesGitHubSource(t *testing.T) {
	// Given
	dir := t.TempDir()
	path := writeYAML(t, dir, "config.yaml", minimalValidYAML)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(cfg.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cfg.Commands))
	}
	if cfg.Commands[0].Source.GitHub == nil {
		t.Fatal("expected github source to be set")
	}
	if cfg.Commands[0].Source.GitHub.Repo != "owner/db" {
		t.Errorf("expected repo owner/db, got %q", cfg.Commands[0].Source.GitHub.Repo)
	}
}

func TestLoad_ofValidConfig_parsesURLSource(t *testing.T) {
	// Given
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: gw
      type: service
      source:
        url: https://example.com/bin
        checksum:
          alg: sha256
          value: abc123
      run: ./bin
  environments:
    - name: dev
      workflow:
        - command: gw
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	src := cfg.Commands[0].Source
	if src.URLSource == nil {
		t.Fatal("expected url source to be set")
	}
	if src.URLSource.URL != "https://example.com/bin" {
		t.Errorf("expected url https://example.com/bin, got %q", src.URLSource.URL)
	}
	if src.URLSource.Checksum == nil {
		t.Fatal("expected checksum to be set")
	}
	if src.URLSource.Checksum.Alg != "sha256" {
		t.Errorf("expected alg sha256, got %q", src.URLSource.Checksum.Alg)
	}
}

// ---- Validate tests ---------------------------------------------------------

func TestValidate_whenCommandNameMissing_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for missing command name, got nil")
	}
}

func TestValidate_whenCommandTypeMissing_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for missing command type, got nil")
	}
}

func TestValidate_whenCommandTypeInvalid_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "daemon", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for invalid command type, got nil")
	}
}

func TestValidate_whenCommandRunMissing_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Source: Source{Local: "/tmp"}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for missing run field, got nil")
	}
}

func TestValidate_whenCommandSourceMissing_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh"},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for missing source, got nil")
	}
}

func TestValidate_whenCommandSourceHasMultiple_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{
				Name: "db", Type: "service", Run: "run.sh",
				Source: Source{
					Local:  "/tmp",
					GitHub: &GitHub{Repo: "owner/repo", Branch: "main"},
				},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for multiple sources, got nil")
	}
}

func TestValidate_whenReadinessHasMultipleProbes_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{
				Name: "db", Type: "service", Run: "run.sh",
				Source: Source{Local: "/tmp"},
				Readiness: &Readiness{
					TCP:   "localhost:5432",
					Shell: "pg_isready",
				},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for multiple readiness probes, got nil")
	}
}

func TestValidate_whenReadinessUsesHTTP_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{
				Name: "svc", Type: "service", Run: "run.sh",
				Source:    Source{Local: "/tmp"},
				Readiness: &Readiness{HTTP: "http://localhost/health"},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for unsupported http probe, got nil")
	}
}

func TestValidate_whenReadinessUsesLog_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{
				Name: "svc", Type: "service", Run: "run.sh",
				Source:    Source{Local: "/tmp"},
				Readiness: &Readiness{Log: "ready"},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for unsupported log probe, got nil")
	}
}

func TestValidate_whenEnvironmentNameMissing_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for missing environment name, got nil")
	}
}

func TestValidate_whenEnvironmentWorkflowEmpty_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for empty workflow, got nil")
	}
}

func TestValidate_whenWorkflowRefsUnknownCommand_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "nonexistent"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for unknown command reference, got nil")
	}
}

func TestValidate_whenDependsOnRefsCommandNotInWorkflow_returnsError(t *testing.T) {
	// Given - "db" depends on "proxy" which exists as a command but is not in the workflow.
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
			{Name: "proxy", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{
				Name: "dev",
				Workflow: []WorkflowStep{
					{Command: "db", DependsOn: []string{"proxy"}},
				},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error when depends-on refs command not in workflow, got nil")
	}
}

func TestValidate_whenWorkflowHasCycle_returnsError(t *testing.T) {
	// Given - A -> B -> A cycle
	cfg := &Config{
		Commands: []Command{
			{Name: "a", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
			{Name: "b", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{
				Name: "dev",
				Workflow: []WorkflowStep{
					{Command: "a", DependsOn: []string{"b"}},
					{Command: "b", DependsOn: []string{"a"}},
				},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for dependency cycle, got nil")
	}
}

func TestValidate_whenWorkflowHasSelfCycle_returnsError(t *testing.T) {
	// Given - A depends on itself
	cfg := &Config{
		Commands: []Command{
			{Name: "a", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{
				Name: "dev",
				Workflow: []WorkflowStep{
					{Command: "a", DependsOn: []string{"a"}},
				},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for self-referential dependency, got nil")
	}
}

func TestValidate_whenConfigIsValid_returnsNil(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "docker compose up", Source: Source{Local: "/tmp/db"}},
			{Name: "migrate", Type: "task", Run: "./migrate.sh up", Source: Source{Local: "/tmp/migrate"}, Teardown: "./migrate.sh down"},
		},
		Environments: []Environment{
			{
				Name: "dev",
				Workflow: []WorkflowStep{
					{Command: "db"},
					{Command: "migrate", DependsOn: []string{"db"}},
				},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err != nil {
		t.Fatalf("expected no error for valid config, got: %v", err)
	}
}

// ---- Duration unmarshal tests -----------------------------------------------

func TestDuration_ofValidString_parsesCorrectly(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"60s", 60 * time.Second},
		{"1m", time.Minute},
		{"500ms", 500 * time.Millisecond},
		{"1m30s", 90 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			// Given / When
			dir := t.TempDir()
			yaml := "env-starter:\n  commands:\n    - name: db\n      type: service\n      source:\n        local: /tmp\n      run: run.sh\n      readiness:\n        tcp: \"localhost:1234\"\n        timeout: " + tc.input + "\n  environments:\n    - name: dev\n      workflow:\n        - command: db\n"
			path := writeYAML(t, dir, "config.yaml", yaml)
			cfg, err := Load(path)

			// Then
			if err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			got := cfg.Commands[0].Readiness.Timeout.Duration
			if got != tc.expected {
				t.Errorf("expected %v, got %v", tc.expected, got)
			}
		})
	}
}

func TestDuration_ofInvalidString_returnsError(t *testing.T) {
	// Given
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: db
      type: service
      source:
        local: /tmp
      run: run.sh
      readiness:
        tcp: "localhost:1234"
        timeout: "not-a-duration"
  environments:
    - name: dev
      workflow:
        - command: db
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	_, err := Load(path)

	// Then
	if err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
}

// ---- Merge tests ------------------------------------------------------------

func TestMerge_whenOverlaySharesName_overlayWins(t *testing.T) {
	// Given
	base := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "original.sh", Source: Source{Local: "/tmp/base"}},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "db", Type: "task", Run: "overlay.sh", Source: Source{Local: "/tmp/overlay"}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if len(result.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(result.Commands))
	}
	if result.Commands[0].Run != "overlay.sh" {
		t.Errorf("expected overlay run, got %q", result.Commands[0].Run)
	}
}

func TestMerge_whenOverlayHasNewName_appendsEntry(t *testing.T) {
	// Given
	base := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "db.sh", Source: Source{Local: "/tmp/db"}},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "proxy", Type: "service", Run: "proxy.sh", Source: Source{Local: "/tmp/proxy"}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if len(result.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(result.Commands))
	}
	if result.Commands[0].Name != "db" {
		t.Errorf("expected first command to be db, got %q", result.Commands[0].Name)
	}
	if result.Commands[1].Name != "proxy" {
		t.Errorf("expected second command to be proxy, got %q", result.Commands[1].Name)
	}
}

func TestMerge_preservesBaseOrder(t *testing.T) {
	// Given
	base := &Config{
		Commands: []Command{
			{Name: "a", Type: "service", Run: "a.sh", Source: Source{Local: "/tmp/a"}},
			{Name: "b", Type: "service", Run: "b.sh", Source: Source{Local: "/tmp/b"}},
			{Name: "c", Type: "service", Run: "c.sh", Source: Source{Local: "/tmp/c"}},
		},
	}
	// Overlay replaces "b" and adds "d"
	overlay := &Config{
		Commands: []Command{
			{Name: "b", Type: "task", Run: "b-new.sh", Source: Source{Local: "/tmp/b2"}},
			{Name: "d", Type: "service", Run: "d.sh", Source: Source{Local: "/tmp/d"}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	names := make([]string, len(result.Commands))
	for i, cmd := range result.Commands {
		names[i] = cmd.Name
	}
	expected := []string{"a", "b", "c", "d"}
	for i, n := range expected {
		if names[i] != n {
			t.Errorf("position %d: expected %q, got %q", i, n, names[i])
		}
	}
}

func TestMerge_doesNotMutateInputs(t *testing.T) {
	// Given
	base := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "db.sh", Source: Source{Local: "/tmp/db"}},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "db", Type: "task", Run: "db-overlay.sh", Source: Source{Local: "/tmp/db2"}},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// Snapshot original state
	origBaseRun := base.Commands[0].Run
	origBaseEnv := base.Environments[0].Name

	// When
	_ = Merge(base, overlay)

	// Then
	if base.Commands[0].Run != origBaseRun {
		t.Errorf("base was mutated: run changed from %q to %q", origBaseRun, base.Commands[0].Run)
	}
	if base.Environments[0].Name != origBaseEnv {
		t.Errorf("base environments were mutated")
	}
}

func TestMerge_whenBaseOnly_keepsAll(t *testing.T) {
	// Given
	base := &Config{
		Commands: []Command{
			{Name: "a", Type: "service", Run: "a.sh", Source: Source{Local: "/tmp/a"}},
			{Name: "b", Type: "service", Run: "b.sh", Source: Source{Local: "/tmp/b"}},
		},
	}
	overlay := &Config{}

	// When
	result := Merge(base, overlay)

	// Then
	if len(result.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(result.Commands))
	}
}

// ---- Setup field tests ------------------------------------------------------

func TestLoad_ofConfigWithSetup_parsesSetupList(t *testing.T) {
	// Given
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: web
      type: service
      source:
        local: /tmp/web
      setup:
        - yarn install
        - yarn build
      run: yarn start
  environments:
    - name: dev
      workflow:
        - command: web
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	got := cfg.Commands[0].Setup
	want := []string{"yarn install", "yarn build"}
	if len(got) != len(want) {
		t.Fatalf("expected %d setup steps, got %d: %v", len(want), len(got), got)
	}
	for i, step := range want {
		if got[i] != step {
			t.Errorf("setup[%d]: expected %q, got %q", i, step, got[i])
		}
	}
}

func TestValidate_whenSetupStepEmpty_returnsError(t *testing.T) {
	// Given
	cfg := &Config{
		Commands: []Command{
			{
				Name:   "web",
				Type:   "service",
				Source: Source{Local: "/tmp/web"},
				Setup:  []string{"yarn install", ""},
				Run:    "yarn start",
			},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "web"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for empty setup step, got nil")
	}
}

// ---- Restart config tests ---------------------------------------------------

func TestValidate_whenRestartOnTaskWithoutReadiness_returnsError(t *testing.T) {
	// Given — task has restart but no readiness probe
	enabled := true
	cfg := &Config{
		Commands: []Command{
			{
				Name:    "tunnel",
				Type:    "task",
				Source:  Source{Local: "/tmp"},
				Run:     "open-tunnel.sh",
				Restart: &Restart{Enabled: &enabled},
			},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "tunnel"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for restart on task without readiness probe, got nil")
	}
}

func TestValidate_whenRestartOnTaskWithReadiness_isValid(t *testing.T) {
	// Given — task has both restart and a readiness probe
	enabled := true
	cfg := &Config{
		Commands: []Command{
			{
				Name:      "tunnel",
				Type:      "task",
				Source:    Source{Local: "/tmp"},
				Run:       "open-tunnel.sh",
				Readiness: &Readiness{Shell: "check-tunnel.sh"},
				Restart:   &Restart{Enabled: &enabled},
			},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "tunnel"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err != nil {
		t.Fatalf("expected no error for restart on task with readiness probe, got: %v", err)
	}
}

func TestValidate_whenRestartMaxRetriesNegative_returnsError(t *testing.T) {
	// Given
	neg := -1
	cfg := &Config{
		Commands: []Command{
			{
				Name:    "svc",
				Type:    "service",
				Source:  Source{Local: "/tmp"},
				Run:     "run.sh",
				Restart: &Restart{MaxRetries: &neg},
			},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "svc"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for negative max-retries, got nil")
	}
}

func TestValidate_whenRestartOnService_isValid(t *testing.T) {
	// Given
	enabled := false
	max := 5
	cfg := &Config{
		Commands: []Command{
			{
				Name:    "svc",
				Type:    "service",
				Source:  Source{Local: "/tmp"},
				Run:     "run.sh",
				Restart: &Restart{Enabled: &enabled, MaxRetries: &max},
			},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "svc"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err != nil {
		t.Fatalf("expected no error for valid restart config on service, got: %v", err)
	}
}

func TestLoad_ofConfigWithRestart_parsesRestartBlock(t *testing.T) {
	// Given
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: tunnel
      type: service
      source:
        local: /tmp
      run: run.sh
      readiness:
        shell: check.sh
      restart:
        enabled: false
        max-retries: 5
        backoff-base: 2s
        check-interval: 30s
  environments:
    - name: dev
      workflow:
        - command: tunnel
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	r := cfg.Commands[0].Restart
	if r == nil {
		t.Fatal("expected restart block to be set")
	}
	if r.Enabled == nil || *r.Enabled != false {
		t.Errorf("expected enabled=false, got %v", r.Enabled)
	}
	if r.MaxRetries == nil || *r.MaxRetries != 5 {
		t.Errorf("expected max-retries=5, got %v", r.MaxRetries)
	}
	if r.BackoffBase == nil || r.BackoffBase.Duration != 2*time.Second {
		t.Errorf("expected backoff-base=2s, got %v", r.BackoffBase)
	}
	if r.CheckInterval == nil || r.CheckInterval.Duration != 30*time.Second {
		t.Errorf("expected check-interval=30s, got %v", r.CheckInterval)
	}
}

func TestLoad_ofTaskWithReadinessAndRestart_parsesCorrectly(t *testing.T) {
	// Given
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: tunnel
      type: task
      source:
        local: /tmp
      run: open-tunnel.sh
      readiness:
        shell: check-tunnel.sh
        timeout: 30s
      restart:
        max-retries: 5
        check-interval: 10s
  environments:
    - name: dev
      workflow:
        - command: tunnel
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error for task with readiness and restart, got: %v", err)
	}
	cmd := cfg.Commands[0]
	if cmd.Readiness == nil {
		t.Fatal("expected readiness to be set")
	}
	if cmd.Restart == nil {
		t.Fatal("expected restart to be set")
	}
	if cmd.Restart.MaxRetries == nil || *cmd.Restart.MaxRetries != 5 {
		t.Errorf("expected max-retries=5, got %v", cmd.Restart.MaxRetries)
	}
	if cmd.Restart.CheckInterval == nil || cmd.Restart.CheckInterval.Duration != 10*time.Second {
		t.Errorf("expected check-interval=10s, got %v", cmd.Restart.CheckInterval)
	}
}

func TestMerge_environmentsMergedByName(t *testing.T) {
	// Given
	base := &Config{
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "db"}}},
			{Name: "prod", Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Environments: []Environment{
			{Name: "dev", Description: "overlay dev", Workflow: []WorkflowStep{{Command: "proxy"}}},
			{Name: "staging", Workflow: []WorkflowStep{{Command: "proxy"}}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if len(result.Environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(result.Environments))
	}
	// base order: dev (replaced), prod; then new: staging
	if result.Environments[0].Name != "dev" {
		t.Errorf("expected first env to be dev, got %q", result.Environments[0].Name)
	}
	if result.Environments[0].Description != "overlay dev" {
		t.Errorf("expected overlay description, got %q", result.Environments[0].Description)
	}
	if result.Environments[1].Name != "prod" {
		t.Errorf("expected second env to be prod, got %q", result.Environments[1].Name)
	}
	if result.Environments[2].Name != "staging" {
		t.Errorf("expected third env to be staging, got %q", result.Environments[2].Name)
	}
}
