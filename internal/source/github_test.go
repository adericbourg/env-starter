package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// callRecorder records the git/gh args passed to each invocation.
type callRecorder struct {
	calls [][]string
}

func (r *callRecorder) record(args ...string) {
	r.calls = append(r.calls, append([]string(nil), args...))
}

// successRunner always succeeds and records calls.
func (r *callRecorder) successRunner(_ context.Context, args ...string) error {
	r.record(args...)
	return nil
}

// failRunner always fails and records calls.
func (r *callRecorder) failRunner(_ context.Context, args ...string) error {
	r.record(args...)
	return errors.New("simulated failure")
}

func newGitHub(cacheBase, repo, branch, method, subdir string) *GitHub {
	return &GitHub{
		Repo:      repo,
		Branch:    branch,
		Method:    method,
		Subdir:    subdir,
		cacheBase: cacheBase,
	}
}

func TestGitHub_Fetch_whenCacheAbsent_clonesRepo(t *testing.T) {
	// Given
	cacheBase := t.TempDir()
	git := &callRecorder{}
	gh := &callRecorder{}

	g := newGitHub(cacheBase, "owner/repo", "main", "ssh", "")
	g.runGit = git.successRunner
	g.runGh = gh.successRunner

	// When
	_, err := g.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(git.calls) == 0 {
		t.Fatal("expected git clone call, got none")
	}
	firstCall := git.calls[0]
	if firstCall[0] != "clone" {
		t.Errorf("first git call = %v, want clone", firstCall)
	}
}

func TestGitHub_Fetch_whenCachePresent_pullsRepo(t *testing.T) {
	// Given
	cacheBase := t.TempDir()
	git := &callRecorder{}

	g := newGitHub(cacheBase, "owner/repo", "main", "ssh", "")

	// Pre-create the cache directory so Fetch thinks it is already cloned.
	dir, err := g.cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := mkdirAll(dir); err != nil {
		t.Fatal(err)
	}

	g.runGit = git.successRunner

	// When
	_, err = g.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(git.calls) == 0 {
		t.Fatal("expected git pull call, got none")
	}
	// The pull call contains "-C" as first arg.
	if git.calls[0][0] != "-C" {
		t.Errorf("expected pull (-C ...) call, got %v", git.calls[0])
	}
}

func TestGitHub_Fetch_methodFallback_sshFailsGhFailsHttpsSucceeds(t *testing.T) {
	// Given
	cacheBase := t.TempDir()

	var gitCalls []string
	var ghCalls int

	// ssh clone → fail; https clone → succeed
	fakeGit := func(_ context.Context, args ...string) error {
		gitCalls = append(gitCalls, args[0]) // record first arg (e.g. "clone" or "-C")
		// The ssh URL contains git@; https URL contains https://
		for _, a := range args {
			if len(a) > 4 && a[:4] == "git@" {
				return errors.New("ssh failed")
			}
		}
		return nil
	}
	fakeGh := func(_ context.Context, args ...string) error {
		ghCalls++
		return errors.New("gh failed")
	}

	g := newGitHub(cacheBase, "owner/repo", "main", "", "") // empty method → auto-fallback
	g.runGit = fakeGit
	g.runGh = fakeGh

	// When
	_, err := g.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ghCalls == 0 {
		t.Error("expected gh to be tried, but it was not called")
	}
	// Both git calls were made (ssh failed, https succeeded).
	if len(gitCalls) < 2 {
		t.Errorf("expected at least 2 git calls (ssh + https), got %d: %v", len(gitCalls), gitCalls)
	}
}

func TestGitHub_Fetch_withSubdir_appendsSubdir(t *testing.T) {
	// Given
	cacheBase := t.TempDir()
	git := &callRecorder{}

	g := newGitHub(cacheBase, "owner/repo", "main", "https", "scripts")
	g.runGit = git.successRunner

	// When
	dir, err := g.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cacheDir, _ := g.cacheDir()
	want := filepath.Join(cacheDir, "scripts")
	if dir != want {
		t.Errorf("dir = %q, want %q", dir, want)
	}
}

func TestGitHub_Fetch_branchDefaultsToMain(t *testing.T) {
	// Given
	cacheBase := t.TempDir()
	var capturedArgs []string
	fakeGit := func(_ context.Context, args ...string) error {
		capturedArgs = args
		return nil
	}

	g := newGitHub(cacheBase, "owner/repo", "", "https", "") // empty branch
	g.runGit = fakeGit

	// When
	_, err := g.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	foundMain := false
	for _, a := range capturedArgs {
		if a == "main" {
			foundMain = true
			break
		}
	}
	if !foundMain {
		t.Errorf("expected 'main' in clone args, got %v", capturedArgs)
	}
}

// mkdirAll is a test helper to create a directory hierarchy.
func mkdirAll(path string) error {
	return makeDir(path, 0o750)
}

func TestGitHub_Fetch_whenConcurrentSameRef_clonesOnce(t *testing.T) {
	// Given
	cacheBase := t.TempDir()

	var cloneCount, pullCount atomic.Int32
	fakeGit := func(_ context.Context, args ...string) error {
		if args[0] == "clone" {
			// Create the target directory (last arg) so subsequent Fetch calls see it.
			if err := os.MkdirAll(args[len(args)-1], 0o750); err != nil {
				return err
			}
			cloneCount.Add(1)
		} else {
			// pull: -C <dir> pull --ff-only
			pullCount.Add(1)
		}
		return nil
	}

	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)

	// When – launch n goroutines all fetching the same repo+branch concurrently.
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gh := newGitHub(cacheBase, "owner/repo", "main", "ssh", "")
			gh.runGit = fakeGit
			_, errs[i] = gh.Fetch(context.Background())
		}()
	}
	wg.Wait()

	// Then
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d got unexpected error: %v", i, err)
		}
	}
	if got := cloneCount.Load(); got != 1 {
		t.Errorf("clone count = %d, want 1 (shared clones should not race)", got)
	}
	if got := pullCount.Load(); got != int32(n-1) {
		t.Errorf("pull count = %d, want %d", got, n-1)
	}
}

func TestGitHub_Fetch_whenConcurrentDifferentRefs_clonesEach(t *testing.T) {
	// Given – three branches; each should get its own clone in parallel.
	cacheBase := t.TempDir()
	branches := []string{"main", "staging", "dev"}

	var cloneCount atomic.Int32
	fakeGit := func(_ context.Context, args ...string) error {
		if args[0] == "clone" {
			if err := os.MkdirAll(args[len(args)-1], 0o750); err != nil {
				return err
			}
			cloneCount.Add(1)
		}
		return nil
	}

	var wg sync.WaitGroup
	errs := make([]error, len(branches))

	// When
	for i, br := range branches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gh := newGitHub(cacheBase, "owner/repo", br, "ssh", "")
			gh.runGit = fakeGit
			_, errs[i] = gh.Fetch(context.Background())
		}()
	}
	wg.Wait()

	// Then
	for i, err := range errs {
		if err != nil {
			t.Errorf("branch %s: unexpected error: %v", branches[i], err)
		}
	}
	if got := cloneCount.Load(); got != int32(len(branches)) {
		t.Errorf("clone count = %d, want %d (one clone per distinct ref)", got, len(branches))
	}
}

func TestGitHub_Fetch_whenMethodGh_includesBranchArg(t *testing.T) {
	// Given
	cacheBase := t.TempDir()
	gh := &callRecorder{}

	g := newGitHub(cacheBase, "owner/repo", "feature-x", "gh", "")
	g.runGh = gh.successRunner

	// When
	_, err := g.Fetch(context.Background())

	// Then
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.calls) == 0 {
		t.Fatal("expected gh call, got none")
	}
	call := gh.calls[0]
	foundBranch := false
	for i, a := range call {
		if a == "--branch" && i+1 < len(call) && call[i+1] == "feature-x" {
			foundBranch = true
		}
	}
	if !foundBranch {
		t.Errorf("expected --branch feature-x in gh call args, got %v", call)
	}
}
