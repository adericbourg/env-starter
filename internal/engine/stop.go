package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
func (e *Engine) stopCommand(c *command) {
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
	defer w.Close()

	cmd := exec.CommandContext(ctx, "sh", "-c", c.cfg.Teardown)
	if c.runDir != "" {
		cmd.Dir = c.runDir
	}
	cmd.Env = append(os.Environ(), envSlice(c.cfg.Env)...)
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

// Shutdown stops every currently-running command gracefully, respecting ctx as
// an overall deadline. All holders are cleared.
func (e *Engine) Shutdown(ctx context.Context) {
	e.mu.Lock()
	names := make([]string, 0, len(e.commands))
	cmds := make([]*command, 0, len(e.commands))
	for name, c := range e.commands {
		names = append(names, name)
		cmds = append(cmds, c)
	}
	e.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for _, c := range cmds {
			// Cancel any in-flight restart/monitor cycle before stopping.
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
			<-startDone
			e.stopCommand(c)
		}
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
