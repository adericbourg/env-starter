// Package termsafe neutralizes terminal control sequences in untrusted text.
//
// Command output is rendered live in the TUI, so a malicious or compromised
// third-party process could emit escape sequences that manipulate the user's
// terminal — writing the clipboard (OSC 52), changing the window title, moving
// the cursor, or worse. Line strips every control and escape sequence except
// SGR (color/style) and the tab character, so colored output still renders
// while the dangerous sequences are removed.
package termsafe

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// Line returns s with all terminal control and escape sequences removed except
// SGR styling (ESC [ ... m) and horizontal tabs. It expects a single line with
// no trailing newline (the log writer splits on newlines before calling this).
func Line(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	state := parser.GroundState
	for len(s) > 0 {
		// A nil parser is fine: we classify sequences by prefix and terminator,
		// not by their collected parameters, so no params/data buffer is needed.
		seq, width, n, newState := ansi.DecodeSequence[string](s, state, nil)
		if n == 0 {
			// Defensive: never loop forever on an undecodable byte.
			break
		}
		if keepSequence(seq, width) {
			b.WriteString(seq)
		}
		s = s[n:]
		state = newState
	}
	return b.String()
}

// keepSequence decides whether a decoded chunk is safe to render. Printable
// text (width > 0), the tab character, and SGR sequences are kept; all other
// control characters and escape sequences are dropped.
func keepSequence(seq string, width int) bool {
	if width > 0 {
		return true
	}
	if seq == "\t" {
		return true
	}
	return isSGR(seq)
}

// isSGR reports whether seq is a CSI Select-Graphic-Rendition sequence, i.e. a
// color/style escape ending in 'm'.
func isSGR(seq string) bool {
	if len(seq) == 0 || !ansi.HasCsiPrefix(seq) {
		return false
	}
	return seq[len(seq)-1] == 'm'
}
