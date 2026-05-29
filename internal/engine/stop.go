package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// StopEnvironment decrements the reference count of every command in the named
// environment's workflow. A command is actually torn down only when its
// reference count reaches zero. Returns an error if the environment is unknown.
func (e *Engine) StopEnvironment(env string) error {
	envCfg, ok := e.findEnv(env)
	if !ok {
		return fmt.Errorf("engine: unknown environment %q", env)
	}

	for _, step := range envCfg.Workflow {
		e.releaseCommand(step.Command)
	}

	e.setEnvState(env, EnvStopped)
	return nil
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

	// Wait until the command has finished starting before stopping it, so we do
	// not race the launch. The stopped entry is kept in the map (reporting
	// CmdStopped) and recreated on a subsequent start.
	<-c.startDone

	e.stopCommand(c)
}

// stopCommand tears a single command down: services are signalled then killed;
// tasks run their teardown (if any).
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
		e.mu.Lock()
		running := c.cmd != nil && c.state != CmdError
		e.mu.Unlock()
		if running {
			e.terminate(c)
		}
		e.setCmdState(c.cfg.Name, CmdStopped, nil)
	}
}

// terminate sends SIGINT to a service's process group, waits up to GracePeriod
// for it to exit, then SIGKILLs it.
func (e *Engine) terminate(c *command) {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}

	// Mark stopped first so the exit watcher does not flip the state to error.
	e.mu.Lock()
	if c.state == CmdHealthy {
		c.state = CmdStopped
	}
	e.mu.Unlock()

	_ = interruptProcess(c.cmd)

	grace := e.GracePeriod
	if grace <= 0 {
		grace = defaultGracePeriod
	}
	select {
	case <-c.exited:
		return
	case <-time.After(grace):
		_ = killProcess(c.cmd)
		<-c.exited
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
			<-c.startDone
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
