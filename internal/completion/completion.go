package completion

import "strings"

// Directive tells the shell what to do after candidate display.
type Directive int

const (
	// DirectiveDefault means display the candidates produced by this call.
	DirectiveDefault Directive = 0
	// DirectiveFiles means fall back to native file-path completion.
	DirectiveFiles Directive = 1
)

// NameDesc is a name/description pair (e.g. an environment from config).
type NameDesc struct {
	Name        string
	Description string
}

// Result holds completion candidates and a shell directive.
// Each candidate is formatted as "value" or "value\tdescription".
type Result struct {
	Candidates []string
	Directive  Directive
}

// subcommands is the canonical ordered list of user-facing subcommands.
var subcommands = []NameDesc{
	{Name: "run", Description: "start a named environment"},
	{Name: "stop", Description: "stop a named environment"},
	{Name: "list", Description: "list configured environments"},
	{Name: "ps", Description: "show running environments"},
	{Name: "command", Description: "manage individual commands (list, restart)"},
	{Name: "shutdown", Description: "stop the daemon and all environments"},
	{Name: "update", Description: "update env-starter to the latest release"},
	{Name: "help", Description: "show usage"},
	{Name: "completion", Description: "generate shell completion scripts"},
}

// cmdFlags maps each subcommand (or "" for the default TUI path) to its flag names.
// Only user-facing subcommands are listed; hidden ones (__daemon, __complete) are omitted.
var cmdFlags = map[string][]string{
	"":           {"--version", "--config", "--config-overlay"},
	"run":        {"--timeout", "--config", "--config-overlay"},
	"list":       {"--config", "--config-overlay"},
	"stop":       {},
	"ps":         {},
	"command":    {},
	"shutdown":   {},
	"update":     {},
	"help":       {},
	"completion": {},
}

// commandVerbs is the ordered list of "env-starter command <verb>" sub-verbs.
var commandVerbs = []NameDesc{
	{Name: "list", Description: "show started commands and their state"},
	{Name: "restart", Description: "restart a single command"},
}

// flagTakesValue lists flags that consume the next token as their value.
// Used to correctly skip flag-value pairs when counting positional arguments.
var flagTakesValue = map[string]bool{
	"--config":         true,
	"--config-overlay": true,
	"--timeout":        true,
}

// Complete computes shell completion candidates for the typed words.
//
// words contains the arguments typed so far, with the program name excluded.
// The last element is the partial word currently being completed (may be "").
// envNames provides environment names and descriptions loaded from the offline
// config (no daemon required). cmdNames optionally provides command names for
// completing "env-starter command restart <TAB>"; existing callers that only
// deal with environments can omit it.
func Complete(words []string, envNames []NameDesc, cmdNames ...NameDesc) Result {
	if len(words) == 0 {
		return Result{Candidates: format(subcommands)}
	}

	current := words[len(words)-1]

	var prev string
	if len(words) >= 2 {
		prev = words[len(words)-2]
	}

	// File flag: the previous word expects a file path as its value.
	if isFileFlag(prev) {
		return Result{Directive: DirectiveFiles}
	}

	// First word: either a flag (starts with "-") or the subcommand name.
	if len(words) == 1 {
		if strings.HasPrefix(current, "-") {
			// Completing a flag on the default TUI path (no subcommand).
			return Result{
				Candidates: filterByPrefix(toStrings(cmdFlags[""]), current),
			}
		}
		return Result{
			Candidates: filterByPrefix(format(subcommands), current),
		}
	}

	// We have a subcommand in words[0].
	sub := words[0]

	// Completing a flag name.
	if strings.HasPrefix(current, "-") {
		flags, ok := cmdFlags[sub]
		if !ok {
			// Unknown subcommand — no candidates.
			return Result{}
		}
		return Result{
			Candidates: filterByPrefix(toStrings(flags), current),
		}
	}

	// For run/stop: complete the env-name positional slot.
	if (sub == "run" || sub == "stop") && isPositionalSlot(words) {
		return Result{
			Candidates: filterByPrefix(format(envNames), current),
		}
	}

	// For command: complete the verb (list/restart), then — for restart —
	// the command-name positional slot.
	if sub == "command" {
		if len(words) == 2 {
			return Result{Candidates: filterByPrefix(format(commandVerbs), current)}
		}
		if words[1] == "restart" && isPositionalSlotFrom(words, 2) {
			return Result{Candidates: filterByPrefix(format(cmdNames), current)}
		}
		return Result{}
	}

	return Result{}
}

// isFileFlag reports whether flag is one whose value is a file path.
func isFileFlag(flag string) bool {
	return flag == "--config" || flag == "--config-overlay"
}

// isPositionalSlot returns true when we are completing the first positional
// (non-flag) argument after the subcommand (words[0]).
// It skips over flags and their values correctly.
func isPositionalSlot(words []string) bool {
	return isPositionalSlotFrom(words, 1)
}

// isPositionalSlotFrom returns true when we are completing the first
// positional (non-flag) argument starting at index start (e.g. start=2 for
// "command restart <TAB>", skipping over "command" and "restart").
// It skips over flags and their values correctly.
func isPositionalSlotFrom(words []string, start int) bool {
	positionals := 0
	i := start
	for i < len(words)-1 { // exclude the current partial (last element)
		w := words[i]
		if flagTakesValue[w] {
			i += 2 // skip flag + its value token
			continue
		}
		if strings.HasPrefix(w, "-") {
			i++ // boolean flag, no value token
			continue
		}
		positionals++
		i++
	}
	return positionals == 0
}

// format converts a []NameDesc into "value\tdescription" candidate strings.
func format(items []NameDesc) []string {
	out := make([]string, len(items))
	for i, item := range items {
		if item.Description != "" {
			out[i] = item.Name + "\t" + item.Description
		} else {
			out[i] = item.Name
		}
	}
	return out
}

// toStrings copies a string slice (flag names have no descriptions).
func toStrings(ss []string) []string {
	out := make([]string, len(ss))
	copy(out, ss)
	return out
}

// filterByPrefix returns candidates whose value (before the first tab) has
// the given prefix. An empty prefix returns all candidates unchanged.
func filterByPrefix(candidates []string, prefix string) []string {
	if prefix == "" {
		return candidates
	}
	var out []string
	for _, c := range candidates {
		value := c
		if idx := strings.IndexByte(c, '\t'); idx >= 0 {
			value = c[:idx]
		}
		if strings.HasPrefix(value, prefix) {
			out = append(out, c)
		}
	}
	return out
}
