package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoad_ofInteractiveAuthTrue_parsesFlag(t *testing.T) {
	// Given a command with interactive-auth: true
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: login
      type: task
      source:
        local: /tmp/login
      run: tsh login
      interactive-auth: true
  environments:
    - name: dev
      workflow:
        - command: login
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Commands[0].InteractiveAuth == nil || !*cfg.Commands[0].InteractiveAuth {
		t.Error("expected InteractiveAuth to be true")
	}
}

func TestLoad_ofInteractiveAuthAbsent_defaultsFalse(t *testing.T) {
	// Given a command without interactive-auth
	dir := t.TempDir()
	path := writeYAML(t, dir, "config.yaml", minimalValidYAML)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Commands[0].InteractiveAuth != nil {
		t.Error("expected InteractiveAuth to be unset")
	}
}

func TestLoad_ofAutoStartTrue_parsesFlag(t *testing.T) {
	// Given an environment with auto-start: true
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: database
      type: service
      source:
        local: /tmp/db
      run: docker compose up
  environments:
    - name: dev
      auto-start: true
      workflow:
        - command: database
`
	path := writeYAML(t, dir, "config.yaml", yaml)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Environments[0].AutoStart == nil || !*cfg.Environments[0].AutoStart {
		t.Error("expected AutoStart to be true")
	}
}

func TestLoad_ofAutoStartAbsent_defaultsFalse(t *testing.T) {
	// Given an environment without auto-start
	dir := t.TempDir()
	path := writeYAML(t, dir, "config.yaml", minimalValidYAML)

	// When
	cfg, err := Load(path)

	// Then
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Environments[0].AutoStart != nil {
		t.Error("expected AutoStart to be unset")
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
          value: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
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

func TestValidate_whenCommandNameUnsafe_returnsError(t *testing.T) {
	// Names become log file names ("<name>.log"), so anything that could
	// escape the logs directory or start a flag must be rejected.
	for _, name := range []string{"../../evil", "a/b", ".hidden", "-flag", "a\\b", "x\ny"} {
		cfg := &Config{
			Commands: []Command{
				{Name: name, Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("name %q: expected error, got nil", name)
		}
	}
}

func TestValidate_whenCommandNameSafe_returnsNil(t *testing.T) {
	for _, name := range []string{"db", "api-server", "web_1", "cache.v2", "my service"} {
		cfg := &Config{
			Commands: []Command{
				{Name: name, Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
			},
			Environments: []Environment{
				{Name: "dev", Workflow: []WorkflowStep{{Command: name}}},
			},
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("name %q: expected no error, got %v", name, err)
		}
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

func TestValidate_whenURLSourceNotHTTPS_returnsError(t *testing.T) {
	// Given a url source served over plaintext http.
	cfg := &Config{
		Commands: []Command{
			{
				Name: "bin", Type: "service", Run: "./bin",
				Source: Source{URLSource: &URL{URL: "http://example.com/bin"}},
			},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for non-https url source, got nil")
	}
}

func TestValidate_whenURLSourceHTTPS_returnsNil(t *testing.T) {
	// Given a url source served over https.
	cfg := &Config{
		Commands: []Command{
			{
				Name: "bin", Type: "service", Run: "./bin",
				Source: Source{URLSource: &URL{URL: "https://example.com/bin"}},
			},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "bin"}}},
		},
	}

	// When / Then
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error for https url source: %v", err)
	}
}

func TestWarnings_whenURLSourceHasNoChecksum_returnsAdvisory(t *testing.T) {
	// Given an https url source with no checksum.
	cfg := &Config{
		Commands: []Command{
			{
				Name: "bin", Type: "service", Run: "./bin",
				Source: Source{URLSource: &URL{URL: "https://example.com/bin"}},
			},
		},
	}

	// When
	warnings := cfg.Warnings()

	// Then
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %d: %v", len(warnings), warnings)
	}
}

func TestWarnings_whenURLSourceHasChecksum_returnsNone(t *testing.T) {
	// Given an https url source with a checksum.
	cfg := &Config{
		Commands: []Command{
			{
				Name: "bin", Type: "service", Run: "./bin",
				Source: Source{URLSource: &URL{
					URL:      "https://example.com/bin",
					Checksum: &Checksum{Alg: "sha256", Value: "abc"},
				}},
			},
		},
	}

	// When / Then
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("expected no warnings, got %v", w)
	}
}

func TestValidate_whenRequireChecksumsAndURLSourceHasNone_returnsError(t *testing.T) {
	// Given require-checksums and an https url source without a checksum.
	cfg := &Config{
		RequireChecksums: true,
		Commands: []Command{
			{
				Name: "bin", Type: "service", Run: "./bin",
				Source: Source{URLSource: &URL{URL: "https://example.com/bin"}},
			},
		},
	}

	// When / Then
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing checksum under require-checksums, got nil")
	}
}

func TestValidate_whenChecksumMalformed_returnsError(t *testing.T) {
	cases := map[string]Checksum{
		"unsupported alg": {Alg: "md5", Value: strings.Repeat("a", 32)},
		"short value":     {Alg: "sha256", Value: "abc123"},
		"non-hex value":   {Alg: "sha256", Value: strings.Repeat("z", 64)},
		"empty value":     {Alg: "sha256"},
	}
	for name, cs := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{Commands: []Command{{
				Name: "bin", Type: "service", Run: "./bin",
				Source: Source{URLSource: &URL{URL: "https://example.com/bin", Checksum: &cs}},
			}}}
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected error for checksum %+v, got nil", cs)
			}
		})
	}
}

func TestValidate_whenChecksumWellFormed_returnsNil(t *testing.T) {
	cfg := &Config{Commands: []Command{{
		Name: "bin", Type: "service", Run: "./bin",
		Source: Source{URLSource: &URL{
			URL:      "https://example.com/bin",
			Checksum: &Checksum{Alg: "sha256", Value: strings.Repeat("ab", 32)},
		}},
	}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestMerge_requireChecksums_isNeverRelaxedByOverlay(t *testing.T) {
	strict := &Config{RequireChecksums: true}
	lax := &Config{}

	if !Merge(strict, lax).RequireChecksums {
		t.Error("overlay without require-checksums must not relax the base")
	}
	if !Merge(lax, strict).RequireChecksums {
		t.Error("overlay with require-checksums must tighten the base")
	}
	if Merge(lax, lax).RequireChecksums {
		t.Error("neither file sets require-checksums: merged config must not")
	}
}

func TestValidate_whenGitHubRepoMalformed_returnsError(t *testing.T) {
	cases := map[string]string{
		"no slash":          "ownerrepo",
		"leading dash":      "-owner/repo",
		"shell metachar":    "owner/repo;rm -rf",
		"path traversal":    "../../etc/repo",
		"too many segments": "owner/repo/extra",
	}
	for name, repo := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			cfg := &Config{Commands: []Command{{
				Name: "c", Type: "service", Run: "x",
				Source: Source{GitHub: &GitHub{Repo: repo, Branch: "main"}},
			}}}
			// When / Then
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected error for repo %q, got nil", repo)
			}
		})
	}
}

func TestValidate_whenGitHubBranchUnsafe_returnsError(t *testing.T) {
	cases := map[string]string{
		"leading dash":   "-x",
		"shell metachar": "main;echo",
		"space":          "feat branch",
	}
	for name, branch := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			cfg := &Config{Commands: []Command{{
				Name: "c", Type: "service", Run: "x",
				Source: Source{GitHub: &GitHub{Repo: "owner/repo", Branch: branch}},
			}}}
			// When / Then
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected error for branch %q, got nil", branch)
			}
		})
	}
}

func TestValidate_whenSubdirEscapes_returnsError(t *testing.T) {
	cases := map[string]string{
		"parent traversal": "../secrets",
		"nested traversal": "scripts/../../etc",
		"absolute path":    "/etc",
	}
	for name, subdir := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			cfg := &Config{Commands: []Command{{
				Name: "c", Type: "service", Run: "x",
				Source: Source{GitHub: &GitHub{Repo: "owner/repo", Branch: "main"}, Subdir: subdir},
			}}}
			// When / Then
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected error for subdir %q, got nil", subdir)
			}
		})
	}
}

func TestValidate_whenGitHubSourceWellFormed_returnsNil(t *testing.T) {
	// Given a valid repo, branch and nested subdir.
	cfg := &Config{
		Commands: []Command{{
			Name: "c", Type: "service", Run: "x",
			Source: Source{
				GitHub: &GitHub{Repo: "acme.org/infra_repo-1", Branch: "release/1.x"},
				Subdir: "scripts/db",
			},
		}},
		Environments: []Environment{{Name: "dev", Workflow: []WorkflowStep{{Command: "c"}}}},
	}
	// When / Then
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func TestMerge_autoStart_overlayCanEnable(t *testing.T) {
	// Given a base environment with auto-start unset and an overlay enabling it
	enabled := true
	base := &Config{
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Environments: []Environment{
			{Name: "dev", AutoStart: &enabled},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if result.Environments[0].AutoStart == nil || !*result.Environments[0].AutoStart {
		t.Error("expected overlay to enable AutoStart")
	}
}

func TestMerge_autoStart_overlayCanDisable(t *testing.T) {
	// Given a base environment with auto-start enabled and an overlay explicitly disabling it
	enabled := true
	disabled := false
	base := &Config{
		Environments: []Environment{
			{Name: "dev", AutoStart: &enabled, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Environments: []Environment{
			{Name: "dev", AutoStart: &disabled},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if result.Environments[0].AutoStart == nil || *result.Environments[0].AutoStart {
		t.Error("expected overlay to disable AutoStart")
	}
}

func TestMerge_autoStart_overlayOmit_inheritsBase(t *testing.T) {
	// Given a base environment with auto-start enabled and an overlay that omits it
	enabled := true
	base := &Config{
		Environments: []Environment{
			{Name: "dev", AutoStart: &enabled, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Environments: []Environment{
			{Name: "dev", Description: "overlay dev"},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if result.Environments[0].AutoStart == nil || !*result.Environments[0].AutoStart {
		t.Error("expected base's AutoStart to be preserved when overlay omits it")
	}
}

func TestMerge_interactiveAuth_overlayCanEnable(t *testing.T) {
	// Given a base command with interactive-auth unset and an overlay enabling it
	enabled := true
	base := &Config{
		Commands: []Command{
			{Name: "login", Type: "task", Run: "tsh login", Source: Source{Local: "/tmp/login"}},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "login", InteractiveAuth: &enabled},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if result.Commands[0].InteractiveAuth == nil || !*result.Commands[0].InteractiveAuth {
		t.Error("expected overlay to enable InteractiveAuth")
	}
}

func TestMerge_interactiveAuth_overlayCanDisable(t *testing.T) {
	// Given a base command with interactive-auth enabled and an overlay explicitly disabling it
	enabled := true
	disabled := false
	base := &Config{
		Commands: []Command{
			{Name: "login", Type: "task", Run: "tsh login", Source: Source{Local: "/tmp/login"}, InteractiveAuth: &enabled},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "login", InteractiveAuth: &disabled},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if result.Commands[0].InteractiveAuth == nil || *result.Commands[0].InteractiveAuth {
		t.Error("expected overlay to disable InteractiveAuth")
	}
}

func TestMerge_interactiveAuth_overlayOmit_inheritsBase(t *testing.T) {
	// Given a base command with interactive-auth enabled and an overlay that omits it
	enabled := true
	base := &Config{
		Commands: []Command{
			{Name: "login", Type: "task", Run: "tsh login", Source: Source{Local: "/tmp/login"}, InteractiveAuth: &enabled},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "login", Env: map[string]string{"FOO": "bar"}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	if result.Commands[0].InteractiveAuth == nil || !*result.Commands[0].InteractiveAuth {
		t.Error("expected base's InteractiveAuth to be preserved when overlay omits it")
	}
}

// ---- Environment env tests ---------------------------------------------------

func TestLoad_ofEnvironmentEnv_parsesMap(t *testing.T) {
	// Given a config with an environment-level env map
	dir := t.TempDir()
	yaml := `
env-starter:
  commands:
    - name: db
      type: service
      source:
        local: /tmp/db
      run: ./start.sh
  environments:
    - name: dev
      env:
        FOO_BAR_KEY: foo
        SUPER_SECRET_KEY: aaa
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
	want := map[string]string{"FOO_BAR_KEY": "foo", "SUPER_SECRET_KEY": "aaa"}
	if got := cfg.Environments[0].Env; len(got) != len(want) || got["FOO_BAR_KEY"] != "foo" || got["SUPER_SECRET_KEY"] != "aaa" {
		t.Errorf("expected env %v, got %v", want, got)
	}
}

func TestValidate_whenCommandEnvKeyEmpty_returnsError(t *testing.T) {
	// Given a command with an empty env key
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}, Env: map[string]string{"": "x"}},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for empty env key, got nil")
	}
}

func TestValidate_whenCommandEnvKeyHasEquals_returnsError(t *testing.T) {
	// Given a command with an env key containing '='
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}, Env: map[string]string{"FOO=BAR": "x"}},
		},
		Environments: []Environment{
			{Name: "dev", Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for env key containing '=', got nil")
	}
}

func TestValidate_whenEnvironmentEnvKeyEmpty_returnsError(t *testing.T) {
	// Given an environment with an empty env key
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"": "x"}, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected error for empty env key, got nil")
	}
}

func TestValidate_whenSharedCommandEnvConflicts_returnsError(t *testing.T) {
	// Given two environments sharing command "db" that set the same key to
	// different values
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"PORT": "5432"}, Workflow: []WorkflowStep{{Command: "db"}}},
			{Name: "dev-debug", Env: map[string]string{"PORT": "5433"}, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "db") || !strings.Contains(err.Error(), "PORT") {
		t.Errorf("expected error to mention command %q and key %q, got: %v", "db", "PORT", err)
	}
}

func TestValidate_whenSharedCommandEnvAgrees_returnsNil(t *testing.T) {
	// Given two environments sharing command "db" that set the same key to the
	// same value — not a conflict
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"PORT": "5432"}, Workflow: []WorkflowStep{{Command: "db"}}},
			{Name: "dev-mirror", Env: map[string]string{"PORT": "5432"}, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_whenCommandEnvOverridesConflictingKey_returnsNil(t *testing.T) {
	// Given two environments that would conflict on "PORT", but the shared
	// command itself sets "PORT" — the command always wins, so there is no
	// observable conflict.
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}, Env: map[string]string{"PORT": "9999"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"PORT": "5432"}, Workflow: []WorkflowStep{{Command: "db"}}},
			{Name: "dev-debug", Env: map[string]string{"PORT": "5433"}, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err != nil {
		t.Errorf("expected no error (command env overrides), got: %v", err)
	}
}

func TestValidate_whenEnvsConflictOnDifferentCommands_returnsNil(t *testing.T) {
	// Given two environments that each set the same key, but for different
	// (unshared) commands — no conflict, since the key never has to be
	// resolved for a single shared process.
	cfg := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
			{Name: "api", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"PORT": "5432"}, Workflow: []WorkflowStep{{Command: "db"}}},
			{Name: "prod", Env: map[string]string{"PORT": "5433"}, Workflow: []WorkflowStep{{Command: "api"}}},
		},
	}

	// When
	err := cfg.Validate()

	// Then
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// ---- Overlay env merge tests --------------------------------------------------

func TestMerge_commandEnv_mergesPerKeyOverlayWins(t *testing.T) {
	// Given a base command with two env keys and an overlay that shares the
	// command name, sets one shared key to a new value, and adds a new key.
	base := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Source: Source{Local: "/tmp/db"},
				Env: map[string]string{"PGPORT": "5432", "LOG_LEVEL": "info"}},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "db", Env: map[string]string{"LOG_LEVEL": "debug", "SUPER_SECRET_KEY": "aaa"}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then: base-only key kept, shared key overridden, overlay-only key added.
	got := result.Commands[0].Env
	want := map[string]string{"PGPORT": "5432", "LOG_LEVEL": "debug", "SUPER_SECRET_KEY": "aaa"}
	if len(got) != len(want) {
		t.Fatalf("expected env %v, got %v", want, got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q: expected %q, got %q", k, v, got[k])
		}
	}
}

func TestMerge_environmentEnv_mergesPerKeyOverlayWins(t *testing.T) {
	// Given a base environment with an env key and an overlay that shares the
	// environment name and adds a secret.
	base := &Config{
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"FOO_BAR_KEY": "foo"}, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"SUPER_SECRET_KEY": "aaa"}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then
	got := result.Environments[0].Env
	if got["FOO_BAR_KEY"] != "foo" || got["SUPER_SECRET_KEY"] != "aaa" || len(got) != 2 {
		t.Errorf("expected merged env {FOO_BAR_KEY: foo, SUPER_SECRET_KEY: aaa}, got %v", got)
	}
}

func TestMerge_whenOverlayIsNameAndEnvOnly_preservesBaseWorkflowAndRun(t *testing.T) {
	// Given a base command and environment with a real definition, and an
	// overlay — the shape a secrets file takes — that names each entry and
	// sets only env, touching nothing else. This is the case the field-level
	// merge exists for: a secrets overlay must not have to repeat the whole
	// command/workflow just to add a variable.
	base := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "run.sh", Teardown: "down.sh", Source: Source{Local: "/tmp/db"}},
		},
		Environments: []Environment{
			{Name: "dev", Description: "local dev", Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "db", Env: map[string]string{"SUPER_SECRET_KEY": "aaa"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"FOO_BAR_KEY": "foo"}},
		},
	}

	// When
	result := Merge(base, overlay)

	// Then: base fields the overlay left unset survive untouched.
	cmd := result.Commands[0]
	if cmd.Type != "service" || cmd.Run != "run.sh" || cmd.Teardown != "down.sh" || cmd.Source.Local != "/tmp/db" {
		t.Errorf("expected base command fields preserved, got %+v", cmd)
	}
	if cmd.Env["SUPER_SECRET_KEY"] != "aaa" {
		t.Errorf("expected overlay env applied, got %v", cmd.Env)
	}

	env := result.Environments[0]
	if env.Description != "local dev" || len(env.Workflow) != 1 || env.Workflow[0].Command != "db" {
		t.Errorf("expected base environment fields preserved, got %+v", env)
	}
	if env.Env["FOO_BAR_KEY"] != "foo" {
		t.Errorf("expected overlay env applied, got %v", env.Env)
	}
}

func TestMerge_whenOverlaySetsFullCommand_stillReplacesUnsetFields(t *testing.T) {
	// Given an overlay that (unlike the secrets-only case) does specify run/type/
	// source — those must still take effect, matching the pre-field-merge
	// behavior for a fully-specified overlay entry.
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
	got := result.Commands[0]
	if got.Type != "task" || got.Run != "overlay.sh" || got.Source.Local != "/tmp/overlay" {
		t.Errorf("expected overlay fields to win, got %+v", got)
	}
}

func TestMerge_env_doesNotMutateInputs(t *testing.T) {
	// Given base and overlay commands/environments with env maps.
	base := &Config{
		Commands: []Command{
			{Name: "db", Type: "service", Run: "db.sh", Source: Source{Local: "/tmp/db"}, Env: map[string]string{"A": "1"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"B": "2"}, Workflow: []WorkflowStep{{Command: "db"}}},
		},
	}
	overlay := &Config{
		Commands: []Command{
			{Name: "db", Env: map[string]string{"A": "overlay", "C": "3"}},
		},
		Environments: []Environment{
			{Name: "dev", Env: map[string]string{"D": "4"}},
		},
	}

	// When
	_ = Merge(base, overlay)

	// Then: neither input's env map was mutated in place.
	if base.Commands[0].Env["A"] != "1" || len(base.Commands[0].Env) != 1 {
		t.Errorf("base command env was mutated: %v", base.Commands[0].Env)
	}
	if base.Environments[0].Env["B"] != "2" || len(base.Environments[0].Env) != 1 {
		t.Errorf("base environment env was mutated: %v", base.Environments[0].Env)
	}
	if overlay.Commands[0].Env["A"] != "overlay" || len(overlay.Commands[0].Env) != 2 {
		t.Errorf("overlay command env was mutated: %v", overlay.Commands[0].Env)
	}
}
