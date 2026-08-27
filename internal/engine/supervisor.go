package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/logbuf"
	"github.com/adericbourg/env-starter/internal/probe"
	"github.com/adericbourg/env-starter/internal/source"
)

// StartEnvironment starts the named environment asynchronously. It returns an
// error only if the environment is unknown; startup progress is reported via
// Events() and the state accessors.
//
// If the environment is currently in a failed state (EnvError or EnvDegraded),
// it is first reset: its workflow commands are released so that errored commands
// — whose refcount was never decremented on failure — are recreated fresh.
// Commands still shared with other running environments are not torn down.
func (e *Engine) StartEnvironment(env string) error {
	envCfg, ok := e.findEnv(env)
	if !ok {
		return fmt.Errorf("engine: unknown environment %q", env)
	}

	prev := e.EnvState(env)
	e.setEnvState(env, EnvStarting)

	go func() {
		// A previously failed env may hold commands in terminal states. Selectively
		// reset them: failed/timed-out/stopped commands are released (or restarted
		// in place when shared) so runEnvironment can bring them up; healthy/done
		// commands are left running untouched.
		if prev == EnvError || prev == EnvDegraded {
			e.resetForRetry(envCfg)
		}
		e.runEnvironment(envCfg)
	}()
	return nil
}

// runEnvironment drives the workflow dependency graph for one environment.
// Each command starts as soon as all of its dependencies are healthy/done.
// Independent branches start concurrently.
func (e *Engine) runEnvironment(envCfg config.Environment) {
	// Map each command in the workflow to its dependency list.
	deps := make(map[string][]string, len(envCfg.Workflow))
	for _, step := range envCfg.Workflow {
		deps[step.Command] = step.DependsOn
	}

	// readyCh per command in this workflow run: closed once the command reached
	// a terminal start state. okMap records whether it succeeded.
	var (
		mu      sync.Mutex
		readyCh = make(map[string]chan struct{}, len(envCfg.Workflow))
		okMap   = make(map[string]bool, len(envCfg.Workflow))
	)
	for _, step := range envCfg.Workflow {
		readyCh[step.Command] = make(chan struct{})
	}

	var wg sync.WaitGroup
	for _, step := range envCfg.Workflow {
		step := step
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Wait for every dependency to finish starting. If any dependency
			// failed, this command does not start and stays pending.
			depsOK := true
			for _, dep := range deps[step.Command] {
				<-readyCh[dep]
				mu.Lock()
				ok := okMap[dep]
				mu.Unlock()
				if !ok {
					depsOK = false
				}
			}

			ok := false
			if depsOK {
				ok = e.acquireAndStart(envCfg.Name, step.Command)
			}
			// A skipped command (dependency failed) keeps whatever state it had
			// (CmdPending by default) and is reported as not-ok.

			mu.Lock()
			okMap[step.Command] = ok
			mu.Unlock()
			close(readyCh[step.Command])
		}()
	}

	wg.Wait()

	// Compute the final environment state from its commands.
	e.recomputeEnvState(envCfg)
}

// acquireAndStart records envName as a holder of command name and, if this is
// the first holder, starts the command. For subsequent holders it waits for the
// already-running command to be healthy. The add is idempotent: re-acquiring a
// command that envName already holds (e.g. on retry) is a no-op that simply
// reports the current health. Returns true if the command is healthy/done.
func (e *Engine) acquireAndStart(envName, name string) bool {
	e.mu.Lock()
	cmdCfg := e.cmdOf[name]
	c, exists := e.commands[name]
	// A previously stopped/errored command with no remaining holders is recreated
	// fresh so a re-start gets a new process, channels and log ring.
	if exists && len(c.holders) == 0 && (c.state == CmdStopped || c.state == CmdError || c.state == CmdTimeout || c.state == CmdDone) {
		exists = false
	}
	if !exists {
		c = &command{
			cfg:       cmdCfg,
			policy:    resolveRestart(cmdCfg),
			state:     CmdPending,
			ring:      logbuf.NewRing(logRingCapacity),
			startDone: make(chan struct{}),
			exited:    make(chan struct{}),
			holders:   make(map[string]struct{}),
		}
		e.commands[name] = c
	}
	c.holders[envName] = struct{}{} // idempotent: re-acquiring an already-held command is a no-op
	first := !exists
	startDone := c.startDone
	e.mu.Unlock()

	if !first {
		// Another environment already started (or is starting) this command, or
		// envName already holds it (retry of a still-healthy command). Wait for it
		// to settle, then — if envName joining changed the merged env — restart in
		// place to apply it, before reporting health.
		<-startDone
		e.maybeRestartForEnvChange(c, envName, "started")
		return e.isHealthy(name)
	}

	e.startCommand(c)
	return e.isHealthy(name)
}

func (e *Engine) isHealthy(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.commands[name]
	if !ok {
		return false
	}
	return c.state == CmdHealthy || c.state == CmdDone
}

// startCommand opens the command's log writer, then either adopts an
// already-healthy external process (see checkUnmanaged) or performs a full
// managed start via doStart. It always closes c.startDone exactly once.
//
// Holds c.restartMu for its duration: a RestartCommand can race in the
// window between acquireAndStart adding this env as a holder and the first
// startCommand actually settling, so this must serialize against
// relaunch/takeOverUnmanaged/restartCommandWithNewConfig too, not just
// against itself.
func (e *Engine) startCommand(c *command) {
	c.restartMu.Lock()
	defer c.restartMu.Unlock()
	defer close(c.startDone)

	e.setCmdState(c.cfg.Name, CmdStarting, nil)

	// Create the log writer before fetching the source so that git/gh output
	// during clone or pull is captured into the command log instead of leaking
	// to the terminal (which would corrupt the TUI).
	logPath := e.logPath(c.cfg.Name)
	file, err := logbuf.OpenFile(logPath)
	if err != nil {
		// A log-file failure is non-fatal: still capture into the ring.
		file = nil
	}
	c.writer = logbuf.NewWriter(c.ring, file)

	if e.checkUnmanaged(c) {
		return
	}

	e.doStart(c)
}

// checkUnmanaged runs the command's readiness probe (if any) once, before
// anything is spawned. If it already passes, an external process is already
// satisfying it: adopt it as unmanaged (CmdHealthy, no process of our own)
// instead of launching a duplicate, warn loudly, and start watching it so a
// later failure triggers a real managed start (see takeOverUnmanaged).
// Returns false — meaning the caller should proceed with a normal managed
// start — when there is no probe configured or the probe does not pass yet.
func (e *Engine) checkUnmanaged(c *command) bool {
	p, timeout, _ := e.buildProbe(c.cfg.Readiness)
	if p == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := p.Check(ctx); err != nil {
		return false
	}

	e.mu.Lock()
	c.unmanaged = true
	e.mu.Unlock()

	// Log before flipping the state to CmdHealthy: a waiter woken by the state
	// change must already see the warning in the command log.
	e.logCmdWarn(c, fmt.Sprintf("%s is already running and healthy — unmanaged by env-starter", c.cfg.Name))
	e.setCmdState(c.cfg.Name, CmdHealthy, nil)
	e.startUnmanagedMonitor(c)
	return true
}

// startUnmanagedMonitor lazily creates c.quit (exactly like startMonitor) so
// the existing stop path cancels it for free, then watches the external
// process's health in the background.
func (e *Engine) startUnmanagedMonitor(c *command) {
	e.mu.Lock()
	if c.quit == nil {
		c.quit = make(chan struct{})
	}
	e.mu.Unlock()
	go e.superviseUnmanaged(c)
}

// superviseUnmanaged periodically re-checks the readiness probe of an
// adopted-unmanaged command. It returns on user cancellation; on the first
// failed probe it hands off to takeOverUnmanaged and returns.
func (e *Engine) superviseUnmanaged(c *command) {
	p, probeTimeout, probeInterval := e.buildProbe(c.cfg.Readiness)
	if p == nil {
		return
	}

	e.mu.Lock()
	quit := c.quit
	e.mu.Unlock()

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-quit:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			err := p.Check(ctx)
			cancel()
			if err != nil {
				e.takeOverUnmanaged(c, err)
				return
			}
		}
	}
}

// takeOverUnmanaged clears the unmanaged flag and performs a full managed
// start (fetch source, run setup, launch process) now that the previously-
// adopted external process is no longer healthy. It swaps in a fresh
// c.startDone around the start, mirroring how relaunch does it for restarts.
func (e *Engine) takeOverUnmanaged(c *command, causeErr error) {
	c.restartMu.Lock()
	defer c.restartMu.Unlock()

	e.mu.Lock()
	c.unmanaged = false
	c.startDone = make(chan struct{})
	e.mu.Unlock()

	e.logCmdWarn(c, fmt.Sprintf("unmanaged %s is no longer healthy (%v) — starting managed instance", c.cfg.Name, causeErr))
	e.setCmdState(c.cfg.Name, CmdStarting, nil)
	e.doStart(c)
	close(c.startDone)
}

// doStart fetches the source, runs setup, launches the process and (for
// services) waits for the readiness probe. Unlike startCommand it does not
// manage c.startDone — callers (startCommand, takeOverUnmanaged) own that.
func (e *Engine) doStart(c *command) {
	ctx := context.Background()

	runDir, err := e.fetchSource(ctx, c.cfg, c.writer)
	if err != nil {
		fetchErr := fmt.Errorf("fetch source: %w", err)
		e.logCmdError(c, fetchErr)
		_ = c.writer.Close()
		e.setCmdState(c.cfg.Name, CmdError, fetchErr)
		return
	}
	c.runDir = runDir

	if err := e.runSetup(ctx, c, runDir); err != nil {
		setupErr := fmt.Errorf("setup failed: %w", err)
		e.logCmdError(c, setupErr)
		_ = c.writer.Close()
		e.setCmdState(c.cfg.Name, CmdError, setupErr)
		return
	}

	// Serialize interactive browser logins: hold the gate from just before
	// launch until this command settles (handleTask/handleService return),
	// so no other interactive-auth command's browser login can overlap.
	if c.cfg.InteractiveAuth != nil && *c.cfg.InteractiveAuth {
		e.authGate.Lock()
		defer e.authGate.Unlock()
	}

	if err := e.launchProcess(c); err != nil {
		launchErr := fmt.Errorf("start process: %w", err)
		e.logCmdError(c, launchErr)
		_ = c.writer.Close()
		e.setCmdState(c.cfg.Name, CmdError, launchErr)
		return
	}

	if c.cfg.Type == "task" {
		e.handleTask(c)
		return
	}
	e.handleService(c)
}

// launchProcess builds a fresh exec.Cmd for c.cfg.Run in c.runDir, starts it,
// and spawns the reaper goroutine that closes c.exited once. It resets
// c.exited, c.exitErr and c.writeOnce under Engine.mu before starting the
// process.
//
// IMPORTANT: this must only be called when the previous c.exited channel has
// already been closed (i.e. the old process has fully exited), because resetting
// writeOnce is only safe after the old reaper has run.
func (e *Engine) launchProcess(c *command) error {
	// c.appliedEnv records exactly what this process got, so a later holder
	// join/leave can tell whether the merged env actually changed.
	eff := e.effectiveEnv(c)

	// Reset single-use per-process state under the lock so concurrent readers
	// (superviseService, releaseCommand, Shutdown) see a consistent channel.
	e.mu.Lock()
	c.exited = make(chan struct{})
	c.exitErr = nil
	c.writeOnce = sync.Once{}
	c.appliedEnv = eff
	e.mu.Unlock()

	cmd := exec.CommandContext(context.Background(), "sh", "-c", c.cfg.Run)
	cmd.Dir = c.runDir
	cmd.Env = append(os.Environ(), envSlice(eff)...)
	cmd.Stdout = c.writer
	cmd.Stderr = c.writer
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return err
	}

	e.mu.Lock()
	c.cmd = cmd
	e.mu.Unlock()

	// Reap the process in the background. The reaper records the exit error and
	// closes c.exited exactly once; everything else observes the exit via that
	// channel. The writer is closed here too so logs flush on exit.
	go func() {
		err := cmd.Wait()
		c.exitErr = err
		c.writeOnce.Do(func() { _ = c.writer.Close() })
		close(c.exited)
	}()

	return nil
}

// handleTask waits for the process to complete, then optionally runs a readiness
// probe. Without a probe the task ends in CmdDone on exit 0 (existing behaviour).
// With a probe the task ends in CmdHealthy when the probe passes, allowing
// dependents to unblock and, if restart is configured, enabling liveness monitoring.
func (e *Engine) handleTask(c *command) {
	<-c.exited
	if c.exitErr != nil {
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("task failed: %w", c.exitErr))
		return
	}

	p, timeout, interval := e.buildProbe(c.cfg.Readiness)
	if p == nil {
		// No probe: task completed successfully.
		e.setCmdState(c.cfg.Name, CmdDone, nil)
		return
	}

	// Task exited 0; run the readiness probe to confirm the side effect is ready
	// (e.g. a tunnel is accepting connections).
	readyDone := make(chan error, 1)
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	go func() {
		readyDone <- probe.WaitReady(probeCtx, p, timeout, interval)
	}()

	rerr := <-readyDone
	if rerr != nil {
		if errors.Is(rerr, probe.ErrTimeout) {
			e.setCmdState(c.cfg.Name, CmdTimeout, fmt.Errorf("readiness: %w", rerr))
		} else {
			e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("readiness: %w", rerr))
		}
		// The process has already exited; no terminate() needed.
		return
	}

	e.setCmdState(c.cfg.Name, CmdHealthy, nil)
	// Start liveness monitoring only when a periodic check is configured.
	if c.policy.enabled && c.policy.checkInterval > 0 {
		e.startMonitor(c)
	}
}

// handleService waits for the readiness probe (if any) to pass, racing against
// an early process exit. Once healthy, it hands off to startMonitor.
func (e *Engine) handleService(c *command) {
	p, timeout, interval := e.buildProbe(c.cfg.Readiness)

	if p == nil {
		// No probe: healthy shortly after launch, unless it exits early. Give
		// the process a brief grace window to surface an immediate failure.
		select {
		case <-c.exited:
			e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("service exited before becoming healthy: %w", errOrExit(c.exitErr)))
			return
		case <-time.After(noProbeGrace):
		}
		e.setCmdState(c.cfg.Name, CmdHealthy, nil)
		e.startMonitor(c)
		return
	}

	// Race readiness against an early process exit.
	readyDone := make(chan error, 1)
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	go func() {
		readyDone <- probe.WaitReady(probeCtx, p, timeout, interval)
	}()

	select {
	case <-c.exited:
		cancelProbe()
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("service exited before becoming healthy: %w", errOrExit(c.exitErr)))
		return
	case rerr := <-readyDone:
		if rerr != nil {
			if errors.Is(rerr, probe.ErrTimeout) {
				e.setCmdState(c.cfg.Name, CmdTimeout, fmt.Errorf("readiness: %w", rerr))
			} else {
				e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("readiness: %w", rerr))
			}
			// Tear down the unhealthy process and wait for it to exit.
			e.terminate(c)
			return
		}
		e.setCmdState(c.cfg.Name, CmdHealthy, nil)
		e.startMonitor(c)
	}
}

// startMonitor creates the per-command quit channel (once) and launches the
// supervise goroutine that owns the command's runtime from healthy onward.
// Services are supervised by superviseService; tasks (whose process has already
// exited) are supervised by superviseTask.
func (e *Engine) startMonitor(c *command) {
	e.mu.Lock()
	if c.quit == nil {
		c.quit = make(chan struct{})
	}
	c.monitorDone = make(chan struct{})
	e.mu.Unlock()
	if c.cfg.Type == "task" {
		go e.superviseTask(c)
	} else {
		go e.superviseService(c)
	}
}

// superviseService is the single owner goroutine for a healthy service. It
// selects over three signals:
//   - process exit (c.exited)
//   - periodic liveness probe failure
//   - user cancellation (c.quit)
//
// On process exit or liveness failure it attempts a restart (if enabled);
// on cancellation it returns immediately, letting the stop path win.
func (e *Engine) superviseService(c *command) {
	e.mu.Lock()
	done := c.monitorDone
	e.mu.Unlock()
	defer close(done)

	for {
		// Snapshot the current channels under the lock so we observe fresh
		// channels if a previous restart cycle swapped them in.
		e.mu.Lock()
		exited := c.exited
		quit := c.quit
		e.mu.Unlock()

		livenessStop := make(chan struct{})
		livenessFail := e.runLiveness(c, livenessStop)

		var lastErr error
		select {
		case <-quit:
			close(livenessStop)
			return // user stop wins

		case <-exited:
			close(livenessStop)
			e.mu.Lock()
			stopped := c.userStopped
			exitErr := c.exitErr
			e.mu.Unlock()
			if stopped {
				return
			}
			lastErr = fmt.Errorf("service exited unexpectedly: %w", errOrExit(exitErr))

		case err := <-livenessFail:
			close(livenessStop)
			e.mu.Lock()
			stopped := c.userStopped
			e.mu.Unlock()
			if stopped {
				return
			}
			lastErr = fmt.Errorf("liveness probe failed: %w", err)
		}

		if !c.policy.enabled {
			e.setCmdState(c.cfg.Name, CmdError, lastErr)
			e.recomputeEnvsFor(c.cfg.Name)
			return
		}

		if !e.attemptRestart(c, lastErr) {
			return // gave up or cancelled
		}
		// Restart succeeded: c.exited and c.quit were refreshed by relaunch;
		// loop back to re-snapshot and resume monitoring.
	}
}

// superviseTask is the single owner goroutine for a healthy task whose process
// has already exited (e.g. a tunnel that backgrounded itself). It watches only
// the liveness probe — there is no process-exit signal to watch.
//
// On liveness failure it attempts a restart (re-run the task, then re-probe);
// on cancellation it returns immediately.
func (e *Engine) superviseTask(c *command) {
	e.mu.Lock()
	done := c.monitorDone
	e.mu.Unlock()
	defer close(done)

	for {
		e.mu.Lock()
		quit := c.quit
		e.mu.Unlock()

		livenessStop := make(chan struct{})
		livenessFail := e.runLiveness(c, livenessStop)

		select {
		case <-quit:
			close(livenessStop)
			return // user stop wins

		case err := <-livenessFail:
			close(livenessStop)
			e.mu.Lock()
			stopped := c.userStopped
			e.mu.Unlock()
			if stopped {
				return
			}
			lastErr := fmt.Errorf("liveness probe failed: %w", err)

			if !c.policy.enabled {
				e.setCmdState(c.cfg.Name, CmdError, lastErr)
				e.recomputeEnvsFor(c.cfg.Name)
				return
			}

			if !e.attemptRestart(c, lastErr) {
				return // gave up or cancelled
			}
			// Restart succeeded; loop back to resume monitoring.
		}
	}
}

// runLiveness runs a single probe.Check call every checkInterval and sends
// the first failure on the returned channel. It exits when stop is closed.
// Returns a never-firing channel if no probe is configured or checkInterval
// is zero.
func (e *Engine) runLiveness(c *command, stop <-chan struct{}) <-chan error {
	out := make(chan error, 1) // buffered so the goroutine never leaks
	p, probeTimeout, _ := e.buildProbe(c.cfg.Readiness)
	interval := c.policy.checkInterval
	if p == nil || interval <= 0 {
		return out // never fires
	}
	go func() {
		// interval is captured here rather than read again inside the
		// goroutine: c.policy can be mutated by restartCommandWithNewConfig
		// once the monitor goroutine (the caller of runLiveness) has fully
		// exited, and this ticker goroutine's lifetime is not otherwise
		// bounded by that exit.
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
				err := p.Check(ctx)
				cancel()
				if err != nil {
					out <- err
					return
				}
			}
		}
	}()
	return out
}

// attemptRestart drives the full retry cycle for one unhealthy episode:
// teardown → backoff sleep → relaunch → readiness wait. Returns true if a
// relaunch succeeds (monitoring resumes), false if retries are exhausted or
// the cycle is cancelled by a user stop.
func (e *Engine) attemptRestart(c *command, causeErr error) bool {
	c.restartMu.Lock()
	defer c.restartMu.Unlock()

	for {
		e.mu.Lock()
		retries := c.retries
		quit := c.quit
		e.mu.Unlock()

		if retries >= c.policy.maxRetries {
			e.setCmdState(c.cfg.Name, CmdError,
				fmt.Errorf("restart: gave up after %d attempt(s): %w", c.policy.maxRetries, causeErr))
			e.recomputeEnvsFor(c.cfg.Name)
			return false
		}

		e.setCmdState(c.cfg.Name, CmdRestarting,
			fmt.Errorf("restarting (attempt %d/%d): %w", retries+1, c.policy.maxRetries, causeErr))

		e.teardownForRestart(c)

		// Cancellable backoff sleep: 1x, 2x, 4x the base duration.
		backoff := c.policy.backoffBase << uint(retries)
		select {
		case <-time.After(backoff):
		case <-quit:
			return false // user stop wins mid-backoff
		}

		e.mu.Lock()
		stopped := c.userStopped
		e.mu.Unlock()
		if stopped {
			return false
		}

		if e.relaunch(c) {
			e.mu.Lock()
			c.retries = 0
			e.mu.Unlock()
			e.setCmdState(c.cfg.Name, CmdHealthy, nil)
			e.recomputeEnvsFor(c.cfg.Name)
			return true
		}

		e.mu.Lock()
		c.retries++
		causeErr = c.err // capture the error from the failed relaunch
		e.mu.Unlock()
	}
}

// teardownForRestart runs the teardown script and kills the process without
// marking the command CmdStopped. It is used by the restart path to clean up
// before a relaunch. If the process has already exited (crash case), the kill
// is a no-op.
func (e *Engine) teardownForRestart(c *command) {
	e.runTeardown(c)
	e.killAndWait(c)
}

// relaunch starts a fresh process for c (reusing c.runDir and c.ring) and
// waits for its readiness probe. It swaps in a fresh c.startDone so any
// concurrent releaseCommand or Shutdown waiter blocks correctly.
// Returns true if the relaunched process becomes healthy.
//
// Callers must hold c.restartMu for the duration of the call (see
// command.restartMu) — relaunch does not acquire it itself, since some
// callers (attemptRestart) need the lock to also cover the teardown and
// backoff that surround the relaunch call within one restart episode.
func (e *Engine) relaunch(c *command) bool {
	// Serialize interactive browser logins on restart too: hold the gate from
	// here until this function returns (after the relaunched process settles),
	// so a re-login can't overlap another interactive-auth command's login.
	if c.cfg.InteractiveAuth != nil && *c.cfg.InteractiveAuth {
		e.authGate.Lock()
		defer e.authGate.Unlock()
	}

	// Swap in a fresh start barrier so releaseCommand/Shutdown waiters block
	// until this relaunch settles. The deferred close covers the error paths.
	e.mu.Lock()
	c.startDone = make(chan struct{})
	e.mu.Unlock()

	settled := false
	defer func() {
		if !settled {
			close(c.startDone)
		}
	}()

	// Reopen the log writer for the new process run.
	logPath := e.logPath(c.cfg.Name)
	file, err := logbuf.OpenFile(logPath)
	if err != nil {
		file = nil
	}

	e.mu.Lock()
	c.writer = logbuf.NewWriter(c.ring, file)
	e.mu.Unlock()

	if err := e.launchProcess(c); err != nil {
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("relaunch: start process: %w", err))
		return false
	}

	// Wait for the process to be ready. Tasks are expected to exit first then pass
	// a probe; services race their probe against an early process exit.
	var ok bool
	if c.cfg.Type == "task" {
		ok = e.waitTaskReady(c)
	} else {
		ok = e.waitReadyOrExit(c)
	}
	close(c.startDone)
	settled = true
	return ok
}

// waitReadyOrExit waits for the readiness probe to pass or the process to exit,
// returning true only when the probe succeeds. It mirrors the probe-race logic
// in handleService but does not emit CmdHealthy (the caller does that).
func (e *Engine) waitReadyOrExit(c *command) bool {
	p, timeout, interval := e.buildProbe(c.cfg.Readiness)

	e.mu.Lock()
	exited := c.exited
	e.mu.Unlock()

	if p == nil {
		select {
		case <-exited:
			return false
		case <-time.After(noProbeGrace):
			return true
		}
	}

	readyDone := make(chan error, 1)
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	go func() {
		readyDone <- probe.WaitReady(probeCtx, p, timeout, interval)
	}()

	select {
	case <-exited:
		cancelProbe()
		e.mu.Lock()
		exitErr := c.exitErr
		e.mu.Unlock()
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("relaunch: service exited before becoming healthy: %w", errOrExit(exitErr)))
		return false
	case rerr := <-readyDone:
		if rerr != nil {
			if errors.Is(rerr, probe.ErrTimeout) {
				e.setCmdState(c.cfg.Name, CmdTimeout, fmt.Errorf("relaunch readiness: %w", rerr))
			} else {
				e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("relaunch readiness: %w", rerr))
			}
			e.terminate(c)
			return false
		}
		return true
	}
}

// waitTaskReady waits for a task's process to exit, then runs the readiness probe
// to verify the side effect (e.g. tunnel open). It returns true only when the
// probe passes. Unlike waitReadyOrExit there is no process to race against — the
// process is expected to exit first before the probe makes sense.
//
// Called exclusively from relaunch for task-type commands; does NOT emit
// CmdHealthy (the caller, attemptRestart, does that).
func (e *Engine) waitTaskReady(c *command) bool {
	p, timeout, interval := e.buildProbe(c.cfg.Readiness)

	e.mu.Lock()
	exited := c.exited
	e.mu.Unlock()

	// Wait for the task process to exit.
	<-exited

	e.mu.Lock()
	exitErr := c.exitErr
	e.mu.Unlock()

	if exitErr != nil {
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("relaunch: task failed: %w", exitErr))
		return false
	}

	if p == nil {
		// Restart on a task without a probe is blocked by validation, but be
		// defensive: treat a successful exit as ready.
		return true
	}

	readyDone := make(chan error, 1)
	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()
	go func() {
		readyDone <- probe.WaitReady(probeCtx, p, timeout, interval)
	}()

	rerr := <-readyDone
	if rerr != nil {
		if errors.Is(rerr, probe.ErrTimeout) {
			e.setCmdState(c.cfg.Name, CmdTimeout, fmt.Errorf("relaunch readiness: %w", rerr))
		} else {
			e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("relaunch readiness: %w", rerr))
		}
		// The process has already exited; no terminate() needed.
		return false
	}
	return true
}

// buildProbe constructs a probe from a readiness config, returning nil when no
// probe is configured along with the timeout/interval to use.
func (e *Engine) buildProbe(r *config.Readiness) (probe.Probe, time.Duration, time.Duration) {
	timeout := e.ProbeTimeout
	interval := e.ProbeInterval
	if r == nil {
		return nil, timeout, interval
	}
	if r.Timeout != nil && r.Timeout.Duration > 0 {
		timeout = r.Timeout.Duration
	}
	if r.Interval != nil && r.Interval.Duration > 0 {
		interval = r.Interval.Duration
	}
	switch {
	case r.TCP != "":
		return probe.NewTCP(r.TCP, timeout, interval), timeout, interval
	case r.Shell != "":
		return probe.NewShell(r.Shell, timeout, interval), timeout, interval
	default:
		return nil, timeout, interval
	}
}

// fetchSource builds the right source.Source for a command and returns the run
// directory, applying Subdir for Local and URL sources (GitHub applies it
// internally). out receives git/gh stdout and stderr for GitHub sources so that
// fetch output is captured in the command log rather than leaking to the terminal.
func (e *Engine) fetchSource(ctx context.Context, cmd config.Command, out io.Writer) (string, error) {
	src := cmd.Source
	switch {
	case src.Local != "":
		dir, err := source.Local{Path: src.Local}.Fetch(ctx)
		if err != nil {
			return "", err
		}
		return appendSubdir(dir, src.Subdir)
	case src.GitHub != nil:
		gh := &source.GitHub{
			Repo:   src.GitHub.Repo,
			Branch: src.GitHub.Branch,
			Method: src.GitHub.Method,
			Subdir: src.Subdir,
			Output: out,
		}
		return gh.Fetch(ctx)
	case src.URLSource != nil:
		u := &source.URL{URL: src.URLSource.URL}
		if src.URLSource.Checksum != nil {
			u.ChecksumAlg = src.URLSource.Checksum.Alg
			u.ChecksumValue = src.URLSource.Checksum.Value
		}
		dir, err := u.Fetch(ctx)
		if err != nil {
			return "", err
		}
		return appendSubdir(dir, src.Subdir)
	default:
		return "", fmt.Errorf("command %q: no source configured", cmd.Name)
	}
}

// appendSubdir joins subdir onto dir and verifies the resulting path exists.
// Returns an error when subdir is set but does not exist inside dir, so callers
// can surface a clear diagnostic rather than passing a non-existent path to the
// process runner.
func appendSubdir(dir, subdir string) (string, error) {
	if subdir == "" {
		return dir, nil
	}
	target := filepath.Join(dir, subdir)
	if _, err := os.Stat(target); err != nil {
		return "", fmt.Errorf("subdir %q does not exist in %s: %w", subdir, dir, err)
	}
	return target, nil
}

// logCmdError writes a human-readable failure line to the command log (TUI panel
// + log file) so configuration problems are visible, not just flagged with ✗.
// Must be called while c.writer is still open.
func (e *Engine) logCmdError(c *command, err error) {
	_, _ = fmt.Fprintf(c.writer, "\nenv-starter: %v\n", err)
}

// logCmdWarn writes a red (ANSI SGR) human-readable warning line to the
// command log (TUI panel + log file). Used for non-fatal but noteworthy
// conditions, such as adopting or taking over an unmanaged command.
// internal/termsafe preserves SGR sequences, so the color survives into both
// the TUI's log pane and the headless CLI's tailed output.
func (e *Engine) logCmdWarn(c *command, msg string) {
	_, _ = fmt.Fprintf(c.writer, "\n\x1b[31menv-starter: %s\x1b[0m\n", msg)
}

func (e *Engine) logPath(cmdName string) string {
	return filepath.Join(e.logsDir, cmdName+".log")
}

// runSetup executes each setup command sequentially in runDir, streaming output
// to the shared writer. It returns on the first command that exits non-zero.
func (e *Engine) runSetup(ctx context.Context, c *command, runDir string) error {
	eff := e.effectiveEnv(c)
	for _, step := range c.cfg.Setup {
		cmd := exec.CommandContext(ctx, "sh", "-c", step)
		cmd.Dir = runDir
		cmd.Env = append(os.Environ(), envSlice(eff)...)
		cmd.Stdout = c.writer
		cmd.Stderr = c.writer
		setSysProcAttr(cmd)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%q: %w", step, err)
		}
	}
	return nil
}

// effectiveEnv computes the environment variables that should be applied to
// c's process: the union of Env from every environment currently holding it
// (c.holders), overridden by the command's own cfg.Env. Config validation
// (see internal/config) guarantees the holder union never has a real
// conflict — two environments disagreeing on a key the command itself does
// not override is rejected at load time — so this is a plain last-write-wins
// union, safe to recompute on every call.
func (e *Engine) effectiveEnv(c *command) map[string]string {
	out := e.holderEnvUnion(c)

	e.mu.Lock()
	cmdEnv := c.cfg.Env
	e.mu.Unlock()

	for k, v := range cmdEnv {
		out[k] = v
	}
	return out
}

// holderEnvUnion returns the union of Env from every environment currently
// holding c (c.holders), without the command's own override layer. Split out
// from effectiveEnv so the env inspector (see resolve_env.go) can resolve the
// environment-level and command-level layers separately for provenance,
// while still sharing this exact holder-union logic with the launch path.
func (e *Engine) holderEnvUnion(c *command) map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string)
	for h := range c.holders {
		for k, v := range e.envEnvOf[h] {
			out[k] = v
		}
	}
	return out
}

// teardownEnv returns the env that should be applied to a command's teardown
// script: the env that was actually applied to its running process
// (c.appliedEnv), so teardown cleans up exactly what run set up — even if a
// holder has already been released (and so excluded from a fresh
// effectiveEnv) by the time teardown runs. Falls back to a fresh
// effectiveEnv when the process was never successfully launched (e.g. a setup
// failure before launchProcess ran).
func (e *Engine) teardownEnv(c *command) map[string]string {
	e.mu.Lock()
	applied := c.appliedEnv
	e.mu.Unlock()
	if applied != nil {
		return applied
	}
	return e.effectiveEnv(c)
}

// maybeRestartForEnvChange restarts a healthy, running shared command in
// place when a holder join/leave (holderName, action) changed its effective
// env (see effectiveEnv), so the process picks up the new merged set. It is a
// no-op when the command is not currently healthy, or when the effective env
// did not actually change.
func (e *Engine) maybeRestartForEnvChange(c *command, holderName, action string) {
	if !e.isHealthy(c.cfg.Name) {
		return
	}
	eff := e.effectiveEnv(c)
	e.mu.Lock()
	unchanged := maps.Equal(c.appliedEnv, eff)
	e.mu.Unlock()
	if unchanged {
		return
	}
	e.logCmdWarn(c, fmt.Sprintf("restarting %q to apply environment variables (environment %q %s)", c.cfg.Name, holderName, action))
	e.restartCommandInPlace(c)
}

// envSlice converts an env map to the "KEY=VALUE" slice form exec expects.
func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func errOrExit(err error) error {
	if err == nil {
		return fmt.Errorf("exit 0")
	}
	return err
}
