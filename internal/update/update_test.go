package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

// --- IsNewer ---

func TestIsNewer_ofDevBuild_returnsFalse(t *testing.T) {
	if IsNewer("dev", "v1.2.3") {
		t.Error("expected false for dev build")
	}
}

func TestIsNewer_ofEqualVersion_returnsFalse(t *testing.T) {
	if IsNewer("1.2.3", "v1.2.3") {
		t.Error("expected false for equal version")
	}
}

func TestIsNewer_ofOlderCurrent_returnsTrue(t *testing.T) {
	if !IsNewer("1.0.0", "v1.2.3") {
		t.Error("expected true when current is older")
	}
}

func TestIsNewer_ofNewerCurrent_returnsFalse(t *testing.T) {
	if IsNewer("2.0.0", "v1.2.3") {
		t.Error("expected false when current is newer than latest")
	}
}

func TestIsNewer_withMissingVPrefixOnLatest_normalizesCorrectly(t *testing.T) {
	if !IsNewer("1.0.0", "1.2.3") {
		t.Error("expected true even when latest tag lacks 'v' prefix")
	}
}

func TestIsNewer_withBothMissingVPrefix_normalizesCorrectly(t *testing.T) {
	if IsNewer("1.2.3", "1.2.3") {
		t.Error("expected false for equal versions without 'v' prefix")
	}
}

func TestIsNewer_ofUnparseableVersion_returnsFalse(t *testing.T) {
	if IsNewer("not-a-version", "v1.2.3") {
		t.Error("expected false for unparseable current version")
	}
}

func TestIsNewer_ofUnparseableLatest_returnsFalse(t *testing.T) {
	if IsNewer("1.0.0", "not-a-version") {
		t.Error("expected false for unparseable latest tag")
	}
}

// --- tagFromLocation ---

func TestTagFromLocation_ofFullURL_returnsTag(t *testing.T) {
	// Given / When
	tag, err := tagFromLocation("https://github.com/adericbourg/env-starter/releases/tag/v2.0.0")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v2.0.0" {
		t.Errorf("tag = %q, want v2.0.0", tag)
	}
}

func TestTagFromLocation_withTrailingSlash_returnsTag(t *testing.T) {
	// Given / When
	tag, err := tagFromLocation("https://github.com/adericbourg/env-starter/releases/tag/v2.0.0/")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v2.0.0" {
		t.Errorf("tag = %q, want v2.0.0", tag)
	}
}

func TestTagFromLocation_withMissingTagSegment_returnsError(t *testing.T) {
	// Given / When
	_, err := tagFromLocation("https://github.com/adericbourg/env-starter/releases/latest")

	// Then
	if err == nil {
		t.Error("expected error when /releases/tag/ segment is absent")
	}
}

func TestTagFromLocation_ofEmptyTag_returnsError(t *testing.T) {
	// Given / When
	_, err := tagFromLocation("https://github.com/adericbourg/env-starter/releases/tag/")

	// Then
	if err == nil {
		t.Error("expected error for empty tag after /releases/tag/")
	}
}

func TestTagFromLocation_ofEmptyLocation_returnsError(t *testing.T) {
	// Given / When
	_, err := tagFromLocation("")

	// Then
	if err == nil {
		t.Error("expected error for empty location")
	}
}

// --- Latest ---

func TestLatest_ofRedirectResponse_returnsTagName(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/adericbourg/env-starter/releases/tag/v2.0.0", http.StatusFound)
	}))
	defer srv.Close()

	c := &Client{webBaseURL: srv.URL}

	// When
	rel, err := c.Latest(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel.TagName != "v2.0.0" {
		t.Errorf("TagName = %q, want v2.0.0", rel.TagName)
	}
}

func TestLatest_ofNonRedirectResponse_returnsError(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "rate limited")
	}))
	defer srv.Close()

	c := &Client{webBaseURL: srv.URL}

	// When
	_, err := c.Latest(context.Background())

	// Then
	if err == nil {
		t.Error("expected error for 403 response")
	}
}

func TestLatest_withMissingLocationHeader_returnsError(t *testing.T) {
	// Given — return 302 without a Location header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := &Client{webBaseURL: srv.URL}

	// When
	_, err := c.Latest(context.Background())

	// Then
	if err == nil {
		t.Error("expected error for redirect without Location header")
	}
}

// --- Apply ---

// buildTarGz creates an in-memory tar.gz archive containing a single file
// named binaryName with the given content.
func buildTarGz(t *testing.T, binaryName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name: binaryName,
		Mode: 0o755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("writing tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("writing tar entry: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestApply_happyPath_replacesBinary(t *testing.T) {
	// The Apply function uses runtime.GOOS/GOARCH internally, so the archive
	// name must match the current platform.
	tag := "v1.2.3"
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	binaryName := "env-starter"
	if runtime.GOOS == "windows" {
		binaryName = "env-starter.exe"
	}

	binaryContent := []byte("fake binary content for testing")
	archiveBytes := buildTarGz(t, binaryName, binaryContent)

	// Compute the sha256 of the archive.
	digest := sha256.Sum256(archiveBytes)
	checksumsContent := fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName)

	// Serve both assets from a test server at the deterministic release download paths.
	checksumsPath := fmt.Sprintf("/adericbourg/env-starter/releases/download/%s/checksums.txt", tag)
	archivePath := fmt.Sprintf("/adericbourg/env-starter/releases/download/%s/%s", tag, archiveName)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case checksumsPath:
			fmt.Fprint(w, checksumsContent)
		case archivePath:
			w.Write(archiveBytes) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rel := Release{TagName: tag}

	// Redirect the binary replacement to a temp file.
	targetFile, err := os.CreateTemp(t.TempDir(), "env-starter-test-*")
	if err != nil {
		t.Fatalf("creating temp target: %v", err)
	}
	targetFile.Close()

	c := &Client{webBaseURL: srv.URL, targetPath: targetFile.Name()}

	// When
	if err := c.Apply(context.Background(), rel); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Then: target file should contain the binary content.
	got, err := os.ReadFile(targetFile.Name())
	if err != nil {
		t.Fatalf("reading target file: %v", err)
	}
	if !bytes.Equal(got, binaryContent) {
		t.Errorf("target content = %q, want %q", got, binaryContent)
	}
}

func TestApply_withChecksumMismatch_returnsError(t *testing.T) {
	tag := "v1.2.3"
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	binaryContent := []byte("fake binary content")
	archiveBytes := buildTarGz(t, "env-starter", binaryContent)

	// Deliberately wrong checksum.
	wrongChecksum := hex.EncodeToString(sha256.New().Sum(nil))
	checksumsContent := fmt.Sprintf("%s  %s\n", wrongChecksum, archiveName)

	checksumsPath := fmt.Sprintf("/adericbourg/env-starter/releases/download/%s/checksums.txt", tag)
	archivePath := fmt.Sprintf("/adericbourg/env-starter/releases/download/%s/%s", tag, archiveName)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case checksumsPath:
			fmt.Fprint(w, checksumsContent)
		case archivePath:
			w.Write(archiveBytes) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rel := Release{TagName: tag}
	c := &Client{webBaseURL: srv.URL}

	// When
	err := c.Apply(context.Background(), rel)

	// Then
	if err == nil {
		t.Error("expected error for checksum mismatch")
	}
}
