package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

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

func defaultHTTPGet(_ context.Context, url string) (io.ReadCloser, error) {
	resp, err := http.Get(url) //nolint:noctx // intentional: real fallback ignores ctx for simplicity
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP GET %s returned status %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

// Fetch downloads the URL into the cache and returns the cache directory.
func (u *URL) Fetch(ctx context.Context) (string, error) {
	dir, err := u.cacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
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
	defer body.Close()

	if err := u.writeAndVerify(body, destPath); err != nil {
		return "", err
	}

	return dir, nil
}

// writeAndVerify streams body to destPath and, when a checksum is configured,
// verifies it. If verification fails the bad file is removed.
func (u *URL) writeAndVerify(body io.Reader, destPath string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating destination file %s: %w", destPath, err)
	}
	defer f.Close()

	var writer io.Writer = f
	var hasher *sha256HashWriter

	if u.ChecksumAlg != "" {
		if u.ChecksumAlg != "sha256" {
			return fmt.Errorf("unsupported checksum algorithm: %s", u.ChecksumAlg)
		}
		hasher = newSHA256HashWriter(f)
		writer = hasher
	}

	if _, err := io.Copy(writer, body); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("writing to %s: %w", destPath, err)
	}

	if hasher != nil {
		got := hasher.HexSum()
		if got != u.ChecksumValue {
			os.Remove(destPath)
			return fmt.Errorf("checksum mismatch for %s: got %s, want %s", destPath, got, u.ChecksumValue)
		}
	}

	return nil
}

// sha256HashWriter wraps an io.Writer and computes a SHA-256 digest as data is written.
type sha256HashWriter struct {
	w   io.Writer
	h   [sha256.Size]byte // reuse via hash.Hash
	sum []byte
}

func newSHA256HashWriter(w io.Writer) *sha256HashWriter {
	hw := &sha256HashWriter{w: w}
	return hw
}

// Write passes data through to the underlying writer while accumulating the hash.
func (hw *sha256HashWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	if n > 0 {
		hw.sum = append(hw.sum, p[:n]...)
	}
	return n, err
}

// HexSum returns the hex-encoded SHA-256 digest of all bytes written.
func (hw *sha256HashWriter) HexSum() string {
	digest := sha256.Sum256(hw.sum)
	return hex.EncodeToString(digest[:])
}
