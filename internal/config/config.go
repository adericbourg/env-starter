package config

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a wrapper around time.Duration that supports YAML unmarshaling
// from string values like "60s", "1m30s".
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// GitHub holds configuration for sourcing from a GitHub repository.
type GitHub struct {
	Repo   string `yaml:"repo"`
	Branch string `yaml:"branch"`
	// Method is optional: ssh|https|gh
	Method string `yaml:"method,omitempty"`
}

// Checksum holds an algorithm + hex value for verifying a downloaded artifact.
type Checksum struct {
	Alg   string `yaml:"alg"`
	Value string `yaml:"value"`
}

// URL holds configuration for sourcing from an arbitrary URL.
type URL struct {
	URL      string
	Checksum *Checksum
}

// Source describes where a command's executable/scripts come from.
// Exactly one of GitHub, URLSource, or Local must be set.
//
// The YAML shape is flat: "url" is a scalar string sibling to "checksum",
// "github", and "local" under the source mapping.
type Source struct {
	GitHub    *GitHub   `yaml:"github,omitempty"`
	URLSource *URL      `yaml:"-"` // populated via custom UnmarshalYAML
	Local     string    `yaml:"local,omitempty"`
	Subdir    string    `yaml:"subdir,omitempty"`
	Checksum  *Checksum `yaml:"checksum,omitempty"` // only used when URLSource is set
}

// sourceYAML mirrors the YAML shape for unmarshaling before we build Source.
type sourceYAML struct {
	GitHub   *GitHub   `yaml:"github"`
	URL      string    `yaml:"url"`
	Local    string    `yaml:"local"`
	Subdir   string    `yaml:"subdir"`
	Checksum *Checksum `yaml:"checksum"`
}

func (s *Source) UnmarshalYAML(value *yaml.Node) error {
	var raw sourceYAML
	if err := value.Decode(&raw); err != nil {
		return err
	}
	s.GitHub = raw.GitHub
	s.Local = raw.Local
	s.Subdir = raw.Subdir
	if raw.URL != "" {
		s.URLSource = &URL{URL: raw.URL, Checksum: raw.Checksum}
	}
	return nil
}

// Readiness describes how to probe whether a command is ready.
// Exactly one of TCP or Shell must be set.
//
// For tasks with a readiness probe: after the process exits 0 the probe is run
// to confirm the side effect is up (e.g. a tunnel accepting connections). The
// task is marked healthy when the probe passes, and its dependents unblock.
// If restart is also configured, the probe is re-used as the liveness check.
type Readiness struct {
	TCP      string    `yaml:"tcp,omitempty"`
	Shell    string    `yaml:"shell,omitempty"`
	HTTP     string    `yaml:"http,omitempty"`
	Log      string    `yaml:"log,omitempty"`
	Timeout  *Duration `yaml:"timeout,omitempty"`
	Interval *Duration `yaml:"interval,omitempty"`
}

// Restart configures automatic restart behaviour for a command.
//
// For services: auto-restart is on by default (no block needed).
// For tasks: restart is opt-in — a restart block must be declared AND the
// command must have a readiness probe (the liveness probe is the only failure
// signal once the process exits). A task with restart: {enabled: false} and
// no readiness probe is allowed (the block is a no-op).
type Restart struct {
	// Enabled controls whether auto-restart is active. Defaults to true for
	// services; tasks opt in by declaring a restart block.
	Enabled *bool `yaml:"enabled,omitempty"`
	// MaxRetries is the number of restart attempts before giving up. Default 3.
	MaxRetries *int `yaml:"max-retries,omitempty"`
	// BackoffBase is the delay before the first retry; subsequent delays double.
	// Default 1s (giving waits of 1s, 2s, 4s for the default 3 retries).
	BackoffBase *Duration `yaml:"backoff-base,omitempty"`
	// CheckInterval is the period at which the readiness probe is re-run after
	// the command is healthy. Default 10s. Set to 0 to disable liveness checking
	// (crashes are still detected for services; for tasks this disables monitoring
	// entirely since the process is already gone).
	CheckInterval *Duration `yaml:"check-interval,omitempty"`
}

// Command represents a runnable unit (service or task).
type Command struct {
	Name      string            `yaml:"name"`
	Type      string            `yaml:"type"`
	Source    Source            `yaml:"source"`
	Setup     []string          `yaml:"setup,omitempty"`
	Run       string            `yaml:"run"`
	Teardown  string            `yaml:"teardown,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Readiness *Readiness        `yaml:"readiness,omitempty"`
	Restart   *Restart          `yaml:"restart,omitempty"`
	// InteractiveAuth marks a command whose run process performs an interactive
	// browser-based login (e.g. Teleport/tsh, JumpCloud, Okta, Google SSO). Most
	// SSO providers reject parallel login attempts, so the engine serializes the
	// launch of every command with this flag set: only one runs at a time.
	InteractiveAuth bool `yaml:"interactive-auth,omitempty"`
}

// WorkflowStep is a reference to a command within an environment workflow.
type WorkflowStep struct {
	Command   string   `yaml:"command"`
	DependsOn []string `yaml:"depends-on,omitempty"`
}

// Environment groups a named set of workflow steps.
type Environment struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description,omitempty"`
	Workflow    []WorkflowStep `yaml:"workflow"`
}

// Config is the root configuration object.
type Config struct {
	// RequireChecksums makes a missing checksum on a url source a validation
	// error instead of a warning. Without a checksum, the downloaded file —
	// which is executed as code — is trusted on TLS alone, so a swapped or
	// tampered artifact at the origin goes undetected.
	RequireChecksums bool          `yaml:"require-checksums,omitempty"`
	Commands         []Command     `yaml:"commands"`
	Environments     []Environment `yaml:"environments"`
}

// envStarterWrapper is used to unwrap the top-level "env-starter:" key.
type envStarterWrapper struct {
	EnvStarter Config `yaml:"env-starter"`
}
