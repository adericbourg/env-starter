package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/logbuf"
)

// StopEnvironment decrements the reference count of every command in the named
// environment's workflow. A command is actually torn down only when its
// reference count reaches zero. Returns an error if the environment is unknown.
func (e *Engine) StopEnvironment(env string) error {
	envCfg, ok := e.findEnv(env)
	if !ok {
		return fmt.Errorf("engine: unknown environment %q", env)
	}

	e.setEnvState(env, EnvStopping)
	go func() {
		e.releaseWorkflow(envCfg)
		e.setEnvState(env, EnvStopped)
	}()
	return nil
}

// releaseWorkflow removes envCfg's hold on every command in its workflow,
// tearing down commands that have no remaining holders.
func (e *Engine) releaseWorkflow(envCfg config.Environment) {
	for _, step := range envCfg.Workflow {
		e.releaseCommand(envCfg.Name, step.Command)
	}
}

// releaseCommand removes envName's hold on command name and tears the command
// down when no holders remain.
func (e *Engine) releaseCommand(envName, name string) {
	e.mu.Lock()
	c, ok := e.commands[name]
	if !ok {
		e.mu.Unlock()
		return
	}
	delete(c.holders, envName)
	shouldStop := len(c.holders) == 0
	e.mu.Unlock()

	if !shouldStop {
		// Still held by other environments: releasing envName's hold may have
		// shrunk the merged env for this shared command. Restart in place to
		// apply it; a no-op if the effective env did not actually change.
		e.maybeRestartForEnvChange(c, envName, "stopped")
		return
	}

	// Cancel any in-flight restart/monitor cycle so the supervisor goroutine
	// exits instead of attempting another restart after we stop the command.
	e.mu.Lock()
	c.userStopped = true
	if c.quit != nil {
		c.quitOnce.Do(func() { close(c.quit) })
	}
	e.mu.Unlock()

	// Signal intent to stop for active commands so the TUI can animate the
	// transition before the process actually exits. Stamp stopStartedAt here
	// (under the same lock) so the shutdown-screen countdown begins immediately.
	e.mu.Lock()
	active := c.state == CmdHealthy || c.state == CmdStarting || c.state == CmdRestarting
	if active {
		c.stopStartedAt = time.Now()
	}
	e.mu.Unlock()
	if active {
		e.setCmdState(name, CmdStopping, nil)
	}

	// Wait until the command has finished starting (or restarting) before
	// stopping it, so we do not race the launch. The stopped entry is kept in
	// the map (reporting CmdStopped) and recreated on a subsequent start.
	e.mu.Lock()
	startDone := c.startDone
	e.mu.Unlock()
	<-startDone

	e.stopCommand(c)
}

// stopCommand tears a single command down. For tasks, the teardown script (if
// any) runs after the task exits. For services, the teardown script (if any)
// runs first so it can stop the underlying process gracefully (e.g.
// `docker stop`), then the process is waited on and killed if still alive.
//
// A command still unmanaged at this point was never fetched, set up, or
// launched by env-starter (see checkUnmanaged) — there is no runDir for a
// teardown script to run in and no process of ours to kill, so it goes
// straight to CmdStopped.
func (e *Engine) stopCommand(c *command) {
	e.mu.Lock()
	unmanaged := c.unmanaged
	e.mu.Unlock()
	if unmanaged {
		e.mu.Lock()
		c.stopStartedAt = time.Time{}
		e.mu.Unlock()
		e.setCmdState(c.cfg.Name, CmdStopped, nil)
		return
	}

	switch c.cfg.Type {
	case "task":
		e.runTeardown(c)
		// A finished task stays "done" semantically, but for a stopped
		// environment we report it stopped only if it had not completed.
		e.mu.Lock()
		state := c.state
		// Clear the stop timer before publishing the terminal state so that
		// StoppingCommands never reports a command that has already finished.
		c.stopStartedAt = time.Time{}
		e.mu.Unlock()
		if state != CmdDone && state != CmdError {
			e.setCmdState(c.cfg.Name, CmdStopped, nil)
		}
	default:
		// Run teardown before signalling so it can stop a backing resource
		// (e.g. a named Docker container) before we kill the foreground client.
		// This also handles orphaned resources if the process already exited.
		e.runTeardown(c)
		e.mu.Lock()
		running := c.cmd != nil && c.state != CmdError
		e.mu.Unlock()
		if running {
			e.terminate(c)
		}
		// Clear the stop timer before publishing the terminal state so that
		// StoppingCommands never reports a command that has already finished.
		e.mu.Lock()
		c.stopStartedAt = time.Time{}
		e.mu.Unlock()
		e.setCmdState(c.cfg.Name, CmdStopped, nil)
	}
}

// terminate sends SIGINT to a service's process group, waits up to GracePeriod
// for it to exit, then SIGKILLs it. The supervisor is prevented from flipping
// the state to CmdError by the userStopped flag, which is set before terminate
// is called. For restart-path kills, use killAndWait instead.
func (e *Engine) terminate(c *command) {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	e.killAndWait(c)
}

// killAndWait sends SIGINT to the process group and waits up to GracePeriod
// for the process to exit, then SIGKILLs it. Unlike terminate it does NOT
// pre-mark the state, so it is safe to use from the restart path where the
// command is already in CmdRestarting.
func (e *Engine) killAndWait(c *command) {
	e.mu.Lock()
	cmd := c.cmd
	exited := c.exited
	e.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = interruptProcess(cmd)

	grace := e.GracePeriod
	if grace <= 0 {
		grace = defaultGracePeriod
	}
	select {
	case <-exited:
		return
	case <-time.After(grace):
		_ = killProcess(cmd)
		<-exited
	}
}

// runTeardown runs a command's Teardown script (if any) via sh -c in its run dir.
// It creates a fresh log writer over the persistent ring so that teardown output
// is captured regardless of whether the per-run writer has already been closed
// (which is always the case for tasks, whose process exits before teardown runs).
func (e *Engine) runTeardown(c *command) {
	if c.cfg.Teardown == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Open the log file in append mode so teardown output follows the run's output
	// in the on-disk log without truncating it. Failure is non-fatal: we still
	// capture into the ring for the TUI.
	file, err := logbuf.OpenFileAppend(e.logPath(c.cfg.Name))
	if err != nil {
		file = nil
	}
	w := logbuf.NewWriter(c.ring, file)
	defer func() { _ = w.Close() }()

	cmd := exec.CommandContext(ctx, "sh", "-c", c.cfg.Teardown)
	if c.runDir != "" {
		cmd.Dir = c.runDir
	}
	cmd.Env = append(os.Environ(), envSlice(e.teardownEnv(c))...)
	cmd.Stdout = w
	cmd.Stderr = w
	setSysProcAttr(cmd)
	_ = cmd.Run()
}

// resetForRetry prepares a previously-failed environment for a re-run.
// Commands in a healthy or terminal-success state (Healthy, Done, Starting,
// Restarting, Stopping) are left running untouched. Commands in a terminal-
// failure state (Error, Timeout, Stopped) are either released when this env is
// the sole holder — so acquireAndStart recreates them fresh — or restarted
// in-place when another env still holds them.
func (e *Engine) resetForRetry(envCfg config.Environment) {
	for _, step := range envCfg.Workflow {
		e.mu.Lock()
		c, ok := e.commands[step.Command]
		if !ok {
			// Never started (pending) — runEnvironment will start it fresh.
			e.mu.Unlock()
			continue
		}
		state := c.state
		holders := len(c.holders)
		e.mu.Unlock()

		switch state {
		case CmdHealthy, CmdDone, CmdStarting, CmdRestarting, CmdStopping:
			// Leave running: healthy, successfully-done, or in-progress commands
			// are re-acquired idempotently by runEnvironment.
		case CmdError, CmdTimeout, CmdStopped:
			if holders <= 1 {
				// Sole holder: release so acquireAndStart recreates the command fresh.
				e.releaseCommand(envCfg.Name, step.Command)
			} else {
				// Shared with another running env: restart in place so it recovers
				// for all holders without recreating the struct.
				e.restartInPlace(c)
			}
		}
	}
}

// restartInPlace recovers a down command that is still held by another
// environment, without recreating the command struct. The command's supervisor
// goroutine has already exited (it set CmdError), so this relaunches the process
// and re-establishes monitoring. Runs synchronously so startDone is closed
// before the subsequent acquireAndStart in runEnvironment observes the command.
func (e *Engine) restartInPlace(c *command) {
	c.restartMu.Lock()
	defer c.restartMu.Unlock()

	e.mu.Lock()
	c.retries = 0
	e.mu.Unlock()

	e.setCmdState(c.cfg.Name, CmdRestarting, nil)
	if e.relaunch(c) {
		e.setCmdState(c.cfg.Name, CmdHealthy, nil)
		e.startMonitor(c)
	}
	// Recompute state for every env sharing this command (including the retrying
	// env and any other holder) so their states reflect the restart outcome.
	e.recomputeEnvsFor(c.cfg.Name)
}

// RestartCommand restarts a single running command in place, preserving its
// environment holders. Unlike automatic restarts it is user-initiated and
// ignores the restart policy — it recycles the process even when auto-restart
// is disabled for this command. Returns an error if the command is unknown or
// currently has no holders (i.e. it is not running).
func (e *Engine) RestartCommand(name string) error {
	e.mu.Lock()
	c, ok := e.commands[name]
	running := ok && len(c.holders) > 0
	e.mu.Unlock()
	if !running {
		return fmt.Errorf("engine: command %q is not running", name)
	}

	// Publish CmdRestarting synchronously so a caller waiting on state/events
	// observes the transition immediately, before the still-current healthy
	// state could be mistaken for the restarted one.
	e.setCmdState(name, CmdRestarting, nil)
	go e.restartCommandInPlace(c)
	return nil
}

// restartCommandInPlace cancels the command's current supervisor/restart cycle
// (if any), then tears down and relaunches the process, re-establishing
// monitoring on success. Holders are never touched.
func (e *Engine) restartCommandInPlace(c *command) {
	// Cancel the current owner goroutine and wait for any in-flight launch to
	// settle, mirroring the barrier releaseCommand/Shutdown use to avoid racing
	// a launch already in progress. Also wait for the monitor goroutine itself
	// to have returned (mirrors restartCommandWithNewConfig): startDone alone
	// only tracks a single launch attempt, not the supervise loop that reads
	// c.quit again on every iteration — without this wait, that still-running
	// old goroutine and the startMonitor call below can end up with two
	// supervisor goroutines racing each other for the same command.
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

	// Serialize against relaunch/takeOverUnmanaged/restartCommandWithNewConfig's
	// own startDone swap-and-close cycle for this command (see command.restartMu):
	// the startDone wait above can unblock before the previous cycle's owner has
	// actually finished its post-relaunch bookkeeping, so this still waits its turn.
	c.restartMu.Lock()
	defer c.restartMu.Unlock()

	// Reset cancellation and retry state for a fresh supervise cycle. Safe
	// because every quitOnce.Do and this reset happen under e.mu, and the
	// previous owner goroutine only ever reads <-quit (never calls Do on it),
	// so it returns cleanly without interfering with the new cycle.
	e.mu.Lock()
	c.userStopped = false
	c.quit = nil
	c.quitOnce = sync.Once{}
	c.retries = 0
	e.mu.Unlock()

	e.setCmdState(c.cfg.Name, CmdRestarting, nil)
	e.teardownForRestart(c)
	if e.relaunch(c) {
		e.setCmdState(c.cfg.Name, CmdHealthy, nil)
		e.startMonitor(c)
	}
	// Recompute state for every env sharing this command so their rollup state
	// reflects the restart outcome (mirrors restartInPlace).
	e.recomputeEnvsFor(c.cfg.Name)
}

// Shutdown stops every currently-running command gracefully, respecting ctx as
// an overall deadline. All holders are cleared.
func (e *Engine) Shutdown(ctx context.Context) {
	e.mu.Lock()
	cmds := make([]*command, 0, len(e.commands))
	for _, c := range e.commands {
		cmds = append(cmds, c)
	}
	e.mu.Unlock()

	// Phase 1: stamp all stop times and cancel restart loops sequentially so
	// that StoppingCommands() returns the full set from the very first tick.
	type stopWork struct {
		c         *command
		startDone chan struct{}
	}
	works := make([]stopWork, 0, len(cmds))
	for _, c := range cmds {
		e.mu.Lock()
		c.userStopped = true
		if c.quit != nil {
			c.quitOnce.Do(func() { close(c.quit) })
		}
		active := c.state == CmdHealthy || c.state == CmdStarting || c.state == CmdRestarting
		if active {
			c.stopStartedAt = time.Now()
		}
		startDone := c.startDone
		e.mu.Unlock()
		if active {
			e.setCmdState(c.cfg.Name, CmdStopping, nil)
		}
		works = append(works, stopWork{c: c, startDone: startDone})
	}

	// Phase 2: stop all commands in parallel so the TUI shows all of them
	// simultaneously instead of one at a time.
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for _, w := range works {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-w.startDone
				e.stopCommand(w.c)
			}()
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		// Deadline hit: force-kill anything still alive.
		for _, c := range cmds {
			if c.cmd != nil && c.cmd.Process != nil {
				_ = killProcess(c.cmd)
			}
		}
		<-done
	}

	e.mu.Lock()
	e.commands = make(map[string]*command)
	for name := range e.envState {
		e.envState[name] = EnvStopped
	}
	e.mu.Unlock()
}
