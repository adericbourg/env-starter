// Package update checks for and applies new releases of env-starter from
// GitHub Releases. The release flow is:
//  1. [Client.Latest] fetches the latest release metadata.
//  2. [IsNewer] decides whether the caller's current version is behind.
//  3. [Client.Apply] downloads, sha256-verifies, and atomically replaces the binary.
//  4. [ReExec] re-launches the newly installed binary in place of the current process.
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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/minio/selfupdate"
	"golang.org/x/mod/semver"
)

const githubRepo = "adericbourg/env-starter"

// Release holds the metadata returned by the GitHub Releases API for a single release.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Asset is a file attached to a GitHub release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Client performs update-related network operations.
// The zero value is usable; all unexported fields have safe defaults.
type Client struct {
	// httpGet is the HTTP GET seam for tests.
	// Defaults to a real net/http GET with the request context wired in.
	httpGet func(ctx context.Context, url string) (io.ReadCloser, error)

	// apiBaseURL overrides the GitHub API base for tests. Defaults to "https://api.github.com".
	apiBaseURL string

	// targetPath overrides the binary replacement path for tests.
	// Defaults to os.Executable().
	targetPath string
}

// New returns a Client configured for production use.
func New() *Client {
	return &Client{}
}

func (c *Client) effectiveHTTPGet() func(ctx context.Context, url string) (io.ReadCloser, error) {
	if c.httpGet != nil {
		return c.httpGet
	}
	return defaultHTTPGet
}

func (c *Client) effectiveAPIBaseURL() string {
	if c.apiBaseURL != "" {
		return c.apiBaseURL
	}
	return "https://api.github.com"
}

func defaultHTTPGet(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP GET %s returned status %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

// Latest fetches the latest release from GitHub Releases and returns its metadata.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.effectiveAPIBaseURL(), githubRepo)
	body, err := c.effectiveHTTPGet()(ctx, url)
	if err != nil {
		return Release{}, fmt.Errorf("fetching latest release: %w", err)
	}
	defer body.Close()

	var rel Release
	if err := json.NewDecoder(body).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("parsing release JSON: %w", err)
	}
	return rel, nil
}

// IsNewer reports whether latestTag names a version newer than current.
// Returns false for dev builds and for unparseable version strings.
func IsNewer(current, latestTag string) bool {
	if current == "dev" {
		return false
	}
	curr := ensureV(current)
	latest := ensureV(latestTag)
	if !semver.IsValid(curr) || !semver.IsValid(latest) {
		return false
	}
	return semver.Compare(curr, latest) < 0
}

func ensureV(v string) string {
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// Apply downloads, verifies, and atomically installs the binary from rel
// in place of the currently running executable.
func (c *Client) Apply(ctx context.Context, rel Release) error {
	archiveAsset, checksumsAsset, err := assetFor(rel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "env-starter-update-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Download checksums.txt.
	checksumsPath := filepath.Join(tmpDir, "checksums.txt")
	if err := c.downloadTo(ctx, checksumsAsset.BrowserDownloadURL, checksumsPath); err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}

	checksumsContent, err := os.ReadFile(checksumsPath)
	if err != nil {
		return fmt.Errorf("reading checksums: %w", err)
	}
	expectedDigest, err := lookupChecksum(checksumsContent, archiveAsset.Name)
	if err != nil {
		return fmt.Errorf("looking up checksum: %w", err)
	}

	// 2. Download the archive and verify its sha256 while streaming to disk.
	archivePath := filepath.Join(tmpDir, archiveAsset.Name)
	if err := c.downloadWithVerify(ctx, archiveAsset.BrowserDownloadURL, archivePath, expectedDigest); err != nil {
		return fmt.Errorf("downloading archive: %w", err)
	}

	// 3. Extract the binary from the archive into memory.
	binaryReader, err := extractBinaryFromTarGz(archivePath, runtime.GOOS)
	if err != nil {
		return fmt.Errorf("extracting binary: %w", err)
	}

	// 4. Atomically replace the running executable.
	opts := selfupdate.Options{}
	if c.targetPath != "" {
		opts.TargetPath = c.targetPath
	}
	if err := selfupdate.Apply(binaryReader, opts); err != nil {
		return fmt.Errorf("applying update: %w", err)
	}
	return nil
}

// assetFor returns the platform-matching archive and checksums.txt assets.
func assetFor(rel Release, goos, goarch string) (archive Asset, checksums Asset, err error) {
	suffix := fmt.Sprintf("_%s_%s.tar.gz", goos, goarch)
	if goos == "windows" {
		suffix = fmt.Sprintf("_%s_%s.zip", goos, goarch)
	}

	var foundArchive, foundChecksums bool
	for _, a := range rel.Assets {
		switch {
		case strings.HasSuffix(a.Name, suffix):
			archive = a
			foundArchive = true
		case a.Name == "checksums.txt":
			checksums = a
			foundChecksums = true
		}
	}
	if !foundArchive {
		return Asset{}, Asset{}, fmt.Errorf("no archive asset found for %s/%s (suffix %q)", goos, goarch, suffix)
	}
	if !foundChecksums {
		return Asset{}, Asset{}, fmt.Errorf("no checksums.txt asset in release %s", rel.TagName)
	}
	return archive, checksums, nil
}

// downloadTo fetches url and writes its body to destPath.
func (c *Client) downloadTo(ctx context.Context, url, destPath string) error {
	body, err := c.effectiveHTTPGet()(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	defer f.Close()

	_, err = io.Copy(f, body)
	return err
}

// downloadWithVerify fetches url into destPath and verifies the sha256 digest
// by streaming through io.MultiWriter — no in-memory buffer of the full file.
func (c *Client) downloadWithVerify(ctx context.Context, url, destPath, expectedSHA256 string) error {
	body, err := c.effectiveHTTPGet()(ctx, url)
	if err != nil {
		return err
	}
	defer body.Close()

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	defer f.Close()

	h := sha256.New()
	// Write to the file and hash simultaneously without buffering the whole content.
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("writing archive: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		os.Remove(destPath)
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, expectedSHA256)
	}
	return nil
}

// extractBinaryFromTarGz opens the tar.gz at archivePath, finds the binary
// entry, reads it into a buffer, and returns it as an io.Reader.
func extractBinaryFromTarGz(archivePath, goos string) (io.Reader, error) {
	binaryName := "env-starter"
	if goos == "windows" {
		binaryName = "env-starter.exe"
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("opening archive: %w", err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("opening gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("binary %q not found in archive", binaryName)
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if filepath.Base(hdr.Name) == binaryName {
			var buf bytes.Buffer
			if _, err := io.Copy(&buf, tr); err != nil {
				return nil, fmt.Errorf("reading binary from archive: %w", err)
			}
			return &buf, nil
		}
	}
}
