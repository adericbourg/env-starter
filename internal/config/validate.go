package config

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// repoPattern matches a GitHub "owner/name" slug. Both segments are restricted
// to characters GitHub actually allows, which also keeps the value from being
// interpreted as a flag or shell metacharacter when handed to git/gh.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)

// branchPattern matches a safe git ref name: ordinary ref characters only, and
// never starting with '-' (which git would parse as a flag).
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// namePattern matches a safe command name. Names are used as file names (the
// per-command log file is "<name>.log") so path separators, leading dots and
// leading dashes are rejected — a name like "../../x" would otherwise escape
// the logs directory.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]*$`)

// Validate checks the Config for correctness. It returns a descriptive error on
// the first violation found, or nil if everything is valid.
func (c *Config) Validate() error {
	// Build a set of known command names for cross-reference checks.
	commandNames := make(map[string]struct{}, len(c.Commands))

	for i, cmd := range c.Commands {
		if err := validateCommand(i, cmd, c.RequireChecksums); err != nil {
			return err
		}
		commandNames[cmd.Name] = struct{}{}
	}

	for i, env := range c.Environments {
		if err := validateEnvironment(i, env, commandNames); err != nil {
			return err
		}
	}

	if err := validateSharedEnvConflicts(c); err != nil {
		return err
	}

	return nil
}

// validateEnvMap rejects env keys that sh/exec cannot represent as a
// "KEY=VALUE" pair: empty, or containing '='.
func validateEnvMap(ownerKind, ownerName string, env map[string]string) error {
	for key := range env {
		if key == "" {
			return fmt.Errorf("%s %q: env has an empty key", ownerKind, ownerName)
		}
		if strings.Contains(key, "=") {
			return fmt.Errorf("%s %q: env key %q must not contain '='", ownerKind, ownerName, key)
		}
	}
	return nil
}

// validateSharedEnvConflicts rejects environment-level env vars that would
// resolve differently depending on which environment started a shared
// command. A command runs once globally (see internal/engine), so if two
// environments referencing the same command set the same key to different
// values, the resulting process env would depend on start order rather than
// on the config — this is caught here instead, at load time. A key the
// command itself sets is exempt: the command's Env always overrides
// environment-level Env, so there is no observable conflict.
func validateSharedEnvConflicts(c *Config) error {
	cmdEnv := make(map[string]map[string]string, len(c.Commands))
	for _, cmd := range c.Commands {
		cmdEnv[cmd.Name] = cmd.Env
	}

	type owner struct {
		envName string
		value   string
	}
	// firstOwner[commandName][key] = the first environment (and value) seen
	// setting that key for that command.
	firstOwner := make(map[string]map[string]owner)

	for _, env := range c.Environments {
		if len(env.Env) == 0 {
			continue
		}
		// A workflow may reference the same command more than once (e.g. with
		// different depends-on); only count it once per environment.
		seenCommands := make(map[string]struct{}, len(env.Workflow))
		for _, step := range env.Workflow {
			if _, dup := seenCommands[step.Command]; dup {
				continue
			}
			seenCommands[step.Command] = struct{}{}

			overrides := cmdEnv[step.Command]
			for key, value := range env.Env {
				if _, overridden := overrides[key]; overridden {
					continue
				}
				byKey := firstOwner[step.Command]
				if byKey == nil {
					byKey = make(map[string]owner)
					firstOwner[step.Command] = byKey
				}
				prior, ok := byKey[key]
				if !ok {
					byKey[key] = owner{envName: env.Name, value: value}
					continue
				}
				if prior.value != value {
					return fmt.Errorf(
						"command %q: environments %q and %q set env %q to conflicting values; duplicate the command to give each environment its own",
						step.Command, prior.envName, env.Name, key)
				}
			}
		}
	}

	return nil
}

func validateCommand(idx int, cmd Command, requireChecksums bool) error {
	if cmd.Name == "" {
		return fmt.Errorf("command[%d]: name is required", idx)
	}
	if !namePattern.MatchString(cmd.Name) {
		return fmt.Errorf("command[%d]: name %q must start with a letter or digit and contain only letters, digits, '.', '_', '-' and spaces", idx, cmd.Name)
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
	if err := validateEnvMap("command", cmd.Name, cmd.Env); err != nil {
		return err
	}
	if err := validateSource(cmd.Name, cmd.Source, requireChecksums); err != nil {
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

func validateSource(cmdName string, src Source, requireChecksums bool) error {
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
	if src.URLSource != nil {
		parsed, err := url.Parse(src.URLSource.URL)
		if err != nil {
			return fmt.Errorf("command %q: invalid url %q: %w", cmdName, src.URLSource.URL, err)
		}
		if parsed.Scheme != "https" {
			return fmt.Errorf("command %q: url source must use https, got scheme %q", cmdName, parsed.Scheme)
		}
		if src.URLSource.Checksum == nil && requireChecksums {
			return fmt.Errorf("command %q: url source has no checksum and require-checksums is enabled", cmdName)
		}
		if err := validateChecksum(cmdName, src.URLSource.Checksum); err != nil {
			return err
		}
	}
	if src.GitHub != nil {
		if !repoPattern.MatchString(src.GitHub.Repo) {
			return fmt.Errorf("command %q: github repo %q must be of the form owner/name (letters, digits, '.', '_', '-')", cmdName, src.GitHub.Repo)
		}
		if src.GitHub.Branch != "" && (!branchPattern.MatchString(src.GitHub.Branch) || strings.HasPrefix(src.GitHub.Branch, "-")) {
			return fmt.Errorf("command %q: github branch %q is not a valid ref name", cmdName, src.GitHub.Branch)
		}
	}
	if err := validateSubdir(cmdName, src.Subdir); err != nil {
		return err
	}
	return nil
}

// checksumValuePattern matches a sha256 digest: exactly 64 hex characters.
var checksumValuePattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// validateChecksum rejects a malformed checksum at config load rather than at
// fetch time, so a typo'd digest (or unsupported algorithm) is reported before
// anything is downloaded.
func validateChecksum(cmdName string, cs *Checksum) error {
	if cs == nil {
		return nil
	}
	if cs.Alg != "sha256" {
		return fmt.Errorf("command %q: checksum alg must be \"sha256\", got %q", cmdName, cs.Alg)
	}
	if !checksumValuePattern.MatchString(cs.Value) {
		return fmt.Errorf("command %q: checksum value must be a 64-character hex sha256 digest", cmdName)
	}
	return nil
}

// validateSubdir rejects a subdir that is absolute or escapes the source
// directory. filepath.Join would otherwise let "../.." reach outside the cache.
func validateSubdir(cmdName, subdir string) error {
	if subdir == "" {
		return nil
	}
	if path.IsAbs(subdir) || strings.HasPrefix(subdir, "/") {
		return fmt.Errorf("command %q: subdir %q must be relative", cmdName, subdir)
	}
	// Clean with a fixed root and confirm it stays under it.
	cleaned := path.Clean("/" + subdir)
	if cleaned == "/" || strings.HasPrefix(cleaned, "/../") || strings.Contains(subdir, "..") {
		return fmt.Errorf("command %q: subdir %q must not escape the source directory", cmdName, subdir)
	}
	return nil
}

// Warnings returns non-fatal advisories about the configuration. Unlike
// Validate, these do not block loading; they surface footguns the user may want
// to address (e.g. a url source downloaded without an integrity checksum, which
// is then trusted on TLS alone).
func (c *Config) Warnings() []string {
	var warnings []string
	for _, cmd := range c.Commands {
		if cmd.Source.URLSource != nil && cmd.Source.URLSource.Checksum == nil {
			warnings = append(warnings, fmt.Sprintf(
				"command %q: url source has NO checksum — the downloaded file will be executed "+
					"trusting TLS alone, so a tampered or swapped artifact at the origin goes "+
					"undetected. Add a sha256 checksum to the source, or set require-checksums: true "+
					"at the top level to make this an error.", cmd.Name))
		}
	}
	return warnings
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
	if err := validateEnvMap("environment", env.Name, env.Env); err != nil {
		return err
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
