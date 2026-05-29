package update

import (
	"testing"
)

func TestLookupChecksum_ofValidFile_findsDigest(t *testing.T) {
	// Given
	content := []byte(
		"abc123  file-one.tar.gz\n" +
			"def456  file-two.tar.gz\n",
	)

	// When
	got, err := lookupChecksum(content, "file-two.tar.gz")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "def456" {
		t.Errorf("digest = %q, want def456", got)
	}
}

func TestLookupChecksum_ofMissingFile_returnsError(t *testing.T) {
	// Given
	content := []byte("abc123  file-one.tar.gz\n")

	// When
	_, err := lookupChecksum(content, "missing.tar.gz")

	// Then
	if err == nil {
		t.Error("expected error when filename is absent from checksums file")
	}
}

func TestLookupChecksum_ofEmptyFile_returnsError(t *testing.T) {
	// Given / When
	_, err := lookupChecksum([]byte{}, "anything.tar.gz")

	// Then
	if err == nil {
		t.Error("expected error for empty checksums file")
	}
}

func TestLookupChecksum_withMalformedLines_skipsAndContinues(t *testing.T) {
	// Given — first line has no double space separator; second is valid
	content := []byte(
		"no-separator-here\n" +
			"abc123  real-file.tar.gz\n",
	)

	// When
	got, err := lookupChecksum(content, "real-file.tar.gz")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "abc123" {
		t.Errorf("digest = %q, want abc123", got)
	}
}
