package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeHTTPGet returns a getter that serves the provided body bytes.
func fakeHTTPGet(data []byte) func(context.Context, string) (io.ReadCloser, error) {
	return func(_ context.Context, _ string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(data))), nil
	}
}

func TestURL_Fetch_downloadsFileIntoCache(t *testing.T) {
	// Given an HTTPS test server serving a file.
	content := []byte("hello world")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content) //nolint:errcheck
	}))
	defer srv.Close()

	cacheBase := t.TempDir()
	u := &URL{
		URL:       srv.URL + "/file.txt",
		cacheBase: cacheBase,
		// Use the test server's client so its self-signed cert is trusted; this
		// still exercises the real download/redirect path in defaultHTTPGet.
		httpGet: clientHTTPGet(srv.Client()),
	}

	// When
	dir, err := u.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dest := filepath.Join(dir, "file.txt")
	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("cannot read downloaded file: %v", readErr)
	}
	if string(got) != string(content) {
		t.Errorf("file content = %q, want %q", got, content)
	}
}

func TestURL_Fetch_tightensPreexistingLooseCacheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on windows")
	}
	// Given a cache root that already exists world-traversable. Cache subdir
	// names are predictable, so the root must be private before any pre-existing
	// content is reused.
	cacheBase := t.TempDir()
	root := filepath.Join(cacheBase, "env-starter")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	u := &URL{
		URL:       "https://example.com/file.txt",
		cacheBase: cacheBase,
		httpGet:   fakeHTTPGet([]byte("data")),
	}

	// When
	if _, err := u.Fetch(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Then
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("cache root: want tightened to 0700, got %o", perm)
	}
}

// clientHTTPGet adapts an *http.Client into the URL.httpGet seam.
func clientHTTPGet(c *http.Client) func(context.Context, string) (io.ReadCloser, error) {
	return func(ctx context.Context, rawURL string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		return resp.Body, nil
	}
}

func TestURL_Fetch_whenSchemeNotHTTPS_returnsError(t *testing.T) {
	// Given a plaintext http:// URL, which must be rejected before any request.
	u := &URL{
		URL:       "http://example.com/archive.tar.gz",
		cacheBase: t.TempDir(),
		httpGet: func(context.Context, string) (io.ReadCloser, error) {
			t.Fatal("getter must not be called for a non-https URL")
			return nil, nil
		},
	}

	// When
	_, err := u.Fetch(context.Background())

	// Then
	if err == nil {
		t.Fatal("expected an error for a non-https URL, got nil")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error %q should mention the https requirement", err)
	}
}

func TestURL_Fetch_withMatchingChecksum_succeeds(t *testing.T) {
	// Given
	content := []byte("checksum test content")
	digest := sha256.Sum256(content)
	expected := hex.EncodeToString(digest[:])

	u := &URL{
		URL:           "https://example.com/archive.tar.gz",
		ChecksumAlg:   "sha256",
		ChecksumValue: expected,
		cacheBase:     t.TempDir(),
		httpGet:       fakeHTTPGet(content),
	}

	// When
	_, err := u.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestURL_Fetch_withMismatchedChecksum_returnsErrorAndRemovesFile(t *testing.T) {
	// Given
	content := []byte("some content")
	cacheBase := t.TempDir()

	u := &URL{
		URL:           "https://example.com/archive.tar.gz",
		ChecksumAlg:   "sha256",
		ChecksumValue: "0000000000000000000000000000000000000000000000000000000000000000",
		cacheBase:     cacheBase,
		httpGet:       fakeHTTPGet(content),
	}

	// When
	_, err := u.Fetch(context.Background())

	// Then
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}

	// The bad file must not be left on disk.
	dir, _ := u.cacheDir()
	dest := filepath.Join(dir, "archive.tar.gz")
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("bad file was not removed: %s", dest)
	}
}

func TestURL_Fetch_whenConcurrentSameURL_noError(t *testing.T) {
	// Given – multiple goroutines downloading the same URL concurrently should not
	// collide on the destination file.
	content := []byte("concurrent content")
	getter := func(_ context.Context, _ string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(content))), nil
	}

	const n = 5
	cacheBase := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, n)

	// When
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			u := &URL{
				URL:       "https://example.com/file.tar.gz",
				cacheBase: cacheBase,
				httpGet:   getter,
			}
			_, errs[i] = u.Fetch(context.Background())
		}()
	}
	wg.Wait()

	// Then
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d got unexpected error: %v", i, err)
		}
	}
}

func TestURL_Fetch_whenBodyExceedsLimit_returnsErrorAndRemovesFile(t *testing.T) {
	// Given a download cap smaller than the served body.
	prev := maxDownloadBytes
	maxDownloadBytes = 8
	t.Cleanup(func() { maxDownloadBytes = prev })

	cacheBase := t.TempDir()
	u := &URL{
		URL:       "https://example.com/big.tar.gz",
		cacheBase: cacheBase,
		httpGet:   fakeHTTPGet([]byte("0123456789abcdef")), // 16 bytes > 8
	}

	// When
	_, err := u.Fetch(context.Background())

	// Then
	if err == nil {
		t.Fatal("expected an error when the body exceeds the size limit, got nil")
	}
	dir, _ := u.cacheDir()
	dest := filepath.Join(dir, "big.tar.gz")
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("oversize file was not removed: %s", dest)
	}
}

func TestURL_Fetch_withNoChecksum_downloadsSuccessfully(t *testing.T) {
	// Given
	content := []byte("no checksum needed")
	u := &URL{
		URL:       "https://example.com/plain.tar.gz",
		cacheBase: t.TempDir(),
		httpGet:   fakeHTTPGet(content),
	}

	// When
	dir, err := u.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dest := filepath.Join(dir, "plain.tar.gz")
	got, readErr := os.ReadFile(dest)
	if readErr != nil {
		t.Fatalf("cannot read downloaded file: %v", readErr)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}
