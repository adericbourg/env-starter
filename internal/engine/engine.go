// Package engine is the orchestration core of env-starter: a foreground
// supervisor that starts and stops environments made of globally-shared
// commands, respecting the dependency graph declared in each environment's
// workflow.
//
// A command is identified by its name and runs once globally. Starting an
// environment records that environment as a holder of the command (and waits
// for it to be healthy if already running). Stopping an environment removes
// its hold; a command is actually torn down only when no environment holds it.
package engine

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/logbuf"
	"github.com/adericbourg/env-starter/internal/source"
)

// EnvState is the lifecycle state of an environment.
type EnvState string

const (
	EnvStopped  EnvState = "stopped"
	EnvStarting EnvState = "starting"
	EnvRunning  EnvState = "running"
	EnvDegraded EnvState = "degraded"
	EnvStopping EnvState = "stopping"
	EnvError    EnvState = "error"
)

// CmdState is the lifecycle state of a command.
type CmdState string

const (
	CmdPending    CmdState = "pending"
	CmdStarting   CmdState = "starting"
	CmdHealthy    CmdState = "healthy"
	CmdDone       CmdState = "done"
	CmdStopping   CmdState = "stopping"
	CmdError      CmdState = "error"
	CmdTimeout    CmdState = "timeout"
	CmdStopped    CmdState = "stopped"
	CmdRestarting CmdState = "restarting"
)

// EnvInfo is a lightweight description of an environment.
type EnvInfo struct {
	Name        string
	Description string
	AutoStart   bool
}

// Event is emitted on every state change. Command events carry Command and
// CmdState; environment events carry Environment and EnvState.
type Event struct {
	Kind        string // "environment" | "command"
	Environment string
	Command     string
	EnvState    EnvState
	CmdState    CmdState
	Err         error
}

const (
	defaultGracePeriod   = 30 * time.Second
	defaultProbeTimeout  = 30 * time.Second
	defaultProbeTick     = 1 * time.Second
	defaultMaxRetries    = 3
	defaultBackoffBase   = 1 * time.Second
	defaultCheckInterval = 10 * time.Second
	eventBufferSize      = 256
	logRingCapacity      = 1000
	// noProbeGrace is how long a probe-less service is observed before being
	// declared healthy, so an immediate non-zero exit surfaces as an error.
	noProbeGrace = 150 * time.Millisecond
)

// restartPolicy is the resolved, per-command restart configuration.
type restartPolicy struct {
	enabled       bool
	maxRetries    int
	backoffBase   time.Duration
	checkInterval time.Duration // 0 means no periodic liveness check
}

// resolveRestart computes a concrete restart policy from the command's config.
// Services default to auto-restart on. Tasks opt in by declaring a restart block;
// without one they are off.
func resolveRestart(cmd config.Command) restartPolicy {
	// Tasks without an explicit restart block are off by default.
	if cmd.Type == "task" && cmd.Restart == nil {
		return restartPolicy{}
	}

	p := restartPolicy{
		enabled:       true,
		maxRetries:    defaultMaxRetries,
		backoffBase:   defaultBackoffBase,
		checkInterval: defaultCheckInterval,
	}

	r := cmd.Restart
	if r != nil {
		if r.Enabled != nil {
			p.enabled = *r.Enabled
		}
		if r.MaxRetries != nil && *r.MaxRetries >= 0 {
			p.maxRetries = *r.MaxRetries
		}
		if r.BackoffBase != nil && r.BackoffBase.Duration > 0 {
			p.backoffBase = r.BackoffBase.Duration
		}
		if r.CheckInterval != nil {
			// A non-nil but zero value explicitly disables liveness checking.
			p.checkInterval = r.CheckInterval.Duration
		}
	}

	// Liveness checking requires a readiness probe. Without one, the only
	// unhealthy signal is the process exiting. Enforce this regardless of what
	// the config declares.
	if cmd.Readiness == nil {
		p.checkInterval = 0
	}

	return p
}

// StoppingCommand describes a command currently being torn down, for the
// shutdown screen. Elapsed is the time since it began stopping; Grace is the
// per-process SIGINT→SIGKILL budget.
type StoppingCommand struct {
	Command string
	Elapsed time.Duration
	Grace   time.Duration
}

// command is the runtime state of a single (globally-shared) command.
// All fields are guarded by Engine.mu — the command struct has no own mutex.
type command struct {
	cfg    config.Command
	policy restartPolicy // resolved once at creation

	state CmdState
	err   error

	// unmanaged is true while this command is healthy only because its
	// readiness probe already passed before env-starter spawned anything —
	// i.e. an external process is already satisfying it. No source was
	// fetched, no setup ran, and c.cmd/c.runDir are unset. Cleared once the
	// probe fails and env-starter takes over with a real managed start.
	unmanaged bool

	// holders is the set of environment names currently referencing this command.
	// The command is torn down only when it becomes empty. Per-env tracking makes
	// acquire/release idempotent, so a retry can skip healthy commands without
	// double-counting or restarting them.
	holders map[string]struct{}

	// restart tracking
	retries     int  // restart attempts consumed in the current cycle; reset to 0 on success
	userStopped bool // set by the stop path to signal that no restart should follow

	// quit is closed exactly once (via quitOnce) to cancel the supervise/restart
	// owner goroutine. Created when the first monitor goroutine starts (i.e. when
	// the service first becomes healthy) and valid until the command is destroyed.
	quit     chan struct{}
	quitOnce sync.Once

	// stopStartedAt is set (to the current time) when the command begins being
	// torn down and cleared (to zero) when teardown completes. A non-zero value
	// means the command is currently stopping and drives the shutdown-screen
	// countdown. It is intentionally independent of CmdState because terminate()
	// resets the state to CmdStopped before sending SIGINT (to prevent the exit
	// watcher from misreporting an error), so CmdStopping would vanish from the
	// screen exactly during the grace window.
	stopStartedAt time.Time

	cmd       *exec.Cmd
	ring      *logbuf.Ring
	writer    *logbuf.Writer
	runDir    string
	startDone chan struct{} // closed once the command reaches a terminal-start state (healthy/done/error)

	// monitorDone is closed by superviseService/superviseTask exactly once,
	// right before that goroutine returns for good. startDone alone is not
	// enough to know the monitor goroutine has stopped touching c.cfg/c.policy
	// — it only tracks a single launch attempt, while the monitor goroutine
	// can still be mid-loop (about to read c.cfg.Readiness/c.policy again in
	// runLiveness) after quit is closed. A caller that is about to mutate a
	// live command's cfg/policy (see restartCommandWithNewConfig) must wait on
	// this after closing quit. Created fresh by startMonitor; nil until the
	// command's first monitor is started, so callers must check for nil
	// before receiving (a nil channel receive blocks forever).
	monitorDone chan struct{}

	// exited is closed exactly once by the reaper goroutine when the process
	// has exited; exitErr then holds the process's exit error (nil on exit 0).
	//
	// IMPORTANT: on restart, both exited and startDone are swapped for fresh
	// channels under Engine.mu — they are single-use (close-once). The restart
	// owner goroutine always re-snapshots these under the lock at the top of each
	// loop iteration so it observes the correct current channel.
	exited    chan struct{}
	exitErr   error
	writeOnce sync.Once

	// appliedEnv is the env map last applied to this command's running process
	// (set in launchProcess/relaunch). Compared against effectiveEnv to decide
	// whether a holder join/leave must restart the process to apply a changed
	// merged env.
	appliedEnv map[string]string
}

// Engine supervises environments and their commands.
//
// cfg, cmdOf, envsOf, and envEnvOf are guarded by mu but are never mutated in
// place: a config reload (see ApplyConfig) replaces each with a newly built
// value under the lock, so a reader that copies a reference while holding mu
// keeps a valid, immutable snapshot after releasing it.
type Engine struct {
	cfg *config.Config

	// GracePeriod is how long a service is given to exit after SIGINT before
	// SIGKILL. Exposed as a field so tests can shrink it.
	GracePeriod time.Duration
	// ProbeTimeout / ProbeInterval are the defaults used when a readiness probe
	// does not specify its own. Exposed so tests can shrink them.
	ProbeTimeout  time.Duration
	ProbeInterval time.Duration

	mu       sync.Mutex
	commands map[string]*command
	envState map[string]EnvState

	// authGate serializes the launch-to-ready window of every command flagged
	// InteractiveAuth, so their interactive browser SSO logins never overlap
	// (most providers reject parallel login attempts). Commands without the
	// flag never touch this lock and keep starting concurrently. Always taken
	// as the outer lock relative to mu: gated code only acquires mu briefly
	// underneath it, so there is no lock-ordering deadlock.
	authGate sync.Mutex

	// cmdOf maps command names to their config.
	cmdOf map[string]config.Command

	// envsOf maps a command name to the names of all environments whose workflow
	// includes it. Used by the restart owner goroutine (which outlives
	// runEnvironment) to recompute environment state after a restart.
	envsOf map[string][]string

	// envEnvOf maps an environment name to its configured Env. Read-only after
	// New (like cmdOf/envsOf), used by effectiveEnv to compute the merged env of
	// a shared command from its current holders.
	envEnvOf map[string]map[string]string

	// logsDir is where per-command log files are written. Resolved once at
	// construction; New fails when no per-user cache dir is available rather
	// than falling back to a shared location like os.TempDir(), because command
	// output can contain secrets.
	logsDir string

	events chan Event
}

// buildIndexes builds the lookup tables derived from cfg: cmdOf (command
// name -> config), envsOf (command name -> names of environments whose
// workflow includes it), and envEnvOf (environment name -> its configured
// Env). Returns an error if any workflow step references an undefined
// command.
func buildIndexes(cfg *config.Config) (cmdOf map[string]config.Command, envsOf map[string][]string, envEnvOf map[string]map[string]string, err error) {
	cmdOf = make(map[string]config.Command, len(cfg.Commands))
	for _, c := range cfg.Commands {
		cmdOf[c.Name] = c
	}

	envsOf = make(map[string][]string)
	envEnvOf = make(map[string]map[string]string, len(cfg.Environments))
	for _, env := range cfg.Environments {
		envEnvOf[env.Name] = env.Env
		for _, step := range env.Workflow {
			if _, ok := cmdOf[step.Command]; !ok {
				return nil, nil, nil, fmt.Errorf("engine: environment %q references unknown command %q", env.Name, step.Command)
			}
			envsOf[step.Command] = append(envsOf[step.Command], env.Name)
		}
	}
	return cmdOf, envsOf, envEnvOf, nil
}

// New builds an Engine from cfg, validating that every environment workflow
// references commands that resolve to defined commands.
func New(cfg *config.Config) (*Engine, error) {
	if cfg == nil {
		return nil, fmt.Errorf("engine: nil config")
	}

	cmdOf, envsOf, envEnvOf, err := buildIndexes(cfg)
	if err != nil {
		return nil, err
	}

	envState := make(map[string]EnvState, len(cfg.Environments))
	for _, env := range cfg.Environments {
		envState[env.Name] = EnvStopped
	}

	cacheDir, err := source.CacheDir()
	if err != nil {
		return nil, fmt.Errorf("engine: resolve logs dir: %w", err)
	}

	return &Engine{
		cfg:           cfg,
		GracePeriod:   defaultGracePeriod,
		ProbeTimeout:  defaultProbeTimeout,
		ProbeInterval: defaultProbeTick,
		commands:      make(map[string]*command),
		envState:      envState,
		cmdOf:         cmdOf,
		envsOf:        envsOf,
		envEnvOf:      envEnvOf,
		logsDir:       filepath.Join(cacheDir, "logs"),
		events:        make(chan Event, eventBufferSize),
	}, nil
}

// Environments returns the environments in config order.
func (e *Engine) Environments() []EnvInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]EnvInfo, 0, len(e.cfg.Environments))
	for _, env := range e.cfg.Environments {
		out = append(out, EnvInfo{Name: env.Name, Description: env.Description, AutoStart: env.AutoStart})
	}
	return out
}

// WorkflowCommands returns the command names in the given environment's
// workflow order. Returns nil for an unknown environment.
func (e *Engine) WorkflowCommands(env string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ev := range e.cfg.Environments {
		if ev.Name == env {
			out := make([]string, 0, len(ev.Workflow))
			for _, step := range ev.Workflow {
				out = append(out, step.Command)
			}
			return out
		}
	}
	return nil
}

// EnvState returns the current state of an environment (EnvStopped if unknown).
func (e *Engine) EnvState(env string) EnvState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, ok := e.envState[env]; ok {
		return s
	}
	return EnvStopped
}

// CmdState returns the current state of a command. A command that has never
// been started (or that has been fully torn down) reports CmdPending.
func (e *Engine) CmdState(command string) CmdState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.commands[command]; ok {
		return c.state
	}
	return CmdPending
}

// CmdRetries returns the number of restart attempts consumed in the current
// cycle and the configured maximum. Both values are 0 for a command that has
// never been (or never can be) auto-restarted.
func (e *Engine) CmdRetries(command string) (attempts, max int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.commands[command]; ok {
		return c.retries, c.policy.maxRetries
	}
	return 0, 0
}

// IsUnmanaged reports whether command is currently healthy only because an
// external process already satisfied its readiness probe before env-starter
// spawned anything. False for a command that has never been started, that
// env-starter actually launched itself, or that is unknown.
func (e *Engine) IsUnmanaged(command string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.commands[command]; ok {
		return c.unmanaged
	}
	return false
}

// Logs returns a copy of the command's ring-buffer lines, or an empty slice if
// the command has no logs.
func (e *Engine) Logs(command string) []string {
	e.mu.Lock()
	c, ok := e.commands[command]
	e.mu.Unlock()
	if !ok || c.ring == nil {
		return []string{}
	}
	return c.ring.Lines()
}

// LogPath returns the on-disk path where the given command's logs are written.
func (e *Engine) LogPath(command string) string {
	return e.logPath(command)
}

// PurgeOldLogs removes log files older than logbuf.LogRetention from the logs
// directory. It is safe to call before any command starts. A missing directory
// is not an error. Returns the base names of removed files.
func (e *Engine) PurgeOldLogs() ([]string, error) {
	return logbuf.PurgeOlderThan(e.logsDir, logbuf.LogRetention)
}

// StoppingCommands returns the commands currently being torn down, in config
// order. Each entry carries the time elapsed since stopping began and the
// per-process SIGINT→SIGKILL grace budget. Returns an empty slice when nothing
// is stopping.
func (e *Engine) StoppingCommands() []StoppingCommand {
	grace := e.GracePeriod
	if grace <= 0 {
		grace = defaultGracePeriod
	}

	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()

	var out []StoppingCommand
	for _, cfg := range e.cfg.Commands {
		c, ok := e.commands[cfg.Name]
		if !ok || c.stopStartedAt.IsZero() {
			continue
		}
		elapsed := now.Sub(c.stopStartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		out = append(out, StoppingCommand{
			Command: cfg.Name,
			Elapsed: elapsed,
			Grace:   grace,
		})
	}
	return out
}

// Events returns the buffered event channel. Sends are non-blocking: if no one
// is reading and the buffer is full, events are dropped.
func (e *Engine) Events() <-chan Event {
	return e.events
}

// emit sends an event without ever blocking the engine. If the buffer is full
// the event is dropped (documented behavior).
func (e *Engine) emit(ev Event) {
	select {
	case e.events <- ev:
	default:
	}
}

func (e *Engine) findEnv(name string) (config.Environment, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, env := range e.cfg.Environments {
		if env.Name == name {
			return env, true
		}
	}
	return config.Environment{}, false
}
