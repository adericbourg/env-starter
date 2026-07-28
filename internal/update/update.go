// Package update checks for and applies new releases of env-starter from
// GitHub Releases. The release flow is:
//  1. [Client.Latest] fetches the latest release tag by following the
//     github.com/releases/latest redirect (no API key required).
//  2. [IsNewer] decides whether the caller's current version is behind.
//  3. [Client.Apply] downloads checksums.txt, finds the platform archive,
//     sha256-verifies it, and atomically replaces the binary.
//  4. [ReExec] re-launches the newly installed binary in place of the current process.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/adericbourg/env-starter/internal/httpsafe"
	"github.com/minio/selfupdate"
	"golang.org/x/mod/semver"
)

const githubRepo = "adericbourg/env-starter"

// noRedirectClient stops at the first redirect without following it.
// Used by Latest to capture the Location header from the 302 response.
var noRedirectClient = &http.Client{
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Release holds the version tag of a GitHub release.
type Release struct {
	TagName string
}

// Client performs update-related network operations.
// The zero value is usable; all unexported fields have safe defaults.
type Client struct {
	// httpGet is the HTTP GET seam for asset downloads in tests.
	// Defaults to a real net/http GET with the request context wired in.
	httpGet func(ctx context.Context, url string) (io.ReadCloser, error)

	// webBaseURL overrides the GitHub web base for tests. Defaults to "https://github.com".
	webBaseURL string

	// targetPath overrides the binary replacement path for tests.
	// Defaults to os.Executable().
	targetPath string

	// verifyKeyPEM overrides the embedded cosign public key in tests.
	// Defaults to cosignPublicKeyPEM.
	verifyKeyPEM string
}

func (c *Client) effectiveVerifyKey() string {
	if c.verifyKeyPEM != "" {
		return c.verifyKeyPEM
	}
	return cosignPublicKeyPEM
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

func (c *Client) effectiveWebBaseURL() string {
	if c.webBaseURL != "" {
		return c.webBaseURL
	}
	return "https://github.com"
}

func defaultHTTPGet(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "env-starter")
	// Same transport policy as source downloads: redirects must stay on https.
	resp, err := httpsafe.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP GET %s returned status %d: %s", url, resp.StatusCode, bytes.TrimSpace(body))
	}
	return resp.Body, nil
}

// tagPattern matches a plausible release tag (v1.2.3, v1.2.3-rc.1). The tag
// comes from a redirect Location header and is interpolated into download URL
// paths, so anything containing '/' or other URL-significant characters is
// rejected rather than allowed to reshape the checksums/archive URLs.
var tagPattern = regexp.MustCompile(`^v[0-9A-Za-z.+-]+$`)

// tagFromLocation extracts the release tag from a GitHub releases/tag redirect
// Location header value. It returns an error if the marker segment is absent,
// the tag is empty, or the tag does not look like a release version.
func tagFromLocation(location string) (string, error) {
	const marker = "/releases/tag/"
	idx := strings.LastIndex(location, marker)
	if idx < 0 {
		return "", fmt.Errorf("location %q does not contain %q", location, marker)
	}
	tag := strings.TrimSuffix(location[idx+len(marker):], "/")
	if tag == "" {
		return "", fmt.Errorf("empty tag in Location %q", location)
	}
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("tag %q in Location %q is not a valid release tag", tag, location)
	}
	return tag, nil
}

// Latest resolves the latest release tag by issuing a non-following GET to the
// github.com releases/latest page and parsing the redirect Location header.
// This avoids the api.github.com REST endpoint and its unauthenticated rate
// limit (60 requests/hour per IP).
func (c *Client) Latest(ctx context.Context) (Release, error) {
	url := fmt.Sprintf("%s/%s/releases/latest", c.effectiveWebBaseURL(), githubRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, fmt.Errorf("fetching latest release: %w", err)
	}
	req.Header.Set("User-Agent", "env-starter")

	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetching latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Release{}, fmt.Errorf("fetching latest release: unexpected status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	tag, err := tagFromLocation(resp.Header.Get("Location"))
	if err != nil {
		return Release{}, fmt.Errorf("fetching latest release: %w", err)
	}
	return Release{TagName: tag}, nil
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

// Apply downloads checksums.txt for the release, discovers the platform
// archive name and expected digest from it, then downloads, sha256-verifies,
// and atomically installs the binary in place of the currently running
// executable.
func (c *Client) Apply(ctx context.Context, rel Release) error {
	tmpDir, err := os.MkdirTemp("", "env-starter-update-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// 1. Download checksums.txt to discover the archive name and digest.
	checksumsURL := fmt.Sprintf("%s/%s/releases/download/%s/checksums.txt",
		c.effectiveWebBaseURL(), githubRepo, rel.TagName)
	checksumsPath := filepath.Join(tmpDir, "checksums.txt")
	if err := c.downloadTo(ctx, checksumsURL, checksumsPath); err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}

	checksumsContent, err := os.ReadFile(checksumsPath)
	if err != nil {
		return fmt.Errorf("reading checksums: %w", err)
	}

	// 1b. Authenticate checksums.txt before trusting any digest in it.
	if err := c.verifyChecksums(ctx, tmpDir, checksumsURL, checksumsContent); err != nil {
		return err
	}

	archiveName, expectedDigest, err := findArchive(checksumsContent, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return fmt.Errorf("finding platform archive: %w", err)
	}

	// 2. Download the archive and verify its sha256 while streaming to disk.
	archiveURL := fmt.Sprintf("%s/%s/releases/download/%s/%s",
		c.effectiveWebBaseURL(), githubRepo, rel.TagName, archiveName)
	archivePath := filepath.Join(tmpDir, archiveName)
	if err := c.downloadWithVerify(ctx, archiveURL, archivePath, expectedDigest); err != nil {
		return fmt.Errorf("downloading archive: %w", err)
	}

	// 3. Extract the binary from the archive into memory.
	binaryReader, err := extractBinary(archivePath, runtime.GOOS)
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

// verifyChecksums authenticates checksumsContent against its cosign Sigstore
// bundle (checksums.txt.sig in the same release). An embedded public key is
// required: a missing key, a missing signature, or an invalid signature are
// all hard failures (fail closed).
func (c *Client) verifyChecksums(ctx context.Context, tmpDir, checksumsURL string, checksumsContent []byte) error {
	pubKey := c.effectiveVerifyKey()
	if pubKey == "" {
		return errors.New("update: signature verification is not configured; embed the cosign public key in internal/update/verify.go before releasing")
	}

	sigURL := checksumsURL + ".sig"
	sigPath := filepath.Join(tmpDir, "checksums.txt.sig")
	if err := c.downloadTo(ctx, sigURL, sigPath); err != nil {
		return fmt.Errorf("downloading checksums signature: %w", err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("reading checksums signature: %w", err)
	}
	sigB64, err := extractBundleSignature(sig)
	if err != nil {
		return err
	}
	if err := verifyBlobSignature(pubKey, checksumsContent, sigB64); err != nil {
		return fmt.Errorf("authenticating release: %w", err)
	}
	return nil
}

// downloadTo fetches url and writes its body to destPath.
func (c *Client) downloadTo(ctx context.Context, url, destPath string) (retErr error) {
	body, err := c.effectiveHTTPGet()(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	defer func() {
		if cerr := f.Close(); retErr == nil {
			retErr = cerr
		}
	}()

	_, err = io.Copy(f, body)
	return err
}

// downloadWithVerify fetches url into destPath and verifies the sha256 digest
// by streaming through io.MultiWriter — no in-memory buffer of the full file.
func (c *Client) downloadWithVerify(ctx context.Context, url, destPath, expectedSHA256 string) (retErr error) {
	body, err := c.effectiveHTTPGet()(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	defer func() {
		if cerr := f.Close(); retErr == nil {
			retErr = cerr
		}
	}()

	h := sha256.New()
	// Write to the file and hash simultaneously without buffering the whole content.
	if _, err := io.Copy(io.MultiWriter(f, h), body); err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("writing archive: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		_ = os.Remove(destPath)
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, expectedSHA256)
	}
	return nil
}

// maxBinaryBytes caps how much of an archive entry is read into memory, guarding
// against a decompression bomb. It is a var so tests can lower it.
var maxBinaryBytes int64 = 512 << 20 // 512 MiB

// extractBinary extracts the env-starter binary from the release archive,
// selecting the archive format from the platform: a .zip on Windows (matching
// goreleaser's format_overrides) and a .tar.gz elsewhere.
func extractBinary(archivePath, goos string) (io.Reader, error) {
	binaryName := "env-starter"
	if goos == "windows" {
		binaryName = "env-starter.exe"
	}
	if goos == "windows" || strings.HasSuffix(archivePath, ".zip") {
		return extractBinaryFromZip(archivePath, binaryName)
	}
	return extractBinaryFromTarGz(archivePath, binaryName)
}

// readCapped copies r into a buffer, refusing to read more than maxBinaryBytes.
func readCapped(r io.Reader) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, maxBinaryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading binary from archive: %w", err)
	}
	if n > maxBinaryBytes {
		return nil, fmt.Errorf("archived binary exceeds the %d byte limit", maxBinaryBytes)
	}
	return &buf, nil
}

// extractBinaryFromTarGz opens the tar.gz at archivePath, finds the named
// binary entry, reads it (size-capped) into a buffer, and returns it.
func extractBinaryFromTarGz(archivePath, binaryName string) (io.Reader, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("opening archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }()

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
			return readCapped(tr)
		}
	}
}

// extractBinaryFromZip opens the zip at archivePath, finds the named binary
// entry, reads it (size-capped) into a buffer, and returns it.
func extractBinaryFromZip(archivePath, binaryName string) (io.Reader, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("opening zip archive: %w", err)
	}
	defer func() { _ = zr.Close() }()

	for _, file := range zr.File {
		if filepath.Base(file.Name) != binaryName {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("opening zip entry: %w", err)
		}
		buf, err := readCapped(rc)
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		return buf, nil
	}
	return nil, fmt.Errorf("binary %q not found in archive", binaryName)
}
