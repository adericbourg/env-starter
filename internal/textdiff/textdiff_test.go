package textdiff

import "testing"

func TestUnified_ofIdenticalInputs_returnsEmpty(t *testing.T) {
	// Given
	text := []byte("a\nb\nc\n")

	// When
	got := Unified("old", "new", text, text)

	// Then
	if got != "" {
		t.Errorf("expected no diff, got:\n%s", got)
	}
}

func TestUnified_ofOneChangedLine_returnsHunkWithContext(t *testing.T) {
	// Given a change in the middle, with more than 3 lines of context on each side.
	old := []byte("1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	new := []byte("1\n2\n3\n4\nFIVE\n6\n7\n8\n9\n")

	// When
	got := Unified("a.yaml", "b.yaml", old, new)

	// Then only 3 lines of context are kept around the change.
	want := "--- a.yaml\n+++ b.yaml\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+FIVE\n 6\n 7\n 8\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnified_ofChangesAtBothEnds_returnsTwoHunks(t *testing.T) {
	// Given a line removed at the start and one added at the end, far apart.
	old := []byte("first\n1\n2\n3\n4\n5\n6\n7\n8\n")
	new := []byte("1\n2\n3\n4\n5\n6\n7\n8\nlast\n")

	// When
	got := Unified("old", "new", old, new)

	// Then
	want := "--- old\n+++ new\n" +
		"@@ -1,4 +1,3 @@\n-first\n 1\n 2\n 3\n" +
		"@@ -7,3 +6,4 @@\n 6\n 7\n 8\n+last\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnified_ofEmptyOld_returnsAllAdditions(t *testing.T) {
	// When
	got := Unified("old", "new", nil, []byte("x\ny"))

	// Then a missing trailing newline does not lose the last line.
	want := "--- old\n+++ new\n@@ -0,0 +1,2 @@\n+x\n+y\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
