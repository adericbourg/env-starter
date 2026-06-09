// Package linkscan extracts http/https URLs from log lines, associating each
// URL with the command that produced it. It is intentionally free of I/O so
// that it can be used by both the TUI and CLI renderers without coupling.
package linkscan

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Link is a URL found in a command's logs, tagged with the command that emitted it.
type Link struct {
	Command string
	URL     string
}

// TaggedLine is a log line tagged with its originating command.
type TaggedLine struct {
	Command string
	Text    string
}

// urlPattern matches http and https URLs, excluding surrounding whitespace and
// common delimiter characters. Trailing punctuation is trimmed separately.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `]+`)

// trailingPunct is the set of characters that are commonly used as sentence or
// list terminators and should be stripped from the end of a matched URL.
const trailingPunct = `.,;:!?)]}'"` + "`"

// Extract returns the http/https URLs found in line, after first stripping any
// ANSI escape sequences. Trailing punctuation characters are trimmed so that
// URLs embedded in prose (e.g. "visit http://x.") yield clean targets.
// Results appear in the order they were found.
func Extract(line string) []string {
	clean := ansi.Strip(line)
	matches := urlPattern.FindAllString(clean, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, strings.TrimRight(m, trailingPunct))
	}
	return out
}

// Collect scans the provided tagged lines (oldest first) and returns the
// distinct URLs found, each paired with the command that first emitted it.
// Deduplication is by URL: only the first occurrence (and its command label)
// is kept. Results appear in the order they were first encountered.
func Collect(lines []TaggedLine) []Link {
	seen := make(map[string]struct{})
	var out []Link
	for _, tl := range lines {
		for _, u := range Extract(tl.Text) {
			if _, ok := seen[u]; ok {
				continue
			}
			seen[u] = struct{}{}
			out = append(out, Link{Command: tl.Command, URL: u})
		}
	}
	return out
}
