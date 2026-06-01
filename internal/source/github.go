package source

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

	// runGit executes a git command. Defaults to the real git binary.
	// Signature: func(ctx context.Context, args ...string) error
	runGit func(ctx context.Context, args ...string) error

	// runGh executes a gh command. Defaults to the real gh binary.
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

// gitRunner is the default git executor.
func gitRunner(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ghRunner is the default gh executor.
func ghRunner(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

	defer lockPath(dir)()

	runGit := g.runGit
	if runGit == nil {
		runGit = gitRunner
	}
	runGh := g.runGh
	if runGh == nil {
		runGh = ghRunner
	}

	info, statErr := os.Stat(dir)
	if statErr == nil && info.IsDir() {
		// Directory already exists — refresh.
		if err := runGit(ctx, "-C", dir, "pull", "--ff-only"); err != nil {
			return "", fmt.Errorf("git pull failed for %s: %w", g.Repo, err)
		}
	} else {
		// Need to clone.
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return "", fmt.Errorf("cannot create cache parent dir: %w", err)
		}
		if err := g.clone(ctx, dir, runGit, runGh); err != nil {
			return "", err
		}
	}

	target := dir
	if g.Subdir != "" {
		target = filepath.Join(dir, g.Subdir)
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
