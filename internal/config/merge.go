package config

// Merge returns a new Config that merges overlay onto base.
// Rules:
//   - Commands and Environments are matched by name.
//   - An overlay entry with the same name replaces the base entry (in place).
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

	// Walk base in order; replace with overlay entry when names match.
	for _, cmd := range base {
		if ov, ok := overlayIndex[cmd.Name]; ok {
			result = append(result, ov)
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

func mergeEnvironments(base, overlay []Environment) []Environment {
	overlayIndex := make(map[string]Environment, len(overlay))
	for _, env := range overlay {
		overlayIndex[env.Name] = env
	}

	consumed := make(map[string]bool, len(overlay))

	result := make([]Environment, 0, len(base)+len(overlay))

	for _, env := range base {
		if ov, ok := overlayIndex[env.Name]; ok {
			result = append(result, ov)
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
