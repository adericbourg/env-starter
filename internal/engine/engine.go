// Package engine is the orchestration core of env-starter: a foreground
// supervisor that starts and stops environments made of reference-counted
// commands, respecting the dependency graph declared in each environment's
// workflow.
//
// A command is identified by its name and runs once globally. Starting an
// environment that needs an already-running command just increments its
// reference count (and waits for it to be healthy). Stopping an environment
// decrements reference counts; a command is actually torn down only when its
// reference count reaches zero.
package engine

import (
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/adericbourg/env-starter/internal/config"
	"github.com/adericbourg/env-starter/internal/logbuf"
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
	CmdPending  CmdState = "pending"
	CmdStarting CmdState = "starting"
	CmdHealthy  CmdState = "healthy"
	CmdDone     CmdState = "done"
	CmdStopping CmdState = "stopping"
	CmdError    CmdState = "error"
	CmdStopped  CmdState = "stopped"
)

// EnvInfo is a lightweight description of an environment.
type EnvInfo struct {
	Name        string
	Description string
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
	defaultGracePeriod  = 30 * time.Second
	defaultProbeTimeout = 30 * time.Second
	defaultProbeTick    = 1 * time.Second
	eventBufferSize     = 256
	logRingCapacity     = 1000
	// noProbeGrace is how long a probe-less service is observed before being
	// declared healthy, so an immediate non-zero exit surfaces as an error.
	noProbeGrace = 150 * time.Millisecond
)

// command is the runtime state of a single (global, reference-counted) command.
type command struct {
	cfg config.Command

	state CmdState
	err   error

	refcount int

	// ready is closed when the command becomes healthy or done; readyErr holds
	// the failure (if any) once startDone is closed.
	cmd       *exec.Cmd
	ring      *logbuf.Ring
	writer    *logbuf.Writer
	runDir    string
	startDone chan struct{} // closed once the command reaches a terminal-start state (healthy/done/error)

	// exited is closed exactly once by the reaper goroutine when the process
	// has exited; exitErr then holds the process's exit error (nil on exit 0).
	exited    chan struct{}
	exitErr   error
	writeOnce sync.Once
}

// Engine supervises environments and their commands.
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

	// envOrder / cmdOf preserve config order and map command names to configs.
	envOrder []string
	cmdOf    map[string]config.Command

	events chan Event
}

// New builds an Engine from cfg, validating that every environment workflow
// references commands that resolve to defined commands.
func New(cfg *config.Config) (*Engine, error) {
	if cfg == nil {
		return nil, fmt.Errorf("engine: nil config")
	}

	cmdOf := make(map[string]config.Command, len(cfg.Commands))
	for _, c := range cfg.Commands {
		cmdOf[c.Name] = c
	}

	envOrder := make([]string, 0, len(cfg.Environments))
	envState := make(map[string]EnvState, len(cfg.Environments))
	for _, env := range cfg.Environments {
		envOrder = append(envOrder, env.Name)
		envState[env.Name] = EnvStopped
		for _, step := range env.Workflow {
			if _, ok := cmdOf[step.Command]; !ok {
				return nil, fmt.Errorf("engine: environment %q references unknown command %q", env.Name, step.Command)
			}
		}
	}

	return &Engine{
		cfg:           cfg,
		GracePeriod:   defaultGracePeriod,
		ProbeTimeout:  defaultProbeTimeout,
		ProbeInterval: defaultProbeTick,
		commands:      make(map[string]*command),
		envState:      envState,
		envOrder:      envOrder,
		cmdOf:         cmdOf,
		events:        make(chan Event, eventBufferSize),
	}, nil
}

// Environments returns the environments in config order.
func (e *Engine) Environments() []EnvInfo {
	out := make([]EnvInfo, 0, len(e.cfg.Environments))
	for _, env := range e.cfg.Environments {
		out = append(out, EnvInfo{Name: env.Name, Description: env.Description})
	}
	return out
}

// WorkflowCommands returns the command names in the given environment's
// workflow order. Returns nil for an unknown environment.
func (e *Engine) WorkflowCommands(env string) []string {
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
	for _, env := range e.cfg.Environments {
		if env.Name == name {
			return env, true
		}
	}
	return config.Environment{}, false
}
