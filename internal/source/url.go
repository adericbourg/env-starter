package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/adericbourg/env-starter/internal/fsutil"
	"github.com/adericbourg/env-starter/internal/httpsafe"
)

// maxDownloadBytes caps the size of a downloaded file to avoid unbounded disk
// (and, previously, memory) use from a malicious or misbehaving server. It is a
// var so tests can lower it.
var maxDownloadBytes int64 = 2 << 30 // 2 GiB

// URL is a Source that downloads a file into the OS cache directory and returns
// that directory. An optional checksum (currently only sha256) is verified; a
// mismatch causes the bad file to be removed and an error to be returned.
type URL struct {
	URL           string
	ChecksumAlg   string
	ChecksumValue string

	// httpGet is the HTTP GET seam used by tests to inject a fake server.
	// Defaults to a real net/http GET.
	httpGet func(ctx context.Context, url string) (io.ReadCloser, error)

	// cacheBase overrides the base cache directory in tests.
	cacheBase string
}

func (u *URL) effectiveCacheBase() string {
	if u.cacheBase != "" {
		return u.cacheBase
	}
	return baseCacheDir
}

// cacheDir returns a stable cache subdirectory derived from the URL hash.
func (u *URL) cacheDir() (string, error) {
	// Hash the URL so the directory name is filesystem-safe and unique.
	h := sha256.Sum256([]byte(u.URL))
	subName := fmt.Sprintf("url-%s", hex.EncodeToString(h[:8]))

	base := u.effectiveCacheBase()
	if base == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine user cache dir: %w", err)
		}
		base = userCache
	}
	return filepath.Join(base, "env-starter", subName), nil
}

// validateHTTPSURL rejects any URL that is not served over https, so that a
// downloaded artifact cannot be served (or tampered with) over plaintext http.
func validateHTTPSURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("url source must use https, got scheme %q in %q", parsed.Scheme, raw)
	}
	return nil
}

func defaultHTTPGet(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpsafe.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP GET %s returned status %d", rawURL, resp.StatusCode)
	}
	return resp.Body, nil
}

// Fetch downloads the URL into the cache and returns the cache directory.
// Concurrent Fetch calls for the same URL serialize so that writes to the
// destination file do not interleave.
func (u *URL) Fetch(ctx context.Context) (string, error) {
	if err := validateHTTPSURL(u.URL); err != nil {
		return "", err
	}

	dir, err := u.cacheDir()
	if err != nil {
		return "", err
	}

	defer lockPath(dir)()

	// The cache dir names are predictable (hash of the URL), so a pre-existing
	// dir is only trusted when it is owned by us and private: a dir planted by
	// another user on a shared cache location would otherwise be reused — and
	// its contents executed — as if we had downloaded it.
	if err := fsutil.EnsureOwnerOnlyDir(filepath.Dir(dir)); err != nil {
		return "", fmt.Errorf("cache dir: %w", err)
	}
	if err := fsutil.EnsureOwnerOnlyDir(dir); err != nil {
		return "", fmt.Errorf("cannot create cache dir %s: %w", dir, err)
	}

	// Derive the local filename from the last path segment of the URL.
	filename := filepath.Base(u.URL)
	if filename == "." || filename == "/" {
		filename = "download"
	}
	destPath := filepath.Join(dir, filename)

	getter := u.httpGet
	if getter == nil {
		getter = defaultHTTPGet
	}

	body, err := getter(ctx, u.URL)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", u.URL, err)
	}
	defer func() { _ = body.Close() }()

	if err := u.writeAndVerify(body, destPath); err != nil {
		return "", err
	}

	return dir, nil
}

// writeAndVerify streams body to destPath and, when a checksum is configured,
// verifies it. The body is bounded by maxDownloadBytes and the digest is
// computed incrementally, so neither the file nor the hash is buffered in
// memory. If the size limit is exceeded or verification fails, the bad file is
// removed.
func (u *URL) writeAndVerify(body io.Reader, destPath string) (retErr error) {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file %s: %w", destPath, err)
	}
	defer func() {
		if cerr := f.Close(); retErr == nil {
			retErr = cerr
		}
	}()

	var writer io.Writer = f
	var hasher hash.Hash

	if u.ChecksumAlg != "" {
		if u.ChecksumAlg != "sha256" {
			return fmt.Errorf("unsupported checksum algorithm: %s", u.ChecksumAlg)
		}
		hasher = sha256.New()
		writer = io.MultiWriter(f, hasher)
	}

	// Read one byte past the cap so an exactly-at-limit file still succeeds while
	// anything larger is detected.
	limited := io.LimitReader(body, maxDownloadBytes+1)
	n, err := io.Copy(writer, limited)
	if err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("writing to %s: %w", destPath, err)
	}
	if n > maxDownloadBytes {
		_ = os.Remove(destPath)
		return fmt.Errorf("download from %s exceeds the %d byte limit", u.URL, maxDownloadBytes)
	}

	if hasher != nil {
		got := hex.EncodeToString(hasher.Sum(nil))
		if got != u.ChecksumValue {
			_ = os.Remove(destPath)
			return fmt.Errorf("checksum mismatch for %s: got %s, want %s", destPath, got, u.ChecksumValue)
		}
	}

	return nil
}
