package engine

import (
	"os"
	"sort"
	"strings"
)

// EnvSource identifies which layer produced a resolved env value.
type EnvSource string

const (
	EnvSourceOS          EnvSource = "os"
	EnvSourceEnvironment EnvSource = "environment"
	EnvSourceCommand     EnvSource = "command"
)

// EnvLayer is one layer's contribution to a key: the value it set, and which
// source set it.
type EnvLayer struct {
	Value  string
	Source EnvSource
}

// ResolvedEnvVar is one key's full provenance: the value actually applied
// (Winning) plus every lower-priority layer it shadows, nearest first, so a
// caller can show what a variable overrides without a second lookup.
type ResolvedEnvVar struct {
	Key      string
	Winning  EnvLayer
	Shadowed []EnvLayer
}

// ResolveEnv resolves the env variables visible to the given environment or
// command, with full layer provenance. Pass a non-empty command to resolve
// exactly what that command's process receives (OS env, overlaid by the
// union of its current holders' environment-level env, overlaid by the
// command's own env — the same layering effectiveEnv applies at launch).
// Otherwise pass a non-empty envName to resolve what every command in that
// environment's workflow inherits before its own overrides (OS env, overlaid
// by the environment's own env). Returns nil for an unknown name.
func (e *Engine) ResolveEnv(envName, command string) []ResolvedEnvVar {
	if command != "" {
		return e.resolveCommandEnv(command)
	}
	return e.resolveEnvironmentEnv(envName)
}

func (e *Engine) resolveEnvironmentEnv(envName string) []ResolvedEnvVar {
	env, ok := e.findEnv(envName)
	if !ok {
		return nil
	}
	return resolveLayers(
		envLayerInput{EnvSourceOS, osEnvironMap()},
		envLayerInput{EnvSourceEnvironment, env.Env},
	)
}

func (e *Engine) resolveCommandEnv(command string) []ResolvedEnvVar {
	e.mu.Lock()
	cmdCfg, ok := e.cmdOf[command]
	c, exists := e.commands[command]
	e.mu.Unlock()
	if !ok {
		return nil
	}

	var holderEnv map[string]string
	if exists {
		holderEnv = e.holderEnvUnion(c)
	}

	return resolveLayers(
		envLayerInput{EnvSourceOS, osEnvironMap()},
		envLayerInput{EnvSourceEnvironment, holderEnv},
		envLayerInput{EnvSourceCommand, cmdCfg.Env},
	)
}

// envLayerInput is one named source's raw key/value map, passed to
// resolveLayers in priority order (later layers win).
type envLayerInput struct {
	source EnvSource
	values map[string]string
}

// resolveLayers merges layers in priority order into ResolvedEnvVar entries
// carrying full provenance. Results are sorted with config-defined keys
// (environment- or command-sourced) before OS-only keys, alphabetically
// within each group — what the user configured is what they're here to
// inspect; inherited OS noise sorts after.
func resolveLayers(layers ...envLayerInput) []ResolvedEnvVar {
	byKey := make(map[string][]EnvLayer)
	seen := make(map[string]struct{})
	var order []string

	for _, l := range layers {
		keys := make([]string, 0, len(l.values))
		for k := range l.values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			byKey[k] = append(byKey[k], EnvLayer{Value: l.values[k], Source: l.source})
			if _, ok := seen[k]; !ok {
				seen[k] = struct{}{}
				order = append(order, k)
			}
		}
	}

	out := make([]ResolvedEnvVar, 0, len(order))
	for _, k := range order {
		ls := byKey[k]
		shadowed := make([]EnvLayer, 0, len(ls)-1)
		for i := len(ls) - 2; i >= 0; i-- {
			shadowed = append(shadowed, ls[i])
		}
		out = append(out, ResolvedEnvVar{Key: k, Winning: ls[len(ls)-1], Shadowed: shadowed})
	}

	sort.SliceStable(out, func(i, j int) bool {
		iOS := out[i].Winning.Source == EnvSourceOS
		jOS := out[j].Winning.Source == EnvSourceOS
		if iOS != jOS {
			return !iOS
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// osEnvironMap converts os.Environ() ("KEY=VALUE" entries) to a map.
func osEnvironMap() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}
