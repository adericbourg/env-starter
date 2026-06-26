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

// recomputeEnvsFor recomputes state for every environment whose workflow
// contains the named command. Called by the restart owner goroutine after a
// restart completes (success or give-up), since runEnvironment has already
// returned and cannot be used.
func (e *Engine) recomputeEnvsFor(cmdName string) {
	for _, envName := range e.envsOf[cmdName] {
		if !e.isEnvActive(envName) {
			// Never started (or already stopped): a command it merely shares with
			// another environment must not derive its state. Keep it stopped.
			continue
		}
		cfg, ok := e.findEnv(envName)
		if !ok {
			continue
		}
		e.recomputeEnvState(cfg)
	}
}

// isEnvActive reports whether the user has an active engagement with env — it has
// been started and not (yet) stopped. Stopped/stopping envs are excluded so a
// shared command's state changes never resurrect them.
func (e *Engine) isEnvActive(env string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.envState[env]
	if !ok {
		return false
	}
	return s != EnvStopped && s != EnvStopping
}

// recomputeEnvState derives an environment's state from the states of the
// commands in its workflow and emits an environment event.
//
// CmdRestarting is treated as neither up nor errored, so it contributes to
// EnvDegraded (the environment is partially available during a restart cycle).
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
		case CmdError, CmdTimeout:
			errored++
		}
	}
	e.mu.Unlock()

	var state EnvState
	switch up {
	case total:
		state = EnvRunning
	case 0:
		state = EnvError
	default:
		state = EnvDegraded
	}
	e.setEnvState(envCfg.Name, state)
}
