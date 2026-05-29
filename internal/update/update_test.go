package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// --- assetFor ---

func TestAssetFor_forLinuxAmd64_findsCorrectAssets(t *testing.T) {
	// Given
	rel := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "env-starter_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux_amd64.tar.gz"},
			{Name: "env-starter_1.2.3_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/darwin_arm64.tar.gz"},
			{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"},
		},
	}

	// When
	archive, checksums, err := assetFor(rel, "linux", "amd64")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archive.Name != "env-starter_1.2.3_linux_amd64.tar.gz" {
		t.Errorf("archive.Name = %q, want linux_amd64 archive", archive.Name)
	}
	if checksums.Name != "checksums.txt" {
		t.Errorf("checksums.Name = %q, want checksums.txt", checksums.Name)
	}
}

func TestAssetFor_forWindowsAmd64_findsZipAsset(t *testing.T) {
	// Given
	rel := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "env-starter_1.2.3_windows_amd64.zip", BrowserDownloadURL: "https://example.com/windows_amd64.zip"},
			{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"},
		},
	}

	// When
	archive, _, err := assetFor(rel, "windows", "amd64")

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archive.Name != "env-starter_1.2.3_windows_amd64.zip" {
		t.Errorf("archive.Name = %q, want windows_amd64.zip asset", archive.Name)
	}
}

func TestAssetFor_withMissingArchive_returnsError(t *testing.T) {
	// Given
	rel := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "checksums.txt"},
		},
	}

	// When
	_, _, err := assetFor(rel, "linux", "amd64")

	// Then
	if err == nil {
		t.Error("expected error when archive asset is missing")
	}
}

func TestAssetFor_withMissingChecksums_returnsError(t *testing.T) {
	// Given
	rel := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "env-starter_1.2.3_linux_amd64.tar.gz"},
		},
	}

	// When
	_, _, err := assetFor(rel, "linux", "amd64")

	// Then
	if err == nil {
		t.Error("expected error when checksums.txt is missing")
	}
}

// --- Latest ---

func TestLatest_ofValidResponse_returnsRelease(t *testing.T) {
	// Given
	payload := map[string]any{
		"tag_name": "v2.0.0",
		"assets": []map[string]any{
			{"name": "env-starter_2.0.0_linux_amd64.tar.gz", "browser_download_url": "https://example.com/archive.tar.gz"},
			{"name": "checksums.txt", "browser_download_url": "https://example.com/checksums.txt"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload) //nolint:errcheck
	}))
	defer srv.Close()

	c := &Client{apiBaseURL: srv.URL}

	// When
	rel, err := c.Latest(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel.TagName != "v2.0.0" {
		t.Errorf("TagName = %q, want v2.0.0", rel.TagName)
	}
	if len(rel.Assets) != 2 {
		t.Errorf("len(Assets) = %d, want 2", len(rel.Assets))
	}
}

func TestLatest_ofHTTPError_returnsError(t *testing.T) {
	// Given
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{apiBaseURL: srv.URL}

	// When
	_, err := c.Latest(context.Background())

	// Then
	if err == nil {
		t.Error("expected error for 404 response")
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

	// Serve both assets from a test server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			fmt.Fprint(w, checksumsContent)
		case "/" + archiveName:
			w.Write(archiveBytes) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/" + archiveName},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}

	// Redirect the binary replacement to a temp file.
	targetFile, err := os.CreateTemp(t.TempDir(), "env-starter-test-*")
	if err != nil {
		t.Fatalf("creating temp target: %v", err)
	}
	targetFile.Close()

	c := &Client{targetPath: targetFile.Name()}

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
	archiveName := fmt.Sprintf("env-starter_1.2.3_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	binaryContent := []byte("fake binary content")
	archiveBytes := buildTarGz(t, "env-starter", binaryContent)

	// Deliberately wrong checksum.
	wrongChecksum := hex.EncodeToString(sha256.New().Sum(nil))
	checksumsContent := fmt.Sprintf("%s  %s\n", wrongChecksum, archiveName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			fmt.Fprint(w, checksumsContent)
		case "/" + archiveName:
			w.Write(archiveBytes) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rel := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/" + archiveName},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		},
	}

	c := &Client{}

	// When
	err := c.Apply(context.Background(), rel)

	// Then
	if err == nil {
		t.Error("expected error for checksum mismatch")
	}
}
