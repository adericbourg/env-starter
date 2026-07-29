package engine

import (
	"maps"
	"reflect"
	"slices"

	"github.com/adericbourg/env-starter/internal/config"
)

// envChangeKind is a bitset: an environment diff can touch more than one kind
// of field at once (e.g. both env and workflow changed).
type envChangeKind uint8

const (
	// envChangedCosmetic is set when Description or AutoStart differ. Neither
	// affects a running command, so a change carrying only this bit triggers
	// no restart.
	envChangedCosmetic envChangeKind = 1 << iota
	// envChangedEnv is set when the environment's Env map differs. This does
	// not by itself restart the whole environment: it is resolved per command
	// against the command's actual effective env (see maybeRestartForEnvChange).
	envChangedEnv
	// envChangedWorkflow is set when the workflow (steps, order, or
	// depends-on) differs. Restarting the environment is the only way to
	// apply a workflow change: dependency edges and holder membership must be
	// rebuilt from scratch.
	envChangedWorkflow
)

// envChange describes how one environment (present in both configs, matched
// by name) differs. old is kept so the caller can release exactly what was
// acquired under the previous workflow.
type envChange struct {
	kinds envChangeKind
	old   config.Environment
	new   config.Environment
}

// configDiff is the structural difference between two configs, matched by
// name. It carries no engine state and does not know what is currently
// running — that reconciliation happens in applyconfig.go.
type configDiff struct {
	// removedCommands lists commands present in old but absent from new.
	removedCommands []string
	// changedCommands lists commands present in both whose definition
	// differs (reflect.DeepEqual, which follows the Readiness/Restart/Source
	// pointer fields).
	changedCommands []string
	// removedEnvs carries the OLD value of every environment present in old
	// but absent from new, so its workflow can still be released.
	removedEnvs []config.Environment
	// changedEnvs maps environment name to its diff, for every environment
	// present in both configs where at least one field differs. An
	// environment absent from this map is identical in both configs.
	changedEnvs map[string]envChange
}

// diffConfig computes the structural difference between old and new,
// matching commands and environments by name. Both configs are assumed
// already validated.
func diffConfig(old, new *config.Config) configDiff {
	oldCmds := indexCommands(old)
	newCmds := indexCommands(new)

	var diff configDiff
	for name, oldCmd := range oldCmds {
		newCmd, ok := newCmds[name]
		if !ok {
			diff.removedCommands = append(diff.removedCommands, name)
			continue
		}
		if !reflect.DeepEqual(oldCmd, newCmd) {
			diff.changedCommands = append(diff.changedCommands, name)
		}
	}

	oldEnvs := indexEnvironments(old)
	newEnvs := indexEnvironments(new)

	diff.changedEnvs = make(map[string]envChange)
	for name, oldEnv := range oldEnvs {
		newEnv, ok := newEnvs[name]
		if !ok {
			diff.removedEnvs = append(diff.removedEnvs, oldEnv)
			continue
		}
		if kinds := environmentDiff(oldEnv, newEnv); kinds != 0 {
			diff.changedEnvs[name] = envChange{kinds: kinds, old: oldEnv, new: newEnv}
		}
	}

	return diff
}

func indexCommands(cfg *config.Config) map[string]config.Command {
	out := make(map[string]config.Command, len(cfg.Commands))
	for _, c := range cfg.Commands {
		out[c.Name] = c
	}
	return out
}

func indexEnvironments(cfg *config.Config) map[string]config.Environment {
	out := make(map[string]config.Environment, len(cfg.Environments))
	for _, e := range cfg.Environments {
		out[e.Name] = e
	}
	return out
}

// environmentDiff classifies how old and new differ, field by field. A
// reordered but otherwise identical depends-on list is treated as a workflow
// change: simpler and safe, even though it is semantically a no-op.
func environmentDiff(old, new config.Environment) envChangeKind {
	var kinds envChangeKind
	if old.Description != new.Description || !boolPtrEqual(old.AutoStart, new.AutoStart) {
		kinds |= envChangedCosmetic
	}
	if !maps.Equal(old.Env, new.Env) {
		kinds |= envChangedEnv
	}
	if !workflowEqual(old.Workflow, new.Workflow) {
		kinds |= envChangedWorkflow
	}
	return kinds
}

// boolPtrEqual compares two optional bools by value rather than pointer
// identity, since Load produces a fresh *bool on every parse even when the
// underlying YAML is unchanged.
func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func workflowEqual(a, b []config.WorkflowStep) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Command != b[i].Command || !slices.Equal(a[i].DependsOn, b[i].DependsOn) {
			return false
		}
	}
	return true
}
