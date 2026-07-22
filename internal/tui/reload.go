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
// for changes and can swap in a fresh engine on demand.
//
// All Controller delegation methods are guarded by a RWMutex so the engine
// pointer can be swapped safely while Bubble Tea's render goroutine reads
// through it.
type reloadController struct {
	mu  sync.RWMutex
	eng *engine.Engine
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

	// onSwap is called after a successful engine swap in Reload. It is used by
	// the daemon event hub to re-subscribe to the new engine's Events() channel.
	onSwap func()
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

// Reload tears down the running engine, re-loads config from disk, and builds
// a fresh engine. It returns an error without touching the running engine when
// loading or constructing the new engine fails, so the user is never left with
// nothing running due to a transient parse error.
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
	newEng, err := engine.New(fresh)
	if err != nil {
		return err
	}

	c.mu.Lock()
	old := c.eng   // capture old engine
	c.eng = newEng // swap in new engine first so clients use it immediately
	c.cfg = fresh
	c.dirty = false
	// Reset the baseline to the just-loaded file so a subsequent scan does not
	// immediately re-trigger.
	c.lastMod, c.lastSize, _ = statNewest(c.watchPaths)
	snap := c.onSwap // capture onSwap while holding lock
	c.mu.Unlock()

	old.Shutdown(ctx) // shutdown old engine AFTER swap (clients now use new engine)

	if snap != nil {
		snap() // notify hub to re-subscribe to new engine
	}

	return nil
}

// SetOnSwap sets a callback that will be invoked after a successful engine
// swap in Reload. This is used by the daemon event hub to re-subscribe to the
// new engine's Events() channel.
func (c *reloadController) SetOnSwap(fn func()) {
	c.mu.Lock()
	c.onSwap = fn
	c.mu.Unlock()
}

// ── Controller delegation ─────────────────────────────────────────────────────
// Every method reads through the live engine under RLock so they remain safe
// across an engine swap in Reload.

func (c *reloadController) Environments() []engine.EnvInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.Environments()
}

func (c *reloadController) WorkflowCommands(env string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.WorkflowCommands(env)
}

func (c *reloadController) EnvState(env string) engine.EnvState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.EnvState(env)
}

func (c *reloadController) CmdState(cmd string) engine.CmdState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.CmdState(cmd)
}

func (c *reloadController) CmdRetries(cmd string) (int, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.CmdRetries(cmd)
}

func (c *reloadController) IsUnmanaged(cmd string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.IsUnmanaged(cmd)
}

func (c *reloadController) Logs(cmd string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.Logs(cmd)
}

func (c *reloadController) LogPath(cmd string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.LogPath(cmd)
}

func (c *reloadController) ResolveEnv(envName, command string) []engine.ResolvedEnvVar {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.ResolveEnv(envName, command)
}

func (c *reloadController) StartEnvironment(env string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.StartEnvironment(env)
}

func (c *reloadController) StopEnvironment(env string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.StopEnvironment(env)
}

func (c *reloadController) RestartCommand(command string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.RestartCommand(command)
}

func (c *reloadController) Events() <-chan engine.Event {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.Events()
}

func (c *reloadController) StoppingCommands() []engine.StoppingCommand {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eng.StoppingCommands()
}

func (c *reloadController) Shutdown(ctx context.Context) {
	c.mu.RLock()
	e := c.eng
	c.mu.RUnlock()
	e.Shutdown(ctx)
}

// Detach is a no-op for reloadController: it owns the engine directly and has
// no client-side resources to release.
func (c *reloadController) Detach() {}
