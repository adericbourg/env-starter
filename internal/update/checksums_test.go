package update

import (
	"testing"
)

// --- findArchive ---

func TestFindArchive_forLinuxAmd64_returnsNameAndDigest(t *testing.T) {
	// Given
	content := []byte(
		"abc123  env-starter_1.2.3_linux_amd64.tar.gz\n" +
			"def456  env-starter_1.2.3_darwin_arm64.tar.gz\n" +
			"ghi789  checksums.txt\n",
	)

	// When
	name, digest, err := findArchive(content, "linux", "amd64")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "env-starter_1.2.3_linux_amd64.tar.gz" {
		t.Errorf("name = %q, want env-starter_1.2.3_linux_amd64.tar.gz", name)
	}
	if digest != "abc123" {
		t.Errorf("digest = %q, want abc123", digest)
	}
}

func TestFindArchive_forWindowsAmd64_findsZipEntry(t *testing.T) {
	// Given
	content := []byte(
		"abc123  env-starter_1.2.3_linux_amd64.tar.gz\n" +
			"def456  env-starter_1.2.3_windows_amd64.zip\n",
	)

	// When
	name, digest, err := findArchive(content, "windows", "amd64")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "env-starter_1.2.3_windows_amd64.zip" {
		t.Errorf("name = %q, want env-starter_1.2.3_windows_amd64.zip", name)
	}
	if digest != "def456" {
		t.Errorf("digest = %q, want def456", digest)
	}
}

func TestFindArchive_withUnknownPlatform_returnsError(t *testing.T) {
	// Given
	content := []byte("abc123  env-starter_1.2.3_linux_amd64.tar.gz\n")

	// When
	_, _, err := findArchive(content, "plan9", "mips")

	// Then
	if err == nil {
		t.Error("expected error when no archive matches the platform")
	}
}

func TestFindArchive_ofEmptyFile_returnsError(t *testing.T) {
	// Given / When
	_, _, err := findArchive([]byte{}, "linux", "amd64")

	// Then
	if err == nil {
		t.Error("expected error for empty checksums file")
	}
}

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
