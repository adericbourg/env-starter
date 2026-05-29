package source

import (
	"context"
	"errors"
	"path/filepath"
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
