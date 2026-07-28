package tui

import (
	"context"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/engine"
)

// reloadController wraps a live *engine.Engine behind the Controller interface
// and adds hot-reload support: it periodically checks the watched config files
// for changes and can apply a fresh config to the engine on demand.
//
// eng is immutable after construction — Reload calls eng.ApplyConfig, which
// mutates the engine in place (see internal/engine/applyconfig.go) rather
// than swapping in a new one, so delegation methods need no locking of their
// own; the engine's own mutex already makes it safe for concurrent use. mu
// guards only the small reload-bookkeeping fields below.
type reloadController struct {
	eng *engine.Engine

	mu sync.Mutex

	cfg *config.Config

	// load re-runs the full config resolution (base + optional overlay merge)
	// and returns a fresh parsed config. It is a closure so XDG/merge knowledge
	// stays in the caller.
	load func() (*config.Config, error)

	// watchPaths lists the config files to stat for change detection. Zero-length
	// entries are ignored.
	watchPaths []string

	// baseline for change detection; advanced whenever a stat delta is observed.
	lastMod  time.Time
	lastSize int64

	// dirty is set when the on-disk config differs semantically from the running
	// engine's config; cleared by a successful Reload or when the file is found to
	// be equal to the running config again.
	dirty bool

	// parseErr holds the last error from config.Load after a file-change was
	// detected. It is cleared when a subsequent scan loads the file successfully,
	// and preserved across scans while the file remains unmodified (so callers
	// see the error without re-reading the file on every tick).
	parseErr error
}

// NewReloadController returns a Controller that wraps eng and supports
// hot-reload from the given config files.
//
//   - cfg is the config eng was built from (used as the DeepEqual baseline).
//   - watchPaths lists files to stat for change detection (e.g. base path and
//     overlay path). Empty strings are skipped.
//   - load re-runs the full config resolution and returns a fresh *config.Config.
func NewReloadController(
	eng *engine.Engine,
	cfg *config.Config,
	watchPaths []string,
	load func() (*config.Config, error),
) *reloadController {
	c := &reloadController{
		eng:        eng,
		cfg:        cfg,
		load:       load,
		watchPaths: watchPaths,
	}
	// Seed the mtime baseline so the first scan does not fire a false positive.
	c.lastMod, c.lastSize, _ = statNewest(watchPaths)
	return c
}

// statNewest returns the latest mtime and the sum of sizes across all given
// non-empty paths. An error is returned only when every path fails to stat.
func statNewest(paths []string) (time.Time, int64, error) {
	var newest time.Time
	var totalSize int64
	var lastErr error
	var n int
	for _, p := range paths {
		if p == "" {
			continue
		}
		info, err := os.Stat(p)
		if err != nil {
			lastErr = err
			continue
		}
		n++
		totalSize += info.Size()
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if n == 0 {
		return time.Time{}, 0, lastErr
	}
	return newest, totalSize, nil
}

// ConfigChanged reports the current state of the on-disk config relative to
// the running engine. See the Controller interface for the full contract.
func (c *reloadController) ConfigChanged() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	mod, size, err := statNewest(c.watchPaths)
	if err != nil {
		// Cannot stat the file — return the last-known state without touching it.
		return c.dirty, c.parseErr
	}

	if mod.Equal(c.lastMod) && size == c.lastSize {
		// File unchanged on disk — return cached state; no re-read needed.
		return c.dirty, c.parseErr
	}

	// File has changed: advance the baseline so we don't re-load on every tick.
	c.lastMod, c.lastSize = mod, size

	fresh, loadErr := c.load()
	if loadErr != nil {
		// Parse error (mid-edit save, syntax error, …). Block reload until fixed.
		c.dirty = false
		c.parseErr = loadErr
		return false, loadErr
	}

	// Load succeeded: clear any previous parse error.
	c.parseErr = nil

	if reflect.DeepEqual(c.cfg, fresh) {
		// Semantically equal to running config (e.g. touch or no-op save).
		c.dirty = false
		return false, nil
	}

	c.dirty = true
	return true, nil
}

// Reload re-loads config from disk and applies it to the running engine via
// ApplyConfig, which reconciles running state selectively (see
// internal/engine/applyconfig.go) instead of tearing everything down. It
// returns an error without touching the running engine when loading or
// validating the new config fails, so the user is never left with nothing
// running due to a transient parse error. ApplyConfig itself returns quickly;
// any restarts it schedules run asynchronously and report progress through
// Events(), same as StartEnvironment.
func (c *reloadController) Reload(ctx context.Context) error {
	fresh, err := c.load()
	if err != nil {
		// File became invalid between the scan and the user pressing (c).
		// Update parseErr so the next scan reflects this immediately.
		c.mu.Lock()
		c.dirty = false
		c.parseErr = err
		c.mu.Unlock()
		return err
	}

	if err := c.eng.ApplyConfig(fresh); err != nil {
		return err
	}

	c.mu.Lock()
	c.cfg = fresh
	c.dirty = false
	// Reset the baseline to the just-loaded file so a subsequent scan does not
	// immediately re-trigger.
	c.lastMod, c.lastSize, _ = statNewest(c.watchPaths)
	c.mu.Unlock()

	return nil
}

// ── Controller delegation ─────────────────────────────────────────────────────
// eng is immutable after construction, so these delegate directly with no
// locking of their own — the engine is already safe for concurrent use.

func (c *reloadController) Environments() []engine.EnvInfo {
	return c.eng.Environments()
}

func (c *reloadController) WorkflowCommands(env string) []string {
	return c.eng.WorkflowCommands(env)
}

func (c *reloadController) EnvState(env string) engine.EnvState {
	return c.eng.EnvState(env)
}

func (c *reloadController) CmdState(cmd string) engine.CmdState {
	return c.eng.CmdState(cmd)
}

func (c *reloadController) CmdRetries(cmd string) (int, int) {
	return c.eng.CmdRetries(cmd)
}

func (c *reloadController) IsUnmanaged(cmd string) bool {
	return c.eng.IsUnmanaged(cmd)
}

func (c *reloadController) Logs(cmd string) []string {
	return c.eng.Logs(cmd)
}

func (c *reloadController) LogPath(cmd string) string {
	return c.eng.LogPath(cmd)
}

func (c *reloadController) ResolveEnv(envName, command string) []engine.ResolvedEnvVar {
	return c.eng.ResolveEnv(envName, command)
}

func (c *reloadController) StartEnvironment(env string) error {
	return c.eng.StartEnvironment(env)
}

func (c *reloadController) StopEnvironment(env string) error {
	return c.eng.StopEnvironment(env)
}

func (c *reloadController) RestartCommand(command string) error {
	return c.eng.RestartCommand(command)
}

func (c *reloadController) Events() <-chan engine.Event {
	return c.eng.Events()
}

func (c *reloadController) StoppingCommands() []engine.StoppingCommand {
	return c.eng.StoppingCommands()
}

func (c *reloadController) Shutdown(ctx context.Context) {
	c.eng.Shutdown(ctx)
}

// Detach is a no-op for reloadController: it owns the engine directly and has
// no client-side resources to release.
func (c *reloadController) Detach() {}
