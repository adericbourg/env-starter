package logbuf

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Ring tests
// ---------------------------------------------------------------------------

func TestNewRing_whenCapacityZero_defaultsToSaneValue(t *testing.T) {
	// Given
	r := NewRing(0)

	// When / Then
	if r.capacity != defaultCapacity {
		t.Fatalf("expected capacity %d, got %d", defaultCapacity, r.capacity)
	}
}

func TestNewRing_whenCapacityNegative_defaultsToSaneValue(t *testing.T) {
	// Given
	r := NewRing(-5)

	// When / Then
	if r.capacity != defaultCapacity {
		t.Fatalf("expected capacity %d, got %d", defaultCapacity, r.capacity)
	}
}

func TestAdd_whenBelowCapacity_retainsAllLines(t *testing.T) {
	// Given
	r := NewRing(5)

	// When
	r.Add("a")
	r.Add("b")
	r.Add("c")

	// Then
	lines := r.Lines()
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	for i, want := range []string{"a", "b", "c"} {
		if lines[i] != want {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], want)
		}
	}
}

func TestAdd_whenExceedsCapacity_evictsOldestPreservesOrder(t *testing.T) {
	// Given
	r := NewRing(3)

	// When — add 5 items into a ring of capacity 3
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.Add(s)
	}

	// Then — only the last 3 lines remain in order
	lines := r.Lines()
	want := []string{"c", "d", "e"}
	if len(lines) != len(want) {
		t.Fatalf("expected %d lines, got %d: %v", len(want), len(lines), lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], w)
		}
	}
}

func TestLines_returnsACopy_mutatingResultDoesNotAffectRing(t *testing.T) {
	// Given
	r := NewRing(5)
	r.Add("x")
	r.Add("y")

	// When
	copy1 := r.Lines()
	copy1[0] = "MUTATED"

	// Then
	copy2 := r.Lines()
	if copy2[0] != "x" {
		t.Errorf("ring was mutated; got %q, want %q", copy2[0], "x")
	}
}

func TestLen_returnsCurrentCount(t *testing.T) {
	// Given
	r := NewRing(10)

	// When
	r.Add("one")
	r.Add("two")

	// Then
	if r.Len() != 2 {
		t.Fatalf("expected Len 2, got %d", r.Len())
	}
}

// ---------------------------------------------------------------------------
// Writer tests
// ---------------------------------------------------------------------------

func TestWrite_multiLineInput_splitsIntoSeparateRingLines(t *testing.T) {
	// Given
	r := NewRing(10)
	w := NewWriter(r, nil)

	// When
	_, err := w.Write([]byte("line1\nline2\nline3\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Then
	lines := r.Lines()
	want := []string{"line1", "line2", "line3"}
	if len(lines) != len(want) {
		t.Fatalf("expected %d lines, got %d: %v", len(want), len(lines), lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("lines[%d] = %q, want %q", i, lines[i], w)
		}
	}
}

func TestWrite_partialLineThenClose_buffersAndFlushesTail(t *testing.T) {
	// Given
	r := NewRing(10)
	w := NewWriter(r, nil)

	// When — two writes that together form one line (no trailing newline)
	w.Write([]byte("hel"))
	w.Write([]byte("lo"))
	w.Close()

	// Then
	lines := r.Lines()
	if len(lines) != 1 || lines[0] != "hello" {
		t.Fatalf("expected [\"hello\"], got %v", lines)
	}
}

func TestWrite_partialLineAcrossWrites_combinedIntoOneLine(t *testing.T) {
	// Given
	r := NewRing(10)
	w := NewWriter(r, nil)

	// When — first write has no newline; second write completes it
	w.Write([]byte("first"))
	w.Write([]byte("_second\n"))
	w.Close()

	// Then
	lines := r.Lines()
	if len(lines) != 1 || lines[0] != "first_second" {
		t.Fatalf("expected [\"first_second\"], got %v", lines)
	}
}

func TestWrite_teesToFile_fileContainsRawBytes(t *testing.T) {
	// Given
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	r := NewRing(10)
	w := NewWriter(r, f)

	payload := "line1\nline2\n"

	// When
	_, err = w.Write([]byte(payload))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Then — file content matches raw bytes
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != payload {
		t.Errorf("file content = %q, want %q", string(got), payload)
	}
}

func TestOpenFileAppend_appendsWithoutTruncating(t *testing.T) {
	// Given an existing file with initial content written via OpenFile.
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")

	wc, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	wc.Write([]byte("first\n"))
	wc.Close()

	// When appending via OpenFileAppend.
	wa, err := OpenFileAppend(path)
	if err != nil {
		t.Fatalf("OpenFileAppend: %v", err)
	}
	wa.Write([]byte("second\n"))
	wa.Close()

	// Then both writes are present — initial content was not truncated.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	want := "first\nsecond\n"
	if string(got) != want {
		t.Errorf("file content = %q, want %q", string(got), want)
	}
}

func TestOpenFile_whenFileHasContent_truncatesIt(t *testing.T) {
	// Given — a file with pre-existing content.
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")

	wc, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile (first): %v", err)
	}
	wc.Write([]byte("previous run output\n"))
	wc.Close()

	// When — opening the same file again via OpenFile.
	wc2, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile (second): %v", err)
	}
	wc2.Close()

	// Then — the previous content is gone (file is empty).
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty file after re-open, got %q", string(got))
	}
}

func TestOpenFile_createsMissingDirsAndFile(t *testing.T) {
	// Given
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c.log")

	// When
	wc, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	wc.Write([]byte("hello"))
	wc.Close()

	// Then
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("file content = %q, want %q", string(got), "hello")
	}
}

// ---------------------------------------------------------------------------
// Concurrency tests
// ---------------------------------------------------------------------------

func TestAdd_concurrentAdds_doesNotRace(t *testing.T) {
	// Given
	r := NewRing(100)
	var wg sync.WaitGroup

	// When — 50 goroutines each add 20 lines concurrently
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				r.Add("goroutine line")
			}
		}()
	}
	wg.Wait()

	// Then — the ring holds exactly capacity (100) entries and no panics occurred
	if r.Len() != 100 {
		t.Errorf("expected Len 100 after concurrent adds, got %d", r.Len())
	}
}

func TestWrite_concurrentWrites_doesNotRace(t *testing.T) {
	// Given
	r := NewRing(200)
	w := NewWriter(r, nil)
	var wg sync.WaitGroup

	// When — 10 goroutines each write 10 complete lines concurrently
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				w.Write([]byte("concurrent line\n"))
			}
		}()
	}
	wg.Wait()
	w.Close()

	// Then — no race and the ring has a non-zero, bounded number of lines
	n := r.Len()
	if n == 0 || n > 200 {
		t.Errorf("unexpected Len after concurrent writes: %d", n)
	}
}
