package engine

import (
	"context"
	"fmt"
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
func (e *Engine) StartEnvironment(env string) error {
	envCfg, ok := e.findEnv(env)
	if !ok {
		return fmt.Errorf("engine: unknown environment %q", env)
	}

	e.setEnvState(env, EnvStarting)

	go e.runEnvironment(envCfg)
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

// acquireAndStart increments the refcount for command name and, if it is the
// first reference, starts it. For subsequent references it waits for the
// already-running command to be healthy. Returns true if the command is
// healthy/done.
func (e *Engine) acquireAndStart(envName, name string) bool {
	cmdCfg := e.cmdOf[name]

	e.mu.Lock()
	c, exists := e.commands[name]
	// A previously stopped/errored command (refcount 0) is recreated fresh so a
	// re-start gets a new process, channels and log ring.
	if exists && c.refcount == 0 && (c.state == CmdStopped || c.state == CmdError || c.state == CmdDone) {
		exists = false
	}
	if !exists {
		c = &command{
			cfg:       cmdCfg,
			state:     CmdPending,
			ring:      logbuf.NewRing(logRingCapacity),
			startDone: make(chan struct{}),
			exited:    make(chan struct{}),
		}
		e.commands[name] = c
	}
	c.refcount++
	first := !exists
	startDone := c.startDone
	e.mu.Unlock()

	if !first {
		// Another environment already started (or is starting) this command.
		// Wait for it to settle, then report its health.
		<-startDone
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

// startCommand fetches the source, launches the process and (for services)
// waits for the readiness probe. It always closes c.startDone exactly once.
func (e *Engine) startCommand(c *command) {
	defer close(c.startDone)

	e.setCmdState(c.cfg.Name, CmdStarting, nil)

	ctx := context.Background()

	runDir, err := e.fetchSource(ctx, c.cfg)
	if err != nil {
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("fetch source: %w", err))
		return
	}
	c.runDir = runDir

	logPath := e.logPath(c.cfg.Name)
	file, err := logbuf.OpenFile(logPath)
	if err != nil {
		// A log-file failure is non-fatal: still capture into the ring.
		file = nil
	}
	c.writer = logbuf.NewWriter(c.ring, file)

	if err := e.runSetup(ctx, c, runDir); err != nil {
		c.writer.Close()
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("setup failed: %w", err))
		return
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", c.cfg.Run)
	cmd.Dir = runDir
	cmd.Env = append(os.Environ(), envSlice(c.cfg.Env)...)
	cmd.Stdout = c.writer
	cmd.Stderr = c.writer
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		c.writer.Close()
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("start process: %w", err))
		return
	}
	c.cmd = cmd

	// Reap the process in the background. The reaper records the exit error and
	// closes c.exited exactly once; everything else observes the exit via that
	// channel. The writer is closed here too so logs flush on exit.
	go func() {
		err := cmd.Wait()
		c.exitErr = err
		c.writeOnce.Do(func() { c.writer.Close() })
		close(c.exited)
	}()

	if c.cfg.Type == "task" {
		e.handleTask(c)
		return
	}
	e.handleService(c)
}

// handleTask waits for the process to complete: exit 0 → done, otherwise error.
func (e *Engine) handleTask(c *command) {
	<-c.exited
	if c.exitErr != nil {
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("task failed: %w", c.exitErr))
		return
	}
	e.setCmdState(c.cfg.Name, CmdDone, nil)
}

// handleService waits for the readiness probe (if any) to pass, racing against
// an early process exit. Once healthy, it watches for an unexpected exit.
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
		e.watchServiceExit(c)
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
			e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("readiness: %w", rerr))
			// Tear down the unhealthy process and wait for it to exit.
			e.terminate(c)
			return
		}
		e.setCmdState(c.cfg.Name, CmdHealthy, nil)
		e.watchServiceExit(c)
	}
}

// watchServiceExit marks a healthy service as errored if its process exits
// unexpectedly. It returns immediately; the watch runs in a goroutine.
func (e *Engine) watchServiceExit(c *command) {
	go func() {
		<-c.exited

		e.mu.Lock()
		// If the command was deliberately stopped, leave it as stopped.
		stopped := c.state == CmdStopped
		e.mu.Unlock()
		if stopped {
			return
		}
		e.setCmdState(c.cfg.Name, CmdError, fmt.Errorf("service exited unexpectedly: %w", errOrExit(c.exitErr)))
	}()
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
// internally).
func (e *Engine) fetchSource(ctx context.Context, cmd config.Command) (string, error) {
	src := cmd.Source
	switch {
	case src.Local != "":
		dir, err := source.Local{Path: src.Local}.Fetch(ctx)
		if err != nil {
			return "", err
		}
		return appendSubdir(dir, src.Subdir), nil
	case src.GitHub != nil:
		gh := &source.GitHub{
			Repo:   src.GitHub.Repo,
			Branch: src.GitHub.Branch,
			Method: src.GitHub.Method,
			Subdir: src.Subdir,
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
		return appendSubdir(dir, src.Subdir), nil
	default:
		return "", fmt.Errorf("command %q: no source configured", cmd.Name)
	}
}

func appendSubdir(dir, subdir string) string {
	if subdir == "" {
		return dir
	}
	return filepath.Join(dir, subdir)
}

func (e *Engine) logPath(cmdName string) string {
	base, err := source.CacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "logs", cmdName+".log")
}

// runSetup executes each setup command sequentially in runDir, streaming output
// to the shared writer. It returns on the first command that exits non-zero.
func (e *Engine) runSetup(ctx context.Context, c *command, runDir string) error {
	for _, step := range c.cfg.Setup {
		cmd := exec.CommandContext(ctx, "sh", "-c", step)
		cmd.Dir = runDir
		cmd.Env = append(os.Environ(), envSlice(c.cfg.Env)...)
		cmd.Stdout = c.writer
		cmd.Stderr = c.writer
		setSysProcAttr(cmd)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%q: %w", step, err)
		}
	}
	return nil
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
