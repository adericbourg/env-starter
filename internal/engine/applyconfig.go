package engine

import (
	"fmt"

	"github.com/adericbourg/env-starter/internal/config"
)

// ApplyConfig atomically swaps in a new configuration. It returns an error
// only when the new config is structurally invalid (e.g. a workflow
// references an undefined command), in which case nothing is mutated — the
// running engine and its config are untouched. Environments new to cfg are
// registered as EnvStopped; environments already tracked keep their current
// state.
//
// Reconciling running state against what actually changed — stopping removed
// environments, restarting changed commands and their dependents — is done by
// applyReloadPlan, added in a later change. This function only performs the
// swap.
func (e *Engine) ApplyConfig(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("engine: nil config")
	}

	cmdOf, envsOf, envEnvOf, err := buildIndexes(cfg)
	if err != nil {
		return err
	}

	e.mu.Lock()
	e.cfg = cfg
	e.cmdOf = cmdOf
	e.envsOf = envsOf
	e.envEnvOf = envEnvOf
	for _, env := range cfg.Environments {
		if _, ok := e.envState[env.Name]; !ok {
			e.envState[env.Name] = EnvStopped
		}
	}
	e.mu.Unlock()

	return nil
}
