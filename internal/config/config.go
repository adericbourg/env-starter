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
type Readiness struct {
	TCP      string    `yaml:"tcp,omitempty"`
	Shell    string    `yaml:"shell,omitempty"`
	HTTP     string    `yaml:"http,omitempty"`
	Log      string    `yaml:"log,omitempty"`
	Timeout  *Duration `yaml:"timeout,omitempty"`
	Interval *Duration `yaml:"interval,omitempty"`
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
	Commands     []Command     `yaml:"commands"`
	Environments []Environment `yaml:"environments"`
}

// envStarterWrapper is used to unwrap the top-level "env-starter:" key.
type envStarterWrapper struct {
	EnvStarter Config `yaml:"env-starter"`
}
