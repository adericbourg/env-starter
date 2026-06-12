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

func TestCheckHTTPSRedirect_rejectsDowngradeAndExcessiveHops(t *testing.T) {
	httpsReq, _ := http.NewRequest(http.MethodGet, "https://example.com/b", nil)
	httpReq, _ := http.NewRequest(http.MethodGet, "http://example.com/b", nil)

	// A single https->https hop is allowed.
	if err := checkHTTPSRedirect(httpsReq, []*http.Request{httpsReq}); err != nil {
		t.Errorf("https redirect should be allowed: %v", err)
	}
	// A downgrade to http is rejected.
	if err := checkHTTPSRedirect(httpReq, []*http.Request{httpsReq}); err == nil {
		t.Error("redirect to http should be rejected")
	}
	// Too many hops are rejected.
	via := make([]*http.Request, maxRedirects+1)
	for i := range via {
		via[i] = httpsReq
	}
	if err := checkHTTPSRedirect(httpsReq, via); err == nil {
		t.Error("excessive redirects should be rejected")
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
