package config

// Merge returns a new Config that merges overlay onto base.
// Rules:
//   - Commands and Environments are matched by name.
//   - An overlay entry with the same name is field-merged onto the base entry
//     (in place): for each field, the overlay's value wins when it is set
//     (non-zero/non-nil); otherwise the base's value is kept. Env maps merge
//     key-by-key, with the overlay winning per key. A field cannot be
//     *cleared* via overlay — a zero value always means "inherit from base" —
//     which is what lets a secrets overlay declare only {name, env} and still
//     preserve the base command's run/type/source/… or environment's workflow.
//   - Overlay entries with new names are appended after all base entries.
//   - Base-only entries are kept as-is.
//   - Neither base nor overlay is mutated.
func Merge(base, overlay *Config) *Config {
	return &Config{
		// Strictness never merges downward: if either file requires checksums,
		// the merged config does — an overlay cannot relax the base's policy.
		RequireChecksums: base.RequireChecksums || overlay.RequireChecksums,
		Commands:         mergeCommands(base.Commands, overlay.Commands),
		Environments:     mergeEnvironments(base.Environments, overlay.Environments),
	}
}

func mergeCommands(base, overlay []Command) []Command {
	// Index overlay commands by name for O(1) lookup.
	overlayIndex := make(map[string]Command, len(overlay))
	for _, cmd := range overlay {
		overlayIndex[cmd.Name] = cmd
	}

	// Track which overlay names have been consumed (i.e., replaced a base entry).
	consumed := make(map[string]bool, len(overlay))

	result := make([]Command, 0, len(base)+len(overlay))

	// Walk base in order; field-merge the overlay entry when names match.
	for _, cmd := range base {
		if ov, ok := overlayIndex[cmd.Name]; ok {
			result = append(result, mergeCommandFields(cmd, ov))
			consumed[cmd.Name] = true
		} else {
			result = append(result, cmd)
		}
	}

	// Append overlay entries that didn't replace any base entry.
	for _, cmd := range overlay {
		if !consumed[cmd.Name] {
			result = append(result, cmd)
		}
	}

	return result
}

// mergeCommandFields field-merges overlay onto base for a same-named command:
// the overlay's value wins per field when set, else base's is kept. Env maps
// merge key-by-key (overlay wins per key) rather than replacing wholesale.
func mergeCommandFields(base, overlay Command) Command {
	merged := base
	if overlay.Type != "" {
		merged.Type = overlay.Type
	}
	if !isZeroSource(overlay.Source) {
		merged.Source = overlay.Source
	}
	if overlay.Setup != nil {
		merged.Setup = overlay.Setup
	}
	if overlay.Run != "" {
		merged.Run = overlay.Run
	}
	if overlay.Teardown != "" {
		merged.Teardown = overlay.Teardown
	}
	merged.Env = mergeEnvMap(base.Env, overlay.Env)
	if overlay.Readiness != nil {
		merged.Readiness = overlay.Readiness
	}
	if overlay.Restart != nil {
		merged.Restart = overlay.Restart
	}
	if overlay.InteractiveAuth != nil {
		merged.InteractiveAuth = overlay.InteractiveAuth
	}
	return merged
}

// isZeroSource reports whether s declares no source variant at all, so a
// same-named overlay command that omits `source` entirely inherits the
// base's source rather than replacing it with an empty (invalid) one.
func isZeroSource(s Source) bool {
	return s.GitHub == nil && s.URLSource == nil && s.Local == "" && s.Subdir == ""
}

func mergeEnvironments(base, overlay []Environment) []Environment {
	overlayIndex := make(map[string]Environment, len(overlay))
	for _, env := range overlay {
		overlayIndex[env.Name] = env
	}

	consumed := make(map[string]bool, len(overlay))

	result := make([]Environment, 0, len(base)+len(overlay))

	for _, env := range base {
		if ov, ok := overlayIndex[env.Name]; ok {
			result = append(result, mergeEnvironmentFields(env, ov))
			consumed[env.Name] = true
		} else {
			result = append(result, env)
		}
	}

	for _, env := range overlay {
		if !consumed[env.Name] {
			result = append(result, env)
		}
	}

	return result
}

// mergeEnvironmentFields field-merges overlay onto base for a same-named
// environment, mirroring mergeCommandFields. This is what lets a secrets
// overlay declare only {name, env} and still preserve the base's workflow.
func mergeEnvironmentFields(base, overlay Environment) Environment {
	merged := base
	if overlay.Description != "" {
		merged.Description = overlay.Description
	}
	merged.Env = mergeEnvMap(base.Env, overlay.Env)
	if overlay.Workflow != nil {
		merged.Workflow = overlay.Workflow
	}
	if overlay.AutoStart != nil {
		merged.AutoStart = overlay.AutoStart
	}
	return merged
}

// mergeEnvMap merges overlay's keys onto base's, with overlay winning on a
// shared key. Returns nil (rather than an allocated empty map) when both
// inputs are empty, so an env-less merge stays indistinguishable from "unset".
func mergeEnvMap(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overlay {
		merged[k] = v
	}
	return merged
}
