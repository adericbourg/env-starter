package engine

import (
	"github.com/adericbourg/env-starter/internal/config"
)

// setCmdState updates a command's state and emits a command event. It is safe
// for concurrent use.
func (e *Engine) setCmdState(name string, state CmdState, err error) {
	e.mu.Lock()
	c, ok := e.commands[name]
	if ok {
		c.state = state
		c.err = err
	}
	e.mu.Unlock()

	e.emit(Event{
		Kind:     "command",
		Command:  name,
		CmdState: state,
		Err:      err,
	})
}

// setEnvState updates an environment's state and emits an environment event.
func (e *Engine) setEnvState(name string, state EnvState) {
	e.mu.Lock()
	e.envState[name] = state
	e.mu.Unlock()

	e.emit(Event{
		Kind:        "environment",
		Environment: name,
		EnvState:    state,
	})
}

// recomputeEnvState derives an environment's state from the states of the
// commands in its workflow and emits an environment event.
//
//	all healthy/done          → running
//	none up, at least one err → error
//	mixed (some up, some err) → degraded
func (e *Engine) recomputeEnvState(envCfg config.Environment) {
	e.mu.Lock()
	var up, errored, total int
	for _, step := range envCfg.Workflow {
		total++
		c, ok := e.commands[step.Command]
		if !ok {
			// Never started (e.g. a dependency failed): treat as not-up.
			continue
		}
		switch c.state {
		case CmdHealthy, CmdDone:
			up++
		case CmdError:
			errored++
		}
	}
	e.mu.Unlock()

	var state EnvState
	switch {
	case up == total:
		state = EnvRunning
	case up == 0:
		state = EnvError
	default:
		state = EnvDegraded
	}
	e.setEnvState(envCfg.Name, state)
}
