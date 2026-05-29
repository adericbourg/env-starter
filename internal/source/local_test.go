package source

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLocal_Fetch_ofExistingDir_returnsPath(t *testing.T) {
	// Given
	dir := t.TempDir()
	l := Local{Path: dir}

	// When
	got, err := l.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestLocal_Fetch_ofMissingPath_returnsError(t *testing.T) {
	// Given
	l := Local{Path: filepath.Join(t.TempDir(), "nonexistent")}

	// When
	_, err := l.Fetch(context.Background())

	// Then
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLocal_Fetch_ofFilePath_returnsError(t *testing.T) {
	// Given
	dir := t.TempDir()
	// Create a file inside the temp dir.
	filePath := filepath.Join(dir, "afile.txt")
	if err := writeFile(filePath, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	l := Local{Path: filePath}

	// When
	_, err := l.Fetch(context.Background())

	// Then
	if err == nil {
		t.Fatal("expected error for file path, got nil")
	}
}
