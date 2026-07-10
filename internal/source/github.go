package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adericbourg/env-starter/internal/fsutil"
)

// runProcess executes name with args, routing stdout to out.
// stderr is captured separately and forwarded to out after the process exits,
// avoiding concurrent writes to the same writer from the separate goroutines
// exec.Cmd uses to drain stdout and stderr. On non-zero exit, the trimmed
// stderr is folded into the returned error so callers can surface the reason
// without ever writing to the real terminal.
func runProcess(ctx context.Context, out io.Writer, name string, args ...string) error {
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	// Forward captured stderr to the log writer regardless of exit status so
	// it appears in the command's log pane.
	if errBuf.Len() > 0 {
		_, _ = out.Write(errBuf.Bytes())
	}
	if err != nil {
		if tail := strings.TrimSpace(errBuf.String()); tail != "" {
			return fmt.Errorf("%s: %w", tail, err)
		}
		return err
	}
	return nil
}

// GitHub is a Source that clones or pulls a GitHub repository into the OS cache
// and returns the local directory (optionally under Subdir).
type GitHub struct {
	// Repo is "owner/name".
	Repo string
	// Branch to check out. Defaults to "main" when empty.
	Branch string
	// Method is one of "ssh", "https", "gh". When empty, ssh→gh→https are tried in order.
	Method string
	// Subdir is an optional path appended to the cache directory.
	Subdir string

	// Output receives git/gh stdout and stderr. Must never be set to os.Stdout
	// or os.Stderr while a TUI owns the terminal. nil means discard.
	Output io.Writer

	// runGit executes a git command. Defaults to g.gitRunner.
	// Signature: func(ctx context.Context, args ...string) error
	runGit func(ctx context.Context, args ...string) error

	// runGh executes a gh command. Defaults to g.ghRunner.
	runGh func(ctx context.Context, args ...string) error

	// cacheBase overrides the cache directory base for tests.
	cacheBase string
}

func (g *GitHub) effectiveCacheBase() string {
	if g.cacheBase != "" {
		return g.cacheBase
	}
	return baseCacheDir
}

func (g *GitHub) effectiveBranch() string {
	if g.Branch == "" {
		return "main"
	}
	return g.Branch
}

// cacheDir returns the directory where this repo will be cached.
func (g *GitHub) cacheDir() (string, error) {
	// Use "owner-name-branch" as a stable subdirectory name.
	parts := strings.SplitN(g.Repo, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid repo format %q: expected owner/name", g.Repo)
	}
	owner, name := parts[0], parts[1]
	branch := g.effectiveBranch()
	subName := fmt.Sprintf("github-%s-%s-%s", owner, name, branch)

	base := g.effectiveCacheBase()
	if base == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine user cache dir: %w", err)
		}
		base = userCache
	}
	return filepath.Join(base, "env-starter", subName), nil
}

// output returns the writer to use for git/gh output. Falls back to io.Discard
// when Output is nil so that no subprocess output reaches the real terminal.
func (g *GitHub) output() io.Writer {
	if g.Output == nil {
		return io.Discard
	}
	return g.Output
}

// gitRunner is the default git executor; it routes output through g.Output.
func (g *GitHub) gitRunner(ctx context.Context, args ...string) error {
	return runProcess(ctx, g.output(), "git", args...)
}

// ghRunner is the default gh executor; it routes output through g.Output.
func (g *GitHub) ghRunner(ctx context.Context, args ...string) error {
	return runProcess(ctx, g.output(), "gh", args...)
}

// sshCloneURL builds the SSH clone URL.
func sshCloneURL(repo string) string {
	return fmt.Sprintf("git@github.com:%s.git", repo)
}

// httpsCloneURL builds the HTTPS clone URL.
func httpsCloneURL(repo string) string {
	return fmt.Sprintf("https://github.com/%s.git", repo)
}

// Fetch clones or pulls the repository and returns the target directory.
// Concurrent Fetch calls for the same repo+ref serialize so that only the
// first caller clones; subsequent callers pull once the clone is done.
func (g *GitHub) Fetch(ctx context.Context) (string, error) {
	dir, err := g.cacheDir()
	if err != nil {
		return "", err
	}

	// The cache dir names are predictable (github-owner-name-branch), so a
	// pre-existing clone is only trusted when the cache root is owned by us and
	// private: a repo planted by another user on a shared cache location would
	// otherwise be pulled — and its contents executed — as if we had cloned it.
	if err := fsutil.EnsureOwnerOnlyDir(filepath.Dir(dir)); err != nil {
		return "", fmt.Errorf("cache dir: %w", err)
	}

	unlock, err := lockPath(dir)
	if err != nil {
		return "", err
	}
	defer unlock()

	runGit := g.runGit
	if runGit == nil {
		runGit = g.gitRunner
	}
	runGh := g.runGh
	if runGh == nil {
		runGh = g.ghRunner
	}

	info, statErr := os.Stat(dir)
	if statErr == nil && info.IsDir() {
		// Directory already exists — refresh.
		if err := runGit(ctx, "-C", dir, "pull", "--ff-only"); err != nil {
			return "", fmt.Errorf("git pull failed for %s: %w", g.Repo, err)
		}
	} else {
		// Need to clone.
		if err := g.clone(ctx, dir, runGit, runGh); err != nil {
			return "", err
		}
	}

	target := dir
	if g.Subdir != "" {
		target = filepath.Join(dir, g.Subdir)
		// Defense in depth: ensure the joined path stays within the clone dir, so
		// a "../.." subdir cannot escape the cache even if config validation is
		// bypassed by a direct caller.
		rel, err := filepath.Rel(dir, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("subdir %q escapes the source directory for %s", g.Subdir, g.Repo)
		}
		if _, err := os.Stat(target); err != nil {
			return "", fmt.Errorf("subdir %q does not exist in %s (branch %s): %w",
				g.Subdir, g.Repo, g.effectiveBranch(), err)
		}
	}
	return target, nil
}

// clone picks the right method (or tries them in order) and performs the clone.
func (g *GitHub) clone(ctx context.Context, dir string, runGit func(context.Context, ...string) error, runGh func(context.Context, ...string) error) error {
	branch := g.effectiveBranch()

	doSSH := func() error {
		return runGit(ctx, "clone", "--branch", branch, sshCloneURL(g.Repo), dir)
	}
	doGh := func() error {
		// Pass -- to separate gh flags from git clone flags, then --branch so the
		// cloned content matches the ref encoded in the cache directory name.
		return runGh(ctx, "repo", "clone", g.Repo, dir, "--", "--branch", branch)
	}
	doHTTPS := func() error {
		return runGit(ctx, "clone", "--branch", branch, httpsCloneURL(g.Repo), dir)
	}

	switch g.Method {
	case "ssh":
		return doSSH()
	case "https":
		return doHTTPS()
	case "gh":
		return doGh()
	default:
		// Try ssh → gh → https, stopping on the first success.
		if err := doSSH(); err == nil {
			return nil
		}
		if err := doGh(); err == nil {
			return nil
		}
		return doHTTPS()
	}
}
