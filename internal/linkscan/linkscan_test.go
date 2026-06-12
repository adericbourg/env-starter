package linkscan

import (
	"reflect"
	"testing"
)

// ── Extract ───────────────────────────────────────────────────────────────────

func TestExtract_ofPlainHttpUrl_returnsUrl(t *testing.T) {
	// Given
	line := "Connect at http://localhost:8080/login"

	// When
	got := Extract(line)

	// Then
	want := []string{"http://localhost:8080/login"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(%q) = %v, want %v", line, got, want)
	}
}

func TestExtract_stripsControlCharactersFromUrl(t *testing.T) {
	// Given a URL with an embedded BEL (an OSC terminator) and a DEL.
	line := "see https://example.com/\x07path\x7fend now"

	// When
	got := Extract(line)

	// Then the control characters are removed from the extracted URL.
	want := []string{"https://example.com/pathend"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(%q) = %v, want %v", line, got, want)
	}
}

func TestExtract_ofHttpsUrl_returnsUrl(t *testing.T) {
	// Given
	line := "Login at https://sso.example.com/auth?token=abc123"

	// When
	got := Extract(line)

	// Then
	want := []string{"https://sso.example.com/auth?token=abc123"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(%q) = %v, want %v", line, got, want)
	}
}

func TestExtract_ofLineWithAnsiCodes_stripsThemFirst(t *testing.T) {
	// Given — ANSI colour codes surrounding a URL
	line := "\033[32mStarted: http://localhost:9000\033[0m"

	// When
	got := Extract(line)

	// Then
	want := []string{"http://localhost:9000"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(%q) = %v, want %v", line, got, want)
	}
}

func TestExtract_ofUrlWithTrailingPunctuation_trimsIt(t *testing.T) {
	// Given — URL wrapped in parentheses and followed by a period
	line := "See (https://example.com/path)."

	// When
	got := Extract(line)

	// Then
	want := []string{"https://example.com/path"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(%q) = %v, want %v", line, got, want)
	}
}

func TestExtract_ofMultipleUrls_returnsAllInOrder(t *testing.T) {
	// Given
	line := "open http://a.example.com and https://b.example.com"

	// When
	got := Extract(line)

	// Then
	want := []string{"http://a.example.com", "https://b.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Extract(%q) = %v, want %v", line, got, want)
	}
}

func TestExtract_ofLineWithoutUrl_returnsEmpty(t *testing.T) {
	// Given
	line := "normal log line with no links"

	// When
	got := Extract(line)

	// Then
	if len(got) != 0 {
		t.Errorf("Extract(%q) = %v, want empty", line, got)
	}
}

func TestExtract_ofEmptyLine_returnsEmpty(t *testing.T) {
	// Given / When / Then
	if got := Extract(""); len(got) != 0 {
		t.Errorf("Extract(%q) = %v, want empty", "", got)
	}
}

// ── Collect ───────────────────────────────────────────────────────────────────

func TestCollect_ofLinesAcrossCommands_tagsEachLink(t *testing.T) {
	// Given
	lines := []TaggedLine{
		{Command: "db", Text: "ready at http://localhost:5432"},
		{Command: "proxy", Text: "listening https://localhost:8443"},
	}

	// When
	got := Collect(lines)

	// Then
	want := []Link{
		{Command: "db", URL: "http://localhost:5432"},
		{Command: "proxy", URL: "https://localhost:8443"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Collect() = %v, want %v", got, want)
	}
}

func TestCollect_whenSameUrlRepeats_dedupesByUrl(t *testing.T) {
	// Given — same URL from two different lines (first wins)
	lines := []TaggedLine{
		{Command: "db", Text: "ready http://localhost:5432"},
		{Command: "db", Text: "still up http://localhost:5432"},
	}

	// When
	got := Collect(lines)

	// Then — only one entry
	if len(got) != 1 {
		t.Fatalf("Collect() returned %d links, want 1: %v", len(got), got)
	}
	if got[0].URL != "http://localhost:5432" {
		t.Errorf("got URL %q, want http://localhost:5432", got[0].URL)
	}
}

func TestCollect_whenSameUrlFromDifferentCommands_firstCommandWins(t *testing.T) {
	// Given — same URL printed by two different commands
	lines := []TaggedLine{
		{Command: "alpha", Text: "http://shared.example.com"},
		{Command: "beta", Text: "http://shared.example.com"},
	}

	// When
	got := Collect(lines)

	// Then — deduplicated, first command label kept
	if len(got) != 1 {
		t.Fatalf("Collect() returned %d links, want 1", len(got))
	}
	if got[0].Command != "alpha" {
		t.Errorf("got command %q, want alpha", got[0].Command)
	}
}

func TestCollect_preservesAppearanceOrder(t *testing.T) {
	// Given
	lines := []TaggedLine{
		{Command: "svc", Text: "no link here"},
		{Command: "svc", Text: "https://first.example.com"},
		{Command: "svc", Text: "https://second.example.com"},
	}

	// When
	got := Collect(lines)

	// Then
	want := []Link{
		{Command: "svc", URL: "https://first.example.com"},
		{Command: "svc", URL: "https://second.example.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Collect() = %v, want %v", got, want)
	}
}

func TestCollect_ofEmptyLines_returnsEmpty(t *testing.T) {
	// Given / When
	got := Collect(nil)

	// Then
	if len(got) != 0 {
		t.Errorf("Collect(nil) = %v, want empty", got)
	}
}

func TestCollect_ofLinesWithNoUrls_returnsEmpty(t *testing.T) {
	// Given
	lines := []TaggedLine{
		{Command: "svc", Text: "starting up..."},
		{Command: "svc", Text: "ready"},
	}

	// When
	got := Collect(lines)

	// Then
	if len(got) != 0 {
		t.Errorf("Collect() = %v, want empty", got)
	}
}
