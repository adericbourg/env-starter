// Package redact masks secrets in command output before it leaves the machine
// (e.g. logs sent to an AI agent's model provider).
package redact

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

const mask = "***"

// minSecretLen is the shortest known value masked: shorter ones (ports,
// "true", "dev") would mangle unrelated output.
const minSecretLen = 8

var (
	keyBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	keyEnd   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)

	// ponytail: best-effort token formats; extend the list when a format slips through.
	// Each pattern's groups 1 and 2 (if any) are kept, the rest is masked.
	patterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(authorization:\s*(?:bearer|basic)\s+)\S+`),
		regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]+`),
		regexp.MustCompile(`(?i)((?:password|passwd|pwd|secret|token|api[_-]?key)\w*\s*[=:]\s*)[^\s"',;&]+`),
		regexp.MustCompile(`(://[^/\s:@]+:)[^/\s@]+(@)`),
		regexp.MustCompile(`()\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_\w{20,}|sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]{10,}|glpat-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{30,})`),
	}
)

// Lines returns a copy of lines with secrets masked as "***": the given known
// values (e.g. the command's env values), private key blocks, and common
// token formats.
func Lines(lines []string, secrets []string) []string {
	// Longest first, so a value containing another one is masked whole.
	secrets = slices.DeleteFunc(slices.Clone(secrets), func(s string) bool { return len(s) < minSecretLen })
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Compare(len(b), len(a)) })

	out := make([]string, len(lines))
	inKey := false
	for i, line := range lines {
		switch {
		case keyBegin.MatchString(line):
			inKey = true
		case keyEnd.MatchString(line):
			inKey = false
		case inKey:
			line = mask
		}
		for _, s := range secrets {
			line = strings.ReplaceAll(line, s, mask)
		}
		for _, p := range patterns {
			line = p.ReplaceAllString(line, "${1}"+mask+"${2}")
		}
		out[i] = line
	}
	return out
}
