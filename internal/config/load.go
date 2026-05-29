package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads a YAML config file from path, unmarshals it, validates it, and
// returns the parsed Config or an error.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	var wrapper envStarterWrapper
	if err := yaml.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	cfg := &wrapper.EnvStarter
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}

	return cfg, nil
}
