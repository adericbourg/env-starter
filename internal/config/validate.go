package config

import (
	"errors"
	"fmt"
	"strings"
)

// Validate checks the Config for correctness. It returns a descriptive error on
// the first violation found, or nil if everything is valid.
func (c *Config) Validate() error {
	// Build a set of known command names for cross-reference checks.
	commandNames := make(map[string]struct{}, len(c.Commands))

	for i, cmd := range c.Commands {
		if err := validateCommand(i, cmd); err != nil {
			return err
		}
		commandNames[cmd.Name] = struct{}{}
	}

	for i, env := range c.Environments {
		if err := validateEnvironment(i, env, commandNames); err != nil {
			return err
		}
	}

	return nil
}

func validateCommand(idx int, cmd Command) error {
	if cmd.Name == "" {
		return fmt.Errorf("command[%d]: name is required", idx)
	}
	if cmd.Type == "" {
		return fmt.Errorf("command %q: type is required", cmd.Name)
	}
	if cmd.Type != "service" && cmd.Type != "task" {
		return fmt.Errorf("command %q: type must be \"service\" or \"task\", got %q", cmd.Name, cmd.Type)
	}
	if cmd.Run == "" {
		return fmt.Errorf("command %q: run is required", cmd.Name)
	}
	for i, step := range cmd.Setup {
		if strings.TrimSpace(step) == "" {
			return fmt.Errorf("command %q: setup[%d] must not be empty", cmd.Name, i)
		}
	}
	if err := validateSource(cmd.Name, cmd.Source); err != nil {
		return err
	}
	if cmd.Readiness != nil {
		if err := validateReadiness(cmd.Name, *cmd.Readiness); err != nil {
			return err
		}
	}
	if cmd.Restart != nil {
		if err := validateRestart(cmd.Name, *cmd.Restart); err != nil {
			return err
		}
	}
	// Cross-field: restart on a task requires a readiness probe (the liveness probe
	// is the only failure signal — the process exits normally on success).
	if cmd.Type == "task" && cmd.Restart != nil && cmd.Readiness == nil {
		if cmd.Restart.Enabled == nil || *cmd.Restart.Enabled {
			return fmt.Errorf("command %q: restart on a task requires a readiness probe", cmd.Name)
		}
	}
	return nil
}

func validateRestart(cmdName string, r Restart) error {
	if r.MaxRetries != nil && *r.MaxRetries < 0 {
		return fmt.Errorf("command %q: restart.max-retries must not be negative, got %d", cmdName, *r.MaxRetries)
	}
	return nil
}

func validateSource(cmdName string, src Source) error {
	count := 0
	if src.GitHub != nil {
		count++
	}
	if src.URLSource != nil {
		count++
	}
	if src.Local != "" {
		count++
	}
	if count == 0 {
		return fmt.Errorf("command %q: source must specify exactly one of github, url, or local (got none)", cmdName)
	}
	if count > 1 {
		return fmt.Errorf("command %q: source must specify exactly one of github, url, or local (got %d)", cmdName, count)
	}
	return nil
}

func validateReadiness(cmdName string, r Readiness) error {
	// Reject unsupported variants (http/log are future variants).
	if r.HTTP != "" {
		return fmt.Errorf("command %q: readiness probe \"http\" is not yet supported; use tcp or shell", cmdName)
	}
	if r.Log != "" {
		return fmt.Errorf("command %q: readiness probe \"log\" is not yet supported; use tcp or shell", cmdName)
	}

	count := 0
	if r.TCP != "" {
		count++
	}
	if r.Shell != "" {
		count++
	}
	if count > 1 {
		return fmt.Errorf("command %q: readiness must specify at most one of tcp or shell", cmdName)
	}
	return nil
}

func validateEnvironment(idx int, env Environment, commandNames map[string]struct{}) error {
	if env.Name == "" {
		return fmt.Errorf("environment[%d]: name is required", idx)
	}
	if len(env.Workflow) == 0 {
		return fmt.Errorf("environment %q: workflow must be non-empty", env.Name)
	}

	// Collect the set of command names used in this workflow (for depends-on validation).
	workflowCommandNames := make(map[string]struct{}, len(env.Workflow))
	for _, step := range env.Workflow {
		workflowCommandNames[step.Command] = struct{}{}
	}

	for i, step := range env.Workflow {
		if step.Command == "" {
			return fmt.Errorf("environment %q, workflow step[%d]: command is required", env.Name, i)
		}
		// Step must reference a defined command.
		if _, ok := commandNames[step.Command]; !ok {
			return fmt.Errorf("environment %q, workflow step[%d]: command %q is not defined", env.Name, i, step.Command)
		}
		// Each depends-on entry must reference a command present in this workflow.
		for _, dep := range step.DependsOn {
			if _, ok := workflowCommandNames[dep]; !ok {
				return fmt.Errorf("environment %q, workflow step[%d] (command %q): depends-on %q is not present in this workflow",
					env.Name, i, step.Command, dep)
			}
		}
	}

	// Detect cycles in the workflow dependency graph.
	if err := detectCycle(env); err != nil {
		return fmt.Errorf("environment %q: %w", env.Name, err)
	}

	return nil
}

// detectCycle performs a DFS-based cycle detection on the workflow dependency
// graph. Each node is a workflow step (identified by command name). Edges go
// from a step to each of its depends-on steps.
func detectCycle(env Environment) error {
	// Build adjacency list: command -> list of commands it depends on.
	adj := make(map[string][]string, len(env.Workflow))
	for _, step := range env.Workflow {
		adj[step.Command] = step.DependsOn
	}

	// Three-color DFS: 0=white (unvisited), 1=gray (in stack), 2=black (done).
	color := make(map[string]int, len(env.Workflow))

	var dfs func(node string) error
	dfs = func(node string) error {
		color[node] = 1 // gray
		for _, dep := range adj[node] {
			if color[dep] == 1 {
				return fmt.Errorf("dependency cycle detected involving command %q", dep)
			}
			if color[dep] == 0 {
				if err := dfs(dep); err != nil {
					return err
				}
			}
		}
		color[node] = 2 // black
		return nil
	}

	for _, step := range env.Workflow {
		if color[step.Command] == 0 {
			if err := dfs(step.Command); err != nil {
				return errors.New(err.Error())
			}
		}
	}

	return nil
}
