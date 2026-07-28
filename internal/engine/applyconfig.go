package engine

import (
	"fmt"
	"sync"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/logbuf"
)

// ApplyConfig atomically swaps in a new configuration. It returns an error
// only when the new config is structurally invalid (e.g. a workflow
// references an undefined command), in which case nothing is mutated — the
// running engine and its config are untouched. Environments new to cfg are
// registered as EnvStopped; environments already tracked keep their current
// state.
//
// Reconciling running state against what actually changed — stopping removed
// environments, restarting changed commands and their dependents — is done by
// applyReloadPlan, added in a later change. This function only performs the
// swap.
func (e *Engine) ApplyConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("engine: nil config")
	}

	cmdOf, envsOf, envEnvOf, err := buildIndexes(cfg)
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.cfg = cfg
	e.cmdOf = cmdOf
	e.envsOf = envsOf
	e.envEnvOf = envEnvOf
	for _, env := range cfg.Environments {
		if _, ok := e.envState[env.Name]; !ok {
			e.envState[env.Name] = EnvStopped
		}
	}
	e.mu.Unlock()

	return nil
}

// restartCommandWithNewConfig restarts a live command with a changed
// definition, applying newCfg to every environment currently holding it.
// Unlike restartCommandInPlace/relaunch (which reuse c.runDir and skip
// setup), this performs a full managed start under the new definition —
// re-fetching the source and re-running setup — because the change that
// triggered this restart may be exactly a changed source or setup. Holders
// are untouched.
//
// Teardown runs against the OLD definition (c.cfg is only swapped for newCfg
// after teardownForRestart returns), so a changed teardown script never
// applies to the process it is meant to be cleaning up after.
func (e *Engine) restartCommandWithNewConfig(c *command, newCfg config.Command) {
	// Cancel the current owner goroutine, wait for any in-flight launch to
	// settle, then wait for the monitor goroutine itself to have returned.
	// The startDone wait alone is not enough: the monitor goroutine
	// (superviseService/superviseTask) can still be about to read c.cfg/
	// c.policy in a fresh loop iteration even after quit is closed, and
	// c.cfg/c.policy are about to be mutated below. monitorDone is nil when
	// this command never had a monitor (e.g. a task with no liveness check),
	// in which case there is nothing to wait for.
	e.mu.Lock()
	c.userStopped = true
	if c.quit != nil {
		c.quitOnce.Do(func() { close(c.quit) })
	}
	startDone := c.startDone
	monitorDone := c.monitorDone
	e.mu.Unlock()
	<-startDone
	if monitorDone != nil {
		<-monitorDone
	}

	// Reset cancellation and retry state for a fresh supervise cycle.
	e.mu.Lock()
	c.userStopped = false
	c.quit = nil
	c.quitOnce = sync.Once{}
	c.retries = 0
	e.mu.Unlock()

	e.setCmdState(newCfg.Name, CmdRestarting, nil)
	e.teardownForRestart(c) // reads c.cfg.Teardown — must run before the swap below.

	// Swap in the new definition and a fresh start barrier. Everything from
	// here on (fetchSource, setup, launch, readiness) reads the new cfg.
	e.mu.Lock()
	c.cfg = newCfg
	c.policy = resolveRestart(newCfg)
	c.startDone = make(chan struct{})
	e.mu.Unlock()

	e.setCmdState(newCfg.Name, CmdStarting, nil)

	logPath := e.logPath(newCfg.Name)
	file, err := logbuf.OpenFile(logPath)
	if err != nil {
		file = nil
	}
	e.mu.Lock()
	c.writer = logbuf.NewWriter(c.ring, file)
	e.mu.Unlock()

	// doStart fetches the source, runs setup, launches the process, then
	// hands off to handleTask/handleService, which set the terminal state
	// and start monitoring themselves.
	e.doStart(c)
	close(c.startDone)

	// Recompute state for every env sharing this command so their rollup
	// state reflects the restart outcome (mirrors restartCommandInPlace).
	e.recomputeEnvsFor(newCfg.Name)
}
