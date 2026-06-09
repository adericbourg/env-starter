package completion_test

import (
	"strings"
	"testing"

	"github.com/adericbourg/env-starter/internal/completion"
)

// testEnvs is a fixed set of environments used across tests.
var testEnvs = []completion.NameDesc{
	{Name: "frontend", Description: "Frontend stack"},
	{Name: "backend", Description: "Backend services"},
	{Name: "full-stack", Description: "Everything"},
}

// --- helpers ---

// candidateValues extracts the value part (before the first tab) from each
// candidate string, so tests can check names without coupling to descriptions.
func candidateValues(candidates []string) []string {
	out := make([]string, len(candidates))
	for i, c := range candidates {
		if idx := strings.IndexByte(c, '\t'); idx >= 0 {
			out[i] = c[:idx]
		} else {
			out[i] = c
		}
	}
	return out
}

// containsAll asserts that all expected values appear in the candidate list.
func containsAll(t *testing.T, candidates []string, expected ...string) {
	t.Helper()
	values := candidateValues(candidates)
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	for _, e := range expected {
		if !set[e] {
			t.Errorf("expected candidate %q not found in %v", e, values)
		}
	}
}

// notContains asserts that the given value does NOT appear in the candidates.
func notContains(t *testing.T, candidates []string, unexpected string) {
	t.Helper()
	for _, v := range candidateValues(candidates) {
		if v == unexpected {
			t.Errorf("unexpected candidate %q found in %v", unexpected, candidateValues(candidates))
			return
		}
	}
}

// exactValues asserts that the candidates match exactly the given values (order-insensitive).
func exactValues(t *testing.T, candidates []string, expected ...string) {
	t.Helper()
	values := candidateValues(candidates)
	if len(values) != len(expected) {
		t.Errorf("candidates = %v, want exactly %v", values, expected)
		return
	}
	set := make(map[string]bool, len(expected))
	for _, e := range expected {
		set[e] = true
	}
	for _, v := range values {
		if !set[v] {
			t.Errorf("unexpected candidate %q in %v (want %v)", v, values, expected)
		}
	}
}

// --- tests ---

func TestComplete_ofEmptyCurrentWord_returnsAllSubcommands(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{""}, nil)

	// Then
	if result.Directive != completion.DirectiveDefault {
		t.Errorf("directive = %v, want DirectiveDefault", result.Directive)
	}
	containsAll(t, result.Candidates, "run", "stop", "list", "ps", "shutdown", "update", "help", "completion")
	notContains(t, result.Candidates, "__complete")
	notContains(t, result.Candidates, "__daemon")
}

func TestComplete_ofSubcommandPrefix_filtersSubcommands(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"r"}, nil)

	// Then
	exactValues(t, result.Candidates, "run")
}

func TestComplete_ofRunWithEmptyArg_returnsAllEnvNames(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"run", ""}, testEnvs)

	// Then
	if result.Directive != completion.DirectiveDefault {
		t.Errorf("directive = %v, want DirectiveDefault", result.Directive)
	}
	exactValues(t, result.Candidates, "frontend", "backend", "full-stack")
}

func TestComplete_ofRunWithEnvPrefix_filtersEnvNames(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"run", "fr"}, testEnvs)

	// Then
	exactValues(t, result.Candidates, "frontend")
}

func TestComplete_ofRunWithDashDash_returnsRunFlags(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"run", "--"}, testEnvs)

	// Then
	if result.Directive != completion.DirectiveDefault {
		t.Errorf("directive = %v, want DirectiveDefault", result.Directive)
	}
	containsAll(t, result.Candidates, "--timeout", "--config", "--config-overlay")
}

func TestComplete_ofConfigFlagFollowedByEmptyArg_returnsFileDirective(t *testing.T) {
	// Given / When — `--config` as the previous word triggers file completion.
	result := completion.Complete([]string{"--config", ""}, nil)

	// Then
	if result.Directive != completion.DirectiveFiles {
		t.Errorf("directive = %v, want DirectiveFiles", result.Directive)
	}
}

func TestComplete_ofRunConfigFlagFollowedByEmptyArg_returnsFileDirective(t *testing.T) {
	// Given / When — `env-starter run --config <TAB>`
	result := completion.Complete([]string{"run", "--config", ""}, nil)

	// Then
	if result.Directive != completion.DirectiveFiles {
		t.Errorf("directive = %v, want DirectiveFiles", result.Directive)
	}
}

func TestComplete_ofConfigOverlayFlagFollowedByEmptyArg_returnsFileDirective(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"--config-overlay", ""}, nil)

	// Then
	if result.Directive != completion.DirectiveFiles {
		t.Errorf("directive = %v, want DirectiveFiles", result.Directive)
	}
}

func TestComplete_ofUnknownSubcommand_returnsNoSubcommandCandidates(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"no-such-cmd", ""}, testEnvs)

	// Then
	if result.Directive != completion.DirectiveDefault {
		t.Errorf("directive = %v, want DirectiveDefault", result.Directive)
	}
	if len(result.Candidates) != 0 {
		t.Errorf("candidates = %v, want empty", result.Candidates)
	}
}

func TestComplete_ofDefaultPathWithVersionPrefix_returnsVersionFlag(t *testing.T) {
	// Given / When — no subcommand, completing a flag.
	result := completion.Complete([]string{"--v"}, nil)

	// Then
	exactValues(t, result.Candidates, "--version")
}

func TestComplete_ofStopWithEmptyArg_returnsAllEnvNames(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"stop", ""}, testEnvs)

	// Then
	exactValues(t, result.Candidates, "frontend", "backend", "full-stack")
}

func TestComplete_ofListWithDashDash_returnsListFlags(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"list", "--"}, nil)

	// Then
	containsAll(t, result.Candidates, "--config", "--config-overlay")
	notContains(t, result.Candidates, "--timeout")
	notContains(t, result.Candidates, "--version")
}

func TestComplete_ofRunWithConfigFlagValueThenEmptyArg_returnsEnvNames(t *testing.T) {
	// Given / When — `env-starter run --config /my/cfg.yaml <TAB>`: the flag
	// and its value are fully typed; we're now at the env-name positional slot.
	result := completion.Complete([]string{"run", "--config", "/my/cfg.yaml", ""}, testEnvs)

	// Then
	if result.Directive != completion.DirectiveDefault {
		t.Errorf("directive = %v, want DirectiveDefault", result.Directive)
	}
	exactValues(t, result.Candidates, "frontend", "backend", "full-stack")
}

func TestComplete_ofRunWithEnvAlreadyTyped_returnsNoMoreCandidates(t *testing.T) {
	// Given / When — `env-starter run frontend <TAB>`: positional already filled.
	result := completion.Complete([]string{"run", "frontend", ""}, testEnvs)

	// Then
	if len(result.Candidates) != 0 {
		t.Errorf("candidates = %v, want empty (positional already filled)", result.Candidates)
	}
}

func TestComplete_ofStopWithDashDash_returnsNoFlags(t *testing.T) {
	// Given / When — `stop` defines no flags.
	result := completion.Complete([]string{"stop", "--"}, nil)

	// Then
	if len(result.Candidates) != 0 {
		t.Errorf("candidates = %v, want empty (stop has no flags)", result.Candidates)
	}
}

func TestComplete_ofTimeoutFlagFollowedByEmptyArg_doesNotReturnFileDirective(t *testing.T) {
	// Given / When — `--timeout` takes a duration, not a file path.
	result := completion.Complete([]string{"run", "--timeout", ""}, testEnvs)

	// Then — should complete env names, not trigger file completion.
	if result.Directive != completion.DirectiveDefault {
		t.Errorf("directive = %v, want DirectiveDefault (--timeout is not a file flag)", result.Directive)
	}
}

func TestComplete_ofDescriptions_areIncludedInCandidates(t *testing.T) {
	// Given / When
	result := completion.Complete([]string{"run", ""}, testEnvs)

	// Then — at least one candidate should carry the tab-separated description.
	found := false
	for _, c := range result.Candidates {
		if c == "frontend\tFrontend stack" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("candidates = %v: expected 'frontend\\tFrontend stack'", result.Candidates)
	}
}
