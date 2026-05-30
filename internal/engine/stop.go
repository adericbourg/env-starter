package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
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

// releaseWorkflow releases every command in an environment's workflow once,
// decrementing reference counts and tearing down commands whose count reaches zero.
func (e *Engine) releaseWorkflow(envCfg config.Environment) {
	for _, step := range envCfg.Workflow {
		e.releaseCommand(step.Command)
	}
}

// releaseCommand decrements a command's refcount and stops it when it reaches
// zero.
func (e *Engine) releaseCommand(name string) {
	e.mu.Lock()
	c, ok := e.commands[name]
	if !ok {
		e.mu.Unlock()
		return
	}
	if c.refcount > 0 {
		c.refcount--
	}
	shouldStop := c.refcount == 0
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
		e.setCmdState(c.cfg.Name, CmdStopped, nil)
	}

	// Clear the stop timer so the command no longer appears in StoppingCommands
	// once teardown is complete.
	e.mu.Lock()
	c.stopStartedAt = time.Time{}
	e.mu.Unlock()
}

// terminate sends SIGINT to a service's process group, waits up to GracePeriod
// for it to exit, then SIGKILLs it. It pre-marks the state CmdStopped so the
// supervise goroutine does not flip the state back to error on process exit.
// For restart-path kills, use killAndWait instead (no state pre-mark).
func (e *Engine) terminate(c *command) {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}

	// Mark stopped first so the supervise goroutine does not flip the state to
	// error when it observes the process exit.
	e.mu.Lock()
	if c.state == CmdHealthy || c.state == CmdStopping {
		c.state = CmdStopped
	}
	e.mu.Unlock()

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

// runTeardown runs a task's Teardown script (if any) via sh -c in its run dir.
func (e *Engine) runTeardown(c *command) {
	if c.cfg.Teardown == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", c.cfg.Teardown)
	if c.runDir != "" {
		cmd.Dir = c.runDir
	}
	cmd.Env = append(os.Environ(), envSlice(c.cfg.Env)...)
	if c.writer != nil {
		cmd.Stdout = c.writer
		cmd.Stderr = c.writer
	}
	setSysProcAttr(cmd)
	_ = cmd.Run()
}

// Shutdown stops every currently-running command gracefully, respecting ctx as
// an overall deadline. Reference counts are reset.
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
