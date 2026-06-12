package termsafe

import "testing"

func TestLine_keepsPrintableTextAndTabs(t *testing.T) {
	// Given a plain line with a tab.
	in := "hello\tworld 123"

	// When / Then it is returned unchanged.
	if got := Line(in); got != in {
		t.Errorf("Line(%q) = %q, want unchanged", in, got)
	}
}

func TestLine_keepsSGRColorSequences(t *testing.T) {
	// Given text wrapped in an SGR color sequence.
	in := "\x1b[31mred\x1b[0m"

	// When / Then the SGR sequences are preserved.
	if got := Line(in); got != in {
		t.Errorf("Line(%q) = %q, want SGR preserved", in, got)
	}
}

func TestLine_stripsOSCClipboardSequence(t *testing.T) {
	// Given an OSC 52 clipboard-write sequence around visible text.
	in := "before\x1b]52;c;ZXZpbA==\x07after"

	// When
	got := Line(in)

	// Then the visible text survives but the OSC escape is gone.
	if got != "beforeafter" {
		t.Errorf("Line(%q) = %q, want %q", in, got, "beforeafter")
	}
}

func TestLine_stripsBELAndOtherControls(t *testing.T) {
	// Given a line with a BEL and a backspace.
	in := "ding\x07back\x08space"

	// When
	got := Line(in)

	// Then control characters are removed, text retained.
	if got != "dingbackspace" {
		t.Errorf("Line(%q) = %q, want %q", in, got, "dingbackspace")
	}
}

func TestLine_stripsTitleAndCursorSequences(t *testing.T) {
	// Given an OSC window-title set and a CSI cursor move.
	in := "\x1b]0;pwned\x07text\x1b[2J\x1b[Hmore"

	// When
	got := Line(in)

	// Then only the visible text remains.
	if got != "textmore" {
		t.Errorf("Line(%q) = %q, want %q", in, got, "textmore")
	}
}
