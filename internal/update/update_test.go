package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestTagFromLocation_ofUnsafeTag_returnsError(t *testing.T) {
	// The tag is interpolated into download URL paths, so anything that could
	// reshape those URLs (path traversal, query strings, missing v prefix) must
	// be rejected even though the cosign gate would catch a forged artifact.
	for _, loc := range []string{
		"https://github.com/adericbourg/env-starter/releases/tag/../../../evil",
		"https://github.com/adericbourg/env-starter/releases/tag/v1.0.0?x=1",
		"https://github.com/adericbourg/env-starter/releases/tag/1.0.0",
		"https://github.com/adericbourg/env-starter/releases/tag/v1.0.0%2F..",
	} {
		if _, err := tagFromLocation(loc); err == nil {
			t.Errorf("location %q: expected error, got nil", loc)
		}
	}
}

func TestTagFromLocation_ofPrereleaseTag_returnsTag(t *testing.T) {
	tag, err := tagFromLocation("https://github.com/adericbourg/env-starter/releases/tag/v2.0.0-rc.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "v2.0.0-rc.1" {
		t.Errorf("want v2.0.0-rc.1, got %q", tag)
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

// buildZip creates an in-memory zip archive containing a single file named
// binaryName with the given content.
func buildZip(t *testing.T, binaryName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(binaryName)
	if err != nil {
		t.Fatalf("creating zip entry: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("writing zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip writer: %v", err)
	}
	return buf.Bytes()
}

func TestExtractBinary_fromZip_returnsBinaryContent(t *testing.T) {
	// Given a Windows-style .zip archive on disk.
	content := []byte("windows binary bytes")
	archive := buildZip(t, "env-starter.exe", content)
	path := filepath.Join(t.TempDir(), "env-starter_1.0.0_windows_amd64.zip")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	// When
	r, err := extractBinary(path, "windows")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}

	// Then
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, content) {
		t.Errorf("extracted content = %q, want %q", got, content)
	}
}

func TestExtractBinary_whenEntryExceedsLimit_returnsError(t *testing.T) {
	// Given a tiny size cap and an archive entry larger than it.
	prev := maxBinaryBytes
	maxBinaryBytes = 4
	t.Cleanup(func() { maxBinaryBytes = prev })

	archive := buildTarGz(t, "env-starter", []byte("0123456789")) // 10 bytes > 4
	path := filepath.Join(t.TempDir(), "env-starter_1.0.0_linux_amd64.tar.gz")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatalf("writing archive: %v", err)
	}

	// When
	_, err := extractBinary(path, "linux")

	// Then
	if err == nil {
		t.Fatal("expected an error when the entry exceeds the size limit, got nil")
	}
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

	// Sign the checksums so signature verification succeeds.
	digest := sha256.Sum256(archiveBytes)
	checksums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName))
	pub, sig := signBlob(t, checksums)

	srv := serveRelease(t, tag, archiveName, archiveBytes, checksums, sig)

	// Redirect the binary replacement to a temp file.
	targetFile, err := os.CreateTemp(t.TempDir(), "env-starter-test-*")
	if err != nil {
		t.Fatalf("creating temp target: %v", err)
	}
	targetFile.Close()

	c := &Client{webBaseURL: srv.URL, targetPath: targetFile.Name(), verifyKeyPEM: pub}

	// When
	if err := c.Apply(context.Background(), Release{TagName: tag}); err != nil {
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
	// Given a signed checksums file whose digest does not match the archive.
	tag := "v1.2.3"
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archiveBytes := buildTarGz(t, "env-starter", []byte("fake binary content"))

	// Sign a checksums file with a deliberately wrong digest.
	wrongDigest := hex.EncodeToString(sha256.New().Sum(nil))
	checksums := []byte(fmt.Sprintf("%s  %s\n", wrongDigest, archiveName))
	pub, sig := signBlob(t, checksums)

	srv := serveRelease(t, tag, archiveName, archiveBytes, checksums, sig)
	c := &Client{webBaseURL: srv.URL, verifyKeyPEM: pub}

	// When
	err := c.Apply(context.Background(), Release{TagName: tag})

	// Then
	if err == nil {
		t.Error("expected error for checksum mismatch")
	}
}

// serveRelease starts a test server exposing checksums.txt (+ optional .sig)
// and the platform archive at the deterministic release download paths.
func serveRelease(t *testing.T, tag, archiveName string, archive, checksums, sig []byte) *httptest.Server {
	t.Helper()
	base := fmt.Sprintf("/adericbourg/env-starter/releases/download/%s/", tag)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base + "checksums.txt":
			w.Write(checksums) //nolint:errcheck
		case base + "checksums.txt.sig":
			if sig == nil {
				http.NotFound(w, r)
				return
			}
			w.Write(sig) //nolint:errcheck
		case base + archiveName:
			w.Write(archive) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestApply_withValidSignature_replacesBinary(t *testing.T) {
	// Given a signed checksums file and a configured public key.
	tag := "v1.2.3"
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	binaryName := "env-starter"
	if runtime.GOOS == "windows" {
		binaryName = "env-starter.exe"
	}
	binaryContent := []byte("signed binary content")
	archiveBytes := buildTarGz(t, binaryName, binaryContent)
	digest := sha256.Sum256(archiveBytes)
	checksums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName))
	pub, sig := signBlob(t, checksums)

	srv := serveRelease(t, tag, archiveName, archiveBytes, checksums, sig)

	targetFile, err := os.CreateTemp(t.TempDir(), "env-starter-test-*")
	if err != nil {
		t.Fatalf("creating temp target: %v", err)
	}
	targetFile.Close()

	c := &Client{webBaseURL: srv.URL, targetPath: targetFile.Name(), verifyKeyPEM: pub}

	// When
	if err := c.Apply(context.Background(), Release{TagName: tag}); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	// Then
	got, _ := os.ReadFile(targetFile.Name())
	if !bytes.Equal(got, binaryContent) {
		t.Errorf("target content = %q, want %q", got, binaryContent)
	}
}

func TestApply_withInvalidSignature_returnsError(t *testing.T) {
	// Given a checksums file whose signature does not match the configured key.
	tag := "v1.2.3"
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archiveBytes := buildTarGz(t, "env-starter", []byte("x"))
	digest := sha256.Sum256(archiveBytes)
	checksums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName))
	pub, _ := signBlob(t, checksums)
	_, wrongSig := signBlob(t, []byte("a different message"))

	srv := serveRelease(t, tag, archiveName, archiveBytes, checksums, wrongSig)
	c := &Client{webBaseURL: srv.URL, verifyKeyPEM: pub}

	// When
	err := c.Apply(context.Background(), Release{TagName: tag})

	// Then
	if err == nil {
		t.Fatal("expected Apply to fail with an invalid checksums signature, got nil")
	}
}

func TestApply_withNoKeyConfigured_returnsError(t *testing.T) {
	// Given a valid release served over HTTP but no verification key embedded.
	tag := "v1.2.3"
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archiveBytes := buildTarGz(t, "env-starter", []byte("binary"))
	digest := sha256.Sum256(archiveBytes)
	checksums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName))
	_, sig := signBlob(t, checksums)

	srv := serveRelease(t, tag, archiveName, archiveBytes, checksums, sig)
	c := &Client{webBaseURL: srv.URL} // verifyKeyPEM deliberately empty

	// When
	err := c.Apply(context.Background(), Release{TagName: tag})

	// Then: must refuse to apply the update when no key is configured.
	if err == nil {
		t.Fatal("expected Apply to fail closed when no verification key is configured, got nil")
	}
}

func TestApply_whenKeyConfiguredButSignatureMissing_returnsError(t *testing.T) {
	// Given a configured key but no signature published in the release.
	tag := "v1.2.3"
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archiveBytes := buildTarGz(t, "env-starter", []byte("x"))
	digest := sha256.Sum256(archiveBytes)
	checksums := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), archiveName))
	pub, _ := signBlob(t, checksums)

	srv := serveRelease(t, tag, archiveName, archiveBytes, checksums, nil) // no .sig served
	c := &Client{webBaseURL: srv.URL, verifyKeyPEM: pub}

	// When
	err := c.Apply(context.Background(), Release{TagName: tag})

	// Then
	if err == nil {
		t.Fatal("expected Apply to fail closed when the signature is missing, got nil")
	}
}
